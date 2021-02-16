package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/buildkite/shellwords"
)

func init() {
	notice = nopNotice
}

func TestInvoke(gt *testing.T) {
	t := newCliTest(gt)
	t.In("./testdata", func(t *cliTest) {
		type (
			streams struct {
				stdin          string // <command> | logwrap
				stdout, stderr string
				pipe           string // <command> | logwrap <command>
			}
		)
		const (
			defaultArgs = "-1 '{text}' -2 '{text}'"
		)
		var (
			trim = func(s string) string {
				return trimWhitespace(s, wsBOF, wsBOL)
			}
			args = func(args ...string) (res string) {
				res = defaultArgs
				if len(args) > 0 {
					res += " " + strings.Join(args, " ")
				}
				return
			}
		)

		// Build a binary that we can invoke to print to stdout/stderr.
		t.build("printer.go", printerCode)

		for _, test := range []struct {
			name   string
			args   string
			env    map[string]string
			input  streams
			output streams
			pre    files
			post   files
		}{
			{
				name: "basic",
				args: args(),
				input: streams{
					stdout: `
					123
					456
				`,
					stderr: `
					a
					b
					c
				`,
				},
				output: streams{
					stdout: `
					123
					456
				`,
					stderr: `
					a
					b
					c
				`,
				},
			},
			{
				name: "default name placeholder",
				args: args("-1 '{name}: {text}'"),
				input: streams{
					stdout: `
					a b c
					`,
				},
				output: streams{
					stdout: `
					printer: a b c
					`,
				},
			},
			{
				name: "custom name placeholder",
				args: args("--name test", "-1 '{name}: {text}'"),
				input: streams{
					stdout: `
					a b c
					c b a
					`,
				},
				output: streams{
					stdout: `
					test: a b c
					test: c b a
					`,
				},
			},
			{
				name: "strip ansi",
				args: args("--ansi 2"),
				input: streams{
					stdout: fmt.Sprintf("a %s c", codes["fg"]["red"].wrap("b")),
					stderr: fmt.Sprintf("A %s C", codes["fg"]["red"].wrap("B")),
				},
				output: streams{
					stdout: `
					a b c
					`,
					stderr: fmt.Sprintf("A %s C\n", codes["fg"]["red"].wrap("B")),
				},
			},
			{
				name: "all streams",
				args: args("-1 '1: {text}'", "-2 '2: {text}'"),
				input: streams{
					stdout: `
					a
					b
					`,
					stderr: `
					C
					D
					`,
				},
				output: streams{
					stdout: `
					1: a
					1: b
					`,
					stderr: `
					2: C
					2: D
					`,
				},
				post: files{
					// There's no way to guarantee the write order of the goroutines started
					// by exec.Command.Start().
				},
			},
			{
				name: "env",
				args: "--name test",
				env: map[string]string{
					"LOGWRAP_STDOUT": "OUT: {name}: {text}",
					"LOGWRAP_STDERR": "ERR: {name}: {text}",
					"LOGWRAP_OPTS":   "--name TEST",
				},
				input: streams{
					stdout: "hi",
					stderr: "hello",
				},
				output: streams{
					stdout: `
					OUT: test: hi
					`,
					stderr: `
					ERR: test: hello
					`,
				},
			},
			{
				name: "truncate logfile",
				args: args("-f log", "--max-size 5b"),
				input: streams{
					stdout: `
					test
					TEST
					Test
					`,
				},
				output: streams{
					stdout: `
					test
					TEST
					Test
					`,
				},
				post: files{
					"log": `
					Test
					`,
				},
			},
			{
				name: "rotate logfile",
				args: args("-f log", "--max-size 5b", "--max-count 2"),
				input: streams{
					stdout: `
					test
					TEST
					Test
					`,
				},
				output: streams{
					stdout: `
					test
					TEST
					Test
					`,
				},
				post: files{
					"log": `
					Test
					`,
					"log.0": `
					TEST
					`,
					"log.1": `
					test
					`,
				},
			},
			{
				name: "logfile filename order",
				args: args("-f log", "--max-size 2b", "--max-count 3"),
				input: streams{
					stdout: `
					0
					1
					2
					3
					4
					5
					`,
				},
				output: streams{
					stdout: `
					0
					1
					2
					3
					4
					5
					`,
				},
				post: files{
					"log":   "5\n",
					"log.0": "4\n",
					"log.1": "3\n",
					"log.2": "2\n",
				},
			},
			{
				name: "logfile filename order",
				args: args("-f log", "--max-size 2b", "--max-count 100"),
				input: streams{
					stdout: `
					0
					1
					2
					3
					4
					5
					6
					7
					8
					9
					0
					1
					2
					`,
				},
				output: streams{
					stdout: `
					0
					1
					2
					3
					4
					5
					6
					7
					8
					9
					0
					1
					2
					`,
				},
				post: files{
					"log":    "2\n",
					"log.00": "1\n",
					"log.01": "0\n",
					"log.02": "9\n",
					"log.03": "8\n",
					"log.04": "7\n",
					"log.05": "6\n",
					"log.06": "5\n",
					"log.07": "4\n",
					"log.08": "3\n",
					"log.09": "2\n",
					"log.10": "1\n",
					"log.11": "0\n",
				},
			},
			{
				name: "drop unused logfiles",
				args: args("-f log", "--max-size 4b", "--max-count 3"),
				input: streams{
					stdout: `
					abc
					def
					ghi
					`,
				},
				output: streams{
					stdout: `
					abc
					def
					ghi
					`,
				},
				pre: files{
					"log.013": "A", // newest
					"log.030": "B",
					"log.213": "C",
					"log.481": "D",
					"log.999": "E",
				},
				post: files{
					"log":   "ghi\n",
					"log.0": "def\n",
					"log.1": "abc\n",
					"log.2": "A", // oldest
				},
			},
			{
				name: "drop unused logfiles",
				args: args("-f log", "--max-size 4b", "--max-count 3"),
				input: streams{
					stdout: `
					abc
					def
					`,
				},
				output: streams{
					stdout: `
					abc
					def
					`,
				},
				pre: files{
					"log.01": "A", // newest
					"log.11": "B",
					"log.22": "C",
					"log.33": "X",
					"log.44": "Y",
					"log.55": "Z",
				},
				post: files{
					"log":   "def\n",
					"log.0": "abc\n", // newest
					"log.1": "A",
					"log.2": "B",
				},
			},
			{
				name: "keep but rename unused logfiles if no limit given",
				args: args("-f log", "--max-size 4b"),
				input: streams{
					stdout: `
					abc
					def
					`,
				},
				output: streams{
					stdout: `
					abc
					def
					`,
				},
				pre: files{
					"log.013": "A", // newest
					"log.030": "B",
					"log.213": "C",
					"log.481": "D",
					"log.999": "E",
				},
				post: files{
					"log":   "def\n",
					"log.0": "A",
					"log.1": "B",
					"log.2": "C",
					"log.3": "D",
					"log.4": "E",
				},
			},
			{
				name: "rotate if no space left",
				args: args("-f log", "--max-size 4b"),
				input: streams{
					stdout: `
					abc ABC
					xyz XYZ
					`,
				},
				output: streams{
					stdout: `
					abc ABC
					xyz XYZ
					`,
				},
				pre: files{
					"log": `
					123
					`,
				},
				post: files{
					"log": `
					xyz XYZ
					`,
				},
			},
			{
				name: "rotate if no space left",
				args: args("-f log", "--max-size 4b", "--max-count 1"),
				input: streams{
					stdout: `
					abc ABC
					xyz XYZ
					`,
				},
				output: streams{
					stdout: `
					abc ABC
					xyz XYZ
					`,
				},
				pre: files{
					"log": `
					123
					`,
				},
				post: files{
					"log": `
					xyz XYZ
					`,
					"log.0": `
					abc ABC
					`,
				},
			},
			{
				name: "rotate if no space left",
				args: args("-f log", "--max-size 10b", "--max-count 1"),
				input: streams{
					stdout: `
					new line
					`,
				},
				output: streams{
					stdout: `
					new line
					`,
				},
				pre: files{
					"log": `
					yadda yadda
					`,
				},
				post: files{
					"log": `
					new line
					`,
					"log.0": `
					yadda yadda
					`,
				},
			},
			{
				name: "ensure logfile is closed only after writing",
				args: args("-f log"),
				input: streams{
					stdout: `new line`,
				},
				output: streams{
					stdout: `
					new line
					`,
				},
				pre: files{
					"log": `
					yadda yadda
					`,
				},
				post: files{
					"log": `
					yadda yadda
					new line
					`,
				},
			},
			{
				name: "stdin",
				args: args("-f log"),
				input: streams{
					pipe: `
					hi from stdin
					test
					`,
				},
				output: streams{
					stdout: `
					hi from stdin
					test
					`,
				},
				post: files{
					"log": `
					hi from stdin
					test
					`,
				},
			},
			{
				name: "stdin piped to command",
				args: args("-f log -1 '{name}: {text}'"),
				input: streams{
					stdin: `
					hi from stdin
					`,
				},
				output: streams{
					stdout: `
					printer: hi from stdin
					`,
				},
				post: files{
					"log": `
					printer: hi from stdin
					`,
				},
			},
			{
				name: "placeholders tryout",
				args: args("-1 '{name}{rjust 6 {name}}' --name test"),
				input: streams{
					stdin: `
					asdf
					`,
				},
				output: streams{
					stdout: `
						test  test
						`,
				},
			},
		} {
			t.Run(test.name, func(t *cliTest) {
				// Populate the directory with "pre-existing" files.
				for file, content := range test.pre {
					t.write(file, trim(content))
				}

				// Set the environment.
				for k, v := range test.env {
					t.env(k, v)
				}

				// Mock the inputs to files.
				for _, input := range []struct {
					name string
					data string
				}{
					{"STDIN", trim(test.input.stdin)},
					{"STDOUT", trim(test.input.stdout)},
					{"STDERR", trim(test.input.stderr)},
				} {
					if input.data != "" {
						t.write(input.name, input.data)
					}
				}

				// Prep args and invoke.
				var (
					stdout, stderr bytes.Buffer
					stdin          io.Reader
					argfmt         = "%s --"
				)
				if test.input.pipe != "" {
					stdin = strings.NewReader(trim(test.input.pipe))
				} else {
					argfmt += " ./printer"
				}
				args, err := shellwords.Split(fmt.Sprintf(argfmt, test.args))
				if err != nil {
					t.Fatal(err)
				}
				if err := invoke(stdin, &stdout, &stderr, args); err != nil {
					t.Fatal(err)
				}

				// Check if the generated files match the test content.
				for _, file := range exclude(t.ls("."), "printer*", "STD*") {
					if !test.post.has(file) {
						t.Errorf("\nextra file: %s: %q", file, t.read(file))
					}
				}
				for _, file := range test.post.names() { // maintain an order
					content := test.post.content(file)
					if !t.exists(file) {
						t.Errorf("\nmissing file: %s: %q", file, content)
						continue
					}
					if exp, got := content, t.read(file); exp != got {
						t.Errorf("\n%s: -%q +%q", file, exp, got)
					}
				}

				// Check outputs.
				for _, o := range []struct {
					name     string
					exp, got string
				}{
					{"stdout", trim(test.output.stdout), stdout.String()},
					{"stderr", trim(test.output.stderr), stderr.String()},
				} {
					if o.exp != o.got {
						t.Errorf("\n%s: -%q +%q", o.name, o.exp, o.got)
					}
				}
			})
		}
	})
}

type files map[string]string

func (fs files) has(f string) (ok bool) {
	if fs == nil {
		return
	}
	_, ok = fs[f]
	return
}

func (fs files) content(f string) string {
	if fs == nil {
		return ""
	}
	return trimWhitespace(fs[f], wsBOF, wsBOL)
}

func (fs files) names() (res []string) {
	for f := range fs {
		res = append(res, f)
	}
	sort.Strings(res)
	return
}

const printerCode = `
package main

import (
    "os"
    "io"
    "io/ioutil"
    "bytes"
)

func main() {
	in, _ := ioutil.ReadFile("./STDIN")
	out, _ := ioutil.ReadFile("./STDOUT")
	err, _ := ioutil.ReadFile("./STDERR")

	if len(in) > 0 {
		io.Copy(os.Stdout, bytes.NewReader(in))
		return
	}

	if len(out) > 0 {
		io.Copy(os.Stdout, bytes.NewReader(out))
	}
	if len(err) > 0 {
		io.Copy(os.Stderr, bytes.NewReader(err))
	}
}
`

func newCliTest(t *testing.T) *cliTest {
	ct := &cliTest{
		T:     t,
		reset: func() {},
	}
	return ct
}

type cliTest struct {
	*testing.T
	reset func()
}

func (t *cliTest) Run(name string, fn func(*cliTest)) {
	t.T.Run(name, func(T *testing.T) {
		t := newCliTest(T)
		defer t.Reset()
		fn(t)
	})
}

func (t *cliTest) RunIn(name, dir string, fn func(*cliTest)) {
	t.Run(name, func(t *cliTest) {
		t.mkdir(dir)
		defer t.cd(t.cd(dir))
		fn(t)
	})
}

func (t *cliTest) In(dir string, fn func(*cliTest)) {
	t.mkdir(dir)
	defer t.Reset()
	defer t.cd(t.cd(dir))
	fn(t)
}

func (t *cliTest) Reset() {
	if t.reset != nil {
		t.reset()
	}
}

func (t *cliTest) ensure(do func()) {
	old, new := t.reset, do
	t.reset = func() {
		defer old()
		new()
	}
}

func (t *cliTest) register(path string) {
	path = filepath.FromSlash(path)
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	t.ensure(func() { os.Remove(abs) })
}

func (t *cliTest) env(k, v string) {
	prev, ok := os.LookupEnv(k)
	if err := os.Setenv(k, v); err != nil {
		t.Fatal(err)
	}

	var reset func()
	if ok {
		reset = func() { os.Setenv(k, prev) }
	} else {
		reset = func() { os.Unsetenv(k) }
	}
	t.ensure(reset)
}

func (t *cliTest) pwd() string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

func (t *cliTest) cd(dir string) string {
	dir = filepath.FromSlash(dir)
	t.Helper()
	wd := t.pwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return wd
}

func (t *cliTest) ls(dir string) []string {
	d, err := os.Open(dir)
	if err != nil {
		t.Fatal("ls:", err)
	}
	fs, err := d.Readdirnames(0)
	if err != nil {
		t.Fatal("ls:", err)
	}
	sort.Strings(fs)
	return fs
}

func (t *cliTest) exists(path string) bool {
	path = filepath.FromSlash(path)
	_, err := os.Stat(path)
	return err == nil
}

func (t *cliTest) mkdir(dir string) {
	dir = filepath.FromSlash(dir)
	t.Helper()
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	t.ensure(func() { os.Remove(dir) })
}

func (t *cliTest) read(src interface{}) (res string) {
	var (
		bs  []byte
		err error
	)
	switch src := src.(type) {
	case io.Reader:
		bs, err = ioutil.ReadAll(src)
	case string:
		src = filepath.FromSlash(src)
		bs, err = ioutil.ReadFile(src)
		if err == nil {
			t.register(src)
		}
	default:
		t.Errorf("read: invalid type: %T", src)
	}
	if err != nil {
		t.Error(err)
	}
	return string(bs)
}

func (t *cliTest) write(dst interface{}, content string) {
	var err error
	switch dst := dst.(type) {
	case io.Writer:
		_, err = io.WriteString(dst, content)
	case string: // path
		err = ioutil.WriteFile(dst, []byte(content), 0644)
		if err == nil {
			t.register(dst)
		}
	default:
		t.Errorf("write: invalid type: %T", dst)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func (t *cliTest) build(file, content string) {
	t.write(file, content)
	cmd := exec.Command("go", "build", file)
	buf := new(bytes.Buffer)
	cmd.Stderr = buf
	if err := cmd.Run(); err != nil {
		t.Fatalf("\nbuild: %s\n%s", err, buf.String())
	}

	// Remember to delete the binary as well.
	t.register(strings.TrimSuffix(file, filepath.Ext(file)))
}
