package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/buildkite/shellwords"
)

const app = "logwrap"

var version = "dev"

func main() {
	var stdin io.Reader
	if isPipe(os.Stdin) {
		stdin = os.Stdin
	}
	switch err := invoke(stdin, os.Stdout, os.Stderr, os.Args[1:]).(type) {
	case nil:
		return
	case *templateError:
		pre := fmt.Sprintf("%s: %s:%d", app, err.name, err.pos)
		fmt.Fprintf(os.Stderr, "%s %s\n", pre, err.text)
		fmt.Fprintf(os.Stderr, "%s %*s %s\n", pre, err.pos+1, "^", err.cause)
	default:
		fmt.Fprintf(os.Stderr, "%s: %s\n", app, err)
	}
	os.Exit(1)
}

func invoke(stdin io.Reader, stdout, stderr io.Writer, args []string) error {
	inv, err := newInvocation(stdin, stdout, stderr, args)
	if err != nil {
		return err
	}
	err = inv.invoke()
	if err_ := inv.cleanup(); err == nil {
		err = err_
	}
	if _, ok := err.(*exec.ExitError); ok {
		err = fmt.Errorf("%s: %s", inv.bin, err)
	}
	return err
}

func newInvocation(stdin io.Reader, stdout, stderr io.Writer, args []string) (*invocation, error) {
	var flags struct {
		name      string
		maxSize   sizeFlag
		maxCount  uint
		file      string
		templates struct {
			stdout, stderr string
		}
		ansi ansiFlag
	}
	fs := flag.NewFlagSet(app, flag.ContinueOnError)
	fs.Usage = nil
	fs.SetOutput(ioutil.Discard)
	fs.StringVar(&flags.templates.stdout, "stdout", defaultStdoutTemplate, "")
	fs.StringVar(&flags.templates.stdout, "1", defaultStdoutTemplate, "")
	fs.StringVar(&flags.templates.stderr, "stderr", defaultStderrTemplate, "")
	fs.StringVar(&flags.templates.stderr, "2", defaultStderrTemplate, "")
	fs.StringVar(&flags.file, "file", "", "")
	fs.StringVar(&flags.file, "f", "", "")
	fs.StringVar(&flags.name, "name", "", "")
	fs.StringVar(&flags.name, "n", "", "")
	fs.UintVar(&flags.maxCount, "max-count", 0, "")
	fs.UintVar(&flags.maxCount, "c", 0, "")
	fs.Var(&flags.maxSize, "max-size", "")
	fs.Var(&flags.maxSize, "s", "")
	fs.Var(&flags.ansi, "ansi", "")
	fs.Var(&flags.ansi, "a", "")
	flags.ansi.stdout = true
	flags.ansi.stderr = true

	var quiet, help, ver bool
	fs.BoolVar(&quiet, "quiet", false, "")
	fs.BoolVar(&quiet, "q", false, "")
	fs.BoolVar(&help, "help", false, "")
	fs.BoolVar(&help, "h", false, "")
	fs.BoolVar(&ver, "version", false, "")
	fs.BoolVar(&ver, "v", false, "")

	if err := parseEnv(fs); err != nil {
		return nil, err
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	inv := &invocation{
		args:         fs.Args(),
		stdin:        stdin,
		stdout:       stdout,
		stderr:       stderr,
		placeholders: defaultPlaceholders(),
		cleanup:      func() error { return nil },
	}

	if quiet {
		inv.stdout = ioutil.Discard
		inv.stderr = ioutil.Discard
		if flags.file == "" {
			// Abort if we're quiet and no logfile is specified, otherwise we'd
			// just run the command and discard its output.
			return nil, errors.New("nothing to do: too quiet")
		}
	}

	var (
		reading = inv.stdin != nil && len(inv.args) == 0
		helping = help || len(inv.args) == 0
		hooks   []func() error
	)

	setBin := func() error {
		inv.bin = inv.args[0]
		inv.args = inv.args[1:]
		return nil
	}

	setName := func() error {
		inv.name = flags.name
		switch {
		case inv.name == "" && reading:
			inv.name = "stdin"
		case inv.name == "":
			inv.name = filepath.Base(inv.bin)
		}
		inv.constant("name", inv.name)
		return nil
	}

	setPath := func() error {
		inv.set("path", placeholderFunc(func(args []string) (string, error) {
			if len(args) == 0 {
				return inv.bin, nil
			}

			abs := abspath(inv.bin)
			switch strings.ToLower(args[0]) {
			case "absolute", "abs":
				return abs, nil
			case "relative", "rel":
				cwd, err := os.Getwd()
				if err != nil {
					return "", err
				}
				return filepath.Rel(cwd, abs)
			default:
				return inv.bin, nil
			}
		}))
		return nil
	}

	setLog := func() error {
		if flags.file == "" {
			return nil
		}
		if flags.maxCount > 0 && flags.maxSize == 0 {
			return errors.New("unable to determine when to rotate logfiles without a maximum size")
		}
		var (
			f   io.WriteCloser
			err error
		)
		if flags.maxSize > 0 {
			f, err = newFileRotator(
				flags.file,
				int64(flags.maxSize),
				int(flags.maxCount),
			)
		} else {
			f, err = openLogfile(flags.file)
		}
		if err != nil {
			return err
		}
		inv.log = f
		if !flags.ansi.file {
			inv.log = &ansiStripper{inv.log}
		}

		// Write out a notice message when we're done with the log.
		init := time.Now()
		inv.log = newCloseWriter(inv.log,
			func(w io.Writer) error {
				msg := "finished %s after %s/%s"
				args := []interface{}{bold(inv.name), ms(time.Since(init)), humanBytes(inv.bytes)}
				if inv.rc != nil {
					msg += ": %s"
					args = append(args, inv.rc)
				}
				return notice(w, msg, args...)
			},
		)
		inv.ensureLast(inv.log.Close)

		// Also write a notice message when initialized.
		return notice(inv.log, "started %s", bold(inv.name))
	}

	setOutputs := func() error {
		var (
			stdout = flags.templates.stdout
			stderr = flags.templates.stderr
		)
		// Abort if the user specifically set both templates to the empty string.
		if stdout == "" && stderr == "" {
			return errors.New("nothing to do: no templates defined")
		}

		for _, c := range []struct {
			stream   *io.Writer
			name     string
			template string
			ansi     bool
		}{
			{&inv.stdout, "stdout", stdout, flags.ansi.stdout},
			{&inv.stderr, "stderr", stderr, flags.ansi.stderr},
		} {
			// Discard output if no template set.
			if c.template == "" {
				*c.stream = ioutil.Discard
				continue
			}

			tmpl, err := newTemplate(c.name, c.template, inv.placeholders)
			if err != nil {
				return err
			}
			output := *c.stream
			if !c.ansi {
				output = &ansiStripper{output}
			}
			if inv.log != nil {
				output = io.MultiWriter(output, inv.log)
			}
			lw := &linewiseWriter{
				Writer: &templateWriter{
					template: tmpl,
					Writer:   output,
				},
			}
			inv.ensureFirst(lw.Close)
			*c.stream = lw
		}
		return nil
	}

	switch {
	case ver:
		inv.invoke = inv.doVersion
	case reading:
		hooks = []func() error{
			setName,
			setLog,
			setOutputs,
		}
		inv.invoke = inv.doRead
	case helping:
		inv.invoke = inv.doHelp
	default:
		hooks = []func() error{
			setBin,
			setName,
			setPath,
			setLog,
			setOutputs,
		}
		inv.invoke = inv.doRun
	}
	for _, fn := range hooks {
		if err := fn(); err != nil {
			return nil, err
		}
	}
	return inv, nil
}

func parseEnv(fs *flag.FlagSet) error {
	for _, env := range []struct {
		name string
		do   func(string) error
	}{
		{"OPTS", func(s string) error {
			args, err := shellwords.Split(s)
			if err != nil {
				return fmt.Errorf("variable contains unbalanced quotes")
			}
			return fs.Parse(args)
		}},
		{"STDOUT", func(s string) error { return fs.Set("stdout", s) }},
		{"STDERR", func(s string) error { return fs.Set("stderr", s) }},
	} {
		key := fmt.Sprintf("%s_%s", strings.ToUpper(app), env.name)
		if val, ok := os.LookupEnv(key); ok {
			if err := env.do(val); err != nil {
				return fmt.Errorf("%s: %s", key, err)
			}
		}
	}
	return nil
}

type invocation struct {
	name           string
	bin            string
	args           []string
	log            io.WriteCloser
	stdin          io.Reader
	stdout, stderr io.Writer
	placeholders

	// These are set at parse time.
	invoke  func() error
	cleanup func() error

	rc    error  // non-nil if doRun/doRead fails
	bytes uint64 // pure byte count for stdin or stdout+stderr
}

func (inv *invocation) ensureFirst(fn func() error) { inv.ensure(true, fn) }
func (inv *invocation) ensureLast(fn func() error)  { inv.ensure(false, fn) }

func (inv *invocation) ensure(prepend bool, fn func() error) {
	if inv.cleanup != nil {
		first, last := inv.cleanup, fn
		if prepend {
			first, last = last, first
		}
		fn = func() error {
			err := first()
			if err_ := last(); err == nil {
				err = err_
			}
			return err
		}
	}
	inv.cleanup = fn
}

func (inv *invocation) doRun() (err error) {
	defer func() { inv.rc = err }()

	cmd := exec.Command(inv.bin, inv.args...)
	cmd.Stdin = inv.stdin
	cmd.Stdout, cmd.Stderr = newInterlockedWriterPair(
		&byteCounter{Writer: inv.stdout, n: &inv.bytes},
		&byteCounter{Writer: inv.stderr, n: &inv.bytes},
	)

	if err := cmd.Start(); err != nil {
		return err
	}
	inv.constant("pid", strconv.Itoa(cmd.Process.Pid))

	// Capture SIGINT, SIGQUIT and SIGTERM and try to exit gracefully.
	wait := make(chan struct{})
	sigch := make(chan os.Signal, 1)
	signal.Notify(sigch,
		syscall.SIGINT,
		syscall.SIGQUIT,
		syscall.SIGTERM,
		// syscall.SIGCHLD, // not available on Windows
	)
	go func() {
		signam := map[os.Signal]string{
			syscall.SIGINT:  "SIGINT",
			syscall.SIGQUIT: "SIGQUIT",
			syscall.SIGTERM: "SIGTERM",
			syscall.SIGKILL: "SIGKILL",
		}
		const (
			interruptWindow = 2 * time.Second
			killDelay       = 3 * time.Second
		)
		var (
			lastSig   os.Signal
			lastSigAt time.Time
			killing   bool
			kill      = time.NewTimer(math.MaxInt64) // we don't want this to fire prematurely
		)
		notify := func(sig os.Signal) {
			msgf := "sent %s to %s (pid %d)"
			args := []interface{}{signam[sig], inv.bin, cmd.Process.Pid}
			switch sig {
			case syscall.SIGINT:
				msgf += "; send again within %s to terminate..."
				args = append(args, interruptWindow)
			case syscall.SIGTERM, syscall.SIGQUIT:
				msgf += "; waiting for %s before killing..."
				args = append(args, killDelay)
				kill.Reset(killDelay)
				killing = true
			case syscall.SIGKILL:
			}
			notice(os.Stderr, msgf, args...)
		}
		for {
			select {
			case <-kill.C:
				cmd.Process.Signal(syscall.SIGKILL)
				notify(syscall.SIGKILL)
			case sig := <-sigch:
				switch {
				case killing:
					// Ignore any signal until the subprocess is killed.
				case sig == syscall.SIGINT && lastSig == syscall.SIGINT && time.Since(lastSigAt) <= interruptWindow:
					// Attempt to terminate the subprocess if multiple
					// interrupts are received within a time window.
					cmd.Process.Signal(syscall.SIGTERM)
					notify(syscall.SIGTERM)
				case sig == syscall.SIGINT:
					cmd.Process.Signal(syscall.SIGINT)
					notify(sig)
				case sig == syscall.SIGQUIT, sig == syscall.SIGTERM:
					cmd.Process.Signal(sig)
					notify(sig)
				default:
					panic(fmt.Sprintf("unhandled signal: %s", sig))
				}
				lastSigAt, lastSig = time.Now(), sig
			case <-wait:
				return
			}
		}
	}()

	err = cmd.Wait()
	close(wait) // kill the signal handler goroutine
	return
}

func (inv *invocation) doRead() error {
	n, err := io.Copy(inv.stdout, inv.stdin)
	inv.rc = err
	inv.bytes = uint64(n)
	return err
}

func (inv *invocation) doHelp() error {
	if len(inv.args) == 0 {
		return inv.errln(help(""))
	}

	// Print only what's valid, and separate help messages by a title box.
	args := uniq(strs(inv.args).transform(strings.ToLower))
	msgs := make(map[string]string)
	for _, arg := range args {
		msg := help(arg)
		if msg == "" {
			continue
		}
		msgs[arg] = msg
	}
	if len(msgs) == 0 {
		return inv.errln(help(""))
	}

	for _, arg := range args {
		msg, ok := msgs[arg]
		if !ok {
			continue
		}
		if len(msgs) > 1 {
			inv.errln(drawBox(arg))
		}
		inv.errln(msg)
	}
	return nil
}

func (inv *invocation) doVersion() error {
	return inv.outln(version)
}

func (inv *invocation) errln(args ...interface{}) error {
	return inv.errf("%s\n", args...)
}

func (inv *invocation) errf(s string, args ...interface{}) error {
	return inv.fprintf(inv.stderr, s, args...)
}

func (inv *invocation) outln(args ...interface{}) error {
	return inv.outf("%s\n", args...)
}

func (inv *invocation) outf(s string, args ...interface{}) error {
	return inv.fprintf(inv.stdout, s, args...)
}

func (inv *invocation) fprintf(w io.Writer, s string, args ...interface{}) error {
	_, err := fmt.Fprintf(w, s, args...)
	return err
}

type noticeFunc func(io.Writer, string, ...interface{}) error

var notice noticeFunc = func(w io.Writer, fs string, args ...interface{}) error {
	_, err := fmt.Fprintf(w, "%s %s: %s\n", defaultTimestamp(), app, fmt.Sprintf(fs, args...))
	return err
}

// nopNotice replaces notice during tests.
func nopNotice(io.Writer, string, ...interface{}) error {
	return nil
}

type sizeFlag uint64

func (f *sizeFlag) Set(s string) error {
	val, err := parseSize(s)
	if err != nil {
		return err
	}
	*f = sizeFlag(val)
	return nil
}

func (f *sizeFlag) String() string {
	return strconv.FormatUint(uint64(*f), 10)
}

type ansiFlag struct {
	stdout, stderr, file bool
}

func (f *ansiFlag) reset() {
	f.stdout = false
	f.stderr = false
	f.file = false
}

func (f *ansiFlag) Set(s string) error {
	f.reset()
	s = strings.TrimSpace(s)
	if s == "-" || s == "" {
		return nil
	}
	for _, r := range s {
		switch r {
		case '1':
			f.stdout = true
		case '2':
			f.stderr = true
		case 'f':
			f.file = true
		case '-':
			f.reset()
		case ' ':
			continue
		default:
			return fmt.Errorf("no such stream: %q", r)
		}
	}
	return nil
}

func (f *ansiFlag) String() string {
	rs := make([]rune, 0, 3)
	if f.stdout {
		rs = append(rs, '1')
	}
	if f.stderr {
		rs = append(rs, '2')
	}
	if f.file {
		rs = append(rs, 'f')
	}
	return string(rs)
}
