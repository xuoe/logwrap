package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"sort"
	"strconv"
	"strings"
	gotemplate "text/template"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/buildkite/shellwords"
	"github.com/mackerelio/go-osstat/uptime"
)

const (
	defaultStdoutTemplate = `{ts} {fg green [{name}]} {text}`
	defaultStderrTemplate = `{ts} {fg red [{name}]} {text}`
)

func newTemplate(name, text string, ps placeholders) (*template, error) {
	var (
		t = &template{
			name:         name,
			text:         text,
			elems:        make([]templateElem, 0, 6),
			placeholders: ps,
			cache:        make(map[string]*cachedPlaceholder),
		}
		p = &templateParser{
			template: t,
		}
	)

PARSE:
	for {
		elem, err := p.parseElem()
		if elem != nil {
			t.elems = append(t.elems, elem)
		}
		switch err {
		case nil:
		case io.EOF:
			break PARSE
		default:
			return nil, err
		}
	}
	return t, nil
}

var (
	errEmptyPlaceholder           = errors.New("empty placeholder")
	errMissingPlaceholderName     = errors.New("missing placeholder name")
	errNoSuchPlaceholder          = errors.New("no such placeholder")
	errUnexpectedClosingDelimiter = errors.New("unexpected closing delimiter")
	errUnterminatedPlaceholder    = errors.New("unterminated placeholder")
	errExpectedSpace              = errors.New("expected a space character")
)

const eof rune = -1

type templateParser struct {
	*template
	pos int
}

func (p *templateParser) parseElem() (elem templateElem, err error) {
	var sb strings.Builder
PARSE:
	for {
		r := p.next()
		switch {
		case r == '{':
			p.prev()
			if sb.Len() > 0 {
				return textElem(sb.String()), nil
			}
			return p.parsePlaceholder()
		case r == '}':
			return nil, p.errAt(p.pos-1, errUnexpectedClosingDelimiter)
		case r == eof:
			err = io.EOF
			break PARSE
		}
		sb.WriteRune(r)
	}
	if sb.Len() > 0 {
		elem = textElem(sb.String())
	}
	return
}

func (p *templateParser) parsePlaceholder() (templateElem, error) {
	var (
		sb    strings.Builder
		pos   = p.pos
		elems []templateElem
	)

	p.next() // skip over delimiter
	p.skipWhitespace()

	name, err := p.parsePlaceholderName()
	if err != nil {
		return nil, err
	}

	p.skipWhitespace()
PARSE:
	for {
		switch r := p.next(); r {
		case eof:
			return nil, p.errAt(pos+1, errUnterminatedPlaceholder)
		case '{':
			if sb.Len() > 0 {
				elems = append(elems, textElem(sb.String()))
				sb.Reset()
			}
			p.prev() // hold this position
			elem, err := p.parsePlaceholder()
			if err != nil {
				return nil, err
			}
			elems = append(elems, elem)
		case '}':
			break PARSE
		default:
			sb.WriteRune(r)
		}
	}

	// We have a nested placeholder.
	if len(elems) > 0 {
		// Because {text} has no actual definition, we can't check/ignore its
		// arguments there, so we ignore it here.
		if name != "text" {
			// Any leftovers become text arguments.
			if sb.Len() > 0 {
				elems = append(elems, textElem(sb.String()))
			}
			return &nestedPlaceholderElem{name, elems}, nil
		}
	}

	// Otherwise, we have a regular placeholder.
	res := &placeholderElem{name: name}
	if sb.Len() > 0 {
		s := sb.String()
		args, err := shellwords.SplitPosix(s)
		if err != nil {
			return nil, p.errAt(pos+1, fmt.Errorf("placeholder arguments contain unbalanced quotes"))
		}
		res.args = args
	}
	return res, nil
}

func (p *templateParser) parsePlaceholderName() (string, error) {
	var (
		sb     strings.Builder
		pos    = p.pos
		closed bool
	)

PARSE:
	for {
		switch r := p.next(); r {
		case '{':
			if sb.Len() > 0 {
				return "", p.errAt(p.pos-1, errExpectedSpace)
			}
			return "", p.errAt(p.pos-pos, errMissingPlaceholderName)
		case '}':
			closed = true
			fallthrough
		case ' ':
			p.prev()
			fallthrough
		case eof:
			break PARSE
		default:
			sb.WriteRune(r)
		}
	}

	name := sb.String()
	switch {
	case name == "" && closed:
		return "", p.errAt(pos-1, errEmptyPlaceholder)
	case name == "":
		return "", p.errAt(pos, errMissingPlaceholderName)
	case !p.has(name):
		return "", p.errAt(pos, errNoSuchPlaceholder)
	}
	return name, nil
}

func (p *templateParser) skipWhitespace() { p.skipWhile(unicode.IsSpace) }

func (p *templateParser) skipWhile(fn func(rune) bool) {
	for fn(p.next()) {
	}
	p.prev()
}

func (p *templateParser) next() rune {
	if p.pos == len(p.text) {
		return eof
	}
	r, s := utf8.DecodeRuneInString(p.text[p.pos:])
	p.pos += s
	return r
}

func (p *templateParser) prev() rune {
	if p.pos == 0 {
		return eof
	}
	r, s := utf8.DecodeLastRuneInString(p.text[:p.pos])
	p.pos -= s
	return r
}

func (p *templateParser) errAt(pos int, err error) error {
	return &templateError{
		name:  p.name,
		text:  string(p.text),
		pos:   pos,
		cause: err,
	}
}

type templateError struct {
	name  string
	text  string
	pos   int
	cause error
}

func (e *templateError) Error() string {
	return fmt.Sprintf("%s:%d: %s", e.name, e.pos+1, e.cause)
}

type template struct {
	name  string
	text  string
	elems []templateElem
	placeholders
	cache map[string]*cachedPlaceholder
}

func (t *template) render(w io.Writer, text []byte) (n int, err error) {
	defer t.dropCache()
	for _, elem := range t.elems {
		var c int
		c, err = t.renderElem(w, text, elem)
		n += c
		if err != nil {
			return
		}
	}
	return
}

func (t *template) renderElem(w io.Writer, text []byte, elem templateElem) (n int, err error) {
	switch elem := elem.(type) {
	case textElem:
		n, err = io.WriteString(w, string(elem))
	case *placeholderElem:
		switch elem.name {
		case "text":
			n, err = w.Write(text)
		default:
			s := t.apply(elem.name, elem.args)
			n, err = io.WriteString(w, s)
		}
	case *nestedPlaceholderElem:
		var args strings.Builder
		for _, arg := range elem.elems {
			switch arg := arg.(type) {
			case textElem:
				args.WriteString(string(arg))
			default:
				// Render inner placeholders into <args>, ensuring quotes are escaped.
				if _, err = t.renderElem(&quoteEscaper{Writer: &args}, text, arg); err != nil {
					return
				}
			}
		}

		var s string
		{
			s = args.String()
			args, err := shellwords.SplitPosix(s)
			if err != nil {
				s = fmt.Sprintf("{%s: bad quoting in: %s}", elem.name, s)
			} else {
				s = t.apply(elem.name, args)
			}
		}
		if len(s) > 0 {
			n, err = io.WriteString(w, s)
		}
	}
	return
}

func (t *template) renderString(s string) (string, error) {
	var out bytes.Buffer
	if _, err := t.render(&out, []byte(s)); err != nil {
		return "", err
	}
	return out.String(), nil
}

func (t *template) apply(name string, args []string) (s string) {
	var err error
APPLY:
	p := t.get(name)
	if p == nil {
		err = errors.New("n/a")
	} else if err = t.check(name, args); err == nil {
		switch p := p.(type) {
		case placeholderFunc:
			s, err = p(args)
		case cyclicPlaceholder:
			s, err = t.tryCache(name, p, args)
		case placeholderMaker:
			t.set(name, p(args))
			goto APPLY
		default:
			panic(fmt.Sprintf("%s: unexpected placeholder type: %T", name, p))
		}
	}
	if err != nil {
		s = fmt.Sprintf("{%s: %s}", name, err)
	}
	return
}

func (t *template) tryCache(name string, p placeholder, args []string) (s string, err error) {
	id := name + strings.Join(args, "")
	cache, ok := t.cache[id]
	if ok {
		return cache.string, cache.error
	}

	switch p := p.(type) {
	case cyclicPlaceholder:
		s, err = p(args)
	default:
		panic(fmt.Sprintf("unexpected placeholder type: %T", p))
	}
	t.cache[id] = &cachedPlaceholder{
		placeholder: p,
		string:      s,
		error:       err,
	}
	return
}

func (t *template) dropCache() {
	for name, cache := range t.cache {
		switch cache.placeholder.(type) {
		case cyclicPlaceholder:
			delete(t.cache, name)
		default:
			panic(fmt.Sprintf("%s: uncacheable placeholder type: %T", name, cache.placeholder))
		}
	}
}

type cachedPlaceholder struct {
	placeholder
	string
	error
}

func (t *template) String() string {
	var sb strings.Builder
	for _, elem := range t.elems {
		fmt.Fprint(&sb, elem)
	}
	return sb.String()
}

type templateElem interface {
	elem()
	fmt.Stringer
}

type textElem string

func (textElem) elem() {}

func (e textElem) String() string {
	return string(e)
}

type placeholderElem struct {
	name string
	args []string
}

func (placeholderElem) elem() {}

func (e *placeholderElem) String() string {
	var sb strings.Builder
	fmt.Fprint(&sb, "{")
	fmt.Fprint(&sb, e.name)
	if len(e.args) > 0 {
		fmt.Fprint(&sb, " ")
		fmt.Fprint(&sb, strings.Join(strs(e.args).transform(strconv.Quote), " "))
	}
	fmt.Fprint(&sb, "}")
	return sb.String()
}

type nestedPlaceholderElem struct {
	name  string
	elems []templateElem
}

func (e *nestedPlaceholderElem) String() string {
	var sb strings.Builder
	fmt.Fprint(&sb, "{")
	fmt.Fprint(&sb, e.name)
	fmt.Fprint(&sb, " ")
	for _, elem := range e.elems {
		fmt.Fprint(&sb, elem)
	}
	fmt.Fprint(&sb, "}")
	return sb.String()
}

func (nestedPlaceholderElem) elem() {}

type placeholder interface{ placeholder() }

type (
	// placeholderFunc is called whenever referenced by the template.
	placeholderFunc func([]string) (string, error)

	// cyclicPlaceholder is called once per render cycle; the result of the
	// first call is cached for the duration of the render cycle.
	cyclicPlaceholder func([]string) (string, error)

	// placeholderMaker creates other placeholders at render time.
	placeholderMaker func([]string) placeholder
)

func (placeholderFunc) placeholder()   {}
func (placeholderMaker) placeholder()  {}
func (cyclicPlaceholder) placeholder() {}

type placeholders map[string]placeholder

// has returns whether a placeholder is defined, not whether its value is nil.
func (ps placeholders) has(name string) bool {
	_, ok := ps[name]
	return ok
}

func (ps placeholders) check(name string, args []string) error {
	if _, ok := ps[name]; !ok {
		return nil
	}
	def, ok := placeholderDefs[name]
	if !ok {
		// Safe to ignore; occurs for custom placeholders during tests.
		return nil
	}
	_, err := def.usage.check(args)
	return err
}

func (ps placeholders) get(name string) placeholder { return ps[name] }

func (ps placeholders) set(name string, p placeholder) { ps[name] = p }

func (ps placeholders) constant(name, val string) {
	ps.set(name, placeholderFunc(func([]string) (string, error) {
		return val, nil
	}))
}

func defaultPlaceholders() placeholders {
	ps := make(placeholders, len(placeholderNames))
	for _, name := range placeholderNames {
		ps[name] = placeholderDefs[name].fn
	}
	return ps
}

var (
	placeholderNames = make([]string, 0, 16)
	placeholderDefs  = make(map[string]placeholderDef, 16)
)

type placeholderDef struct {
	help  string
	usage usageSpec
	fn    placeholder
}

func init() {
	var (
		color = func(kind, id, text string) (string, error) {
			c, ok := codes[kind][strings.ToLower(strings.TrimSpace(id))]
			if !ok {
				return "", fmt.Errorf("no such color: %s", id)
			}
			if text == "" {
				return c.String(), nil
			}
			return c.wrap(text), nil
		}
		attr = func(id, text string) (string, error) {
			c, ok := codes["attrs"][id]
			if !ok {
				// attrs are never requested by the user directly, so panic here.
				panic(fmt.Sprintf("no such attribute: %s", id))
			}
			if text == "" {
				return c.String(), nil
			}
			return c.wrap(text), nil
		}

		fg = func(id, text string) (string, error) { return color("fg", id, text) }
		bg = func(id, text string) (string, error) { return color("bg", id, text) }

		usage = func(name string, dst *usageSpec) func(...string) string {
			return func(tmplArgs ...string) string {
				usageStr := strings.TrimSpace(strings.Join(tmplArgs, " "))
				p := newUsageParser(usageStr)
				spec, err := p.parse()
				if err != nil {
					panic(err)
				}
				*dst = spec
				helpStr := name
				if len(usageStr) > 0 {
					helpStr += " " + usageStr
				}
				return fmt.Sprintf("Usage: {%s}", helpStr)
			}
		}

		run = func(args []string) (s string, err error) {
			if len(args) == 0 {
				panic("run: too few arguments")
			}
			bin, args := args[0], args[1:]
			cmd := exec.Command(bin, args...)
			bs, err := cmd.Output()
			if err != nil {
				err = fmt.Errorf("%s: %s", bin, err)
			} else {
				s = trimWhitespace(string(bs), wsBOF, wsEOF)
				s = strings.ReplaceAll(s, "\n", " ")
			}
			return
		}

		justifier = func(lr rune) placeholderFunc {
			return placeholderFunc(func(args []string) (string, error) {
				width, err := strconv.Atoi(args[0])
				if err != nil {
					return "", fmt.Errorf("width %s: not an integer", args[0])
				}
				pad, args := args[1], args[2:]
				var s string
				if len(args) == 0 {
					s, pad = pad, " "
				} else {
					s = strings.Join(args, " ")
				}
				return justify(lr, s, width, pad), nil
			})
		}
	)

	for _, proto := range []struct {
		name  string
		build func() (string, placeholder)
	}{
		{
			"name",
			func() (string, placeholder) {
				h := `
				Outputs the session name.

				{{usage}}

				By default, the session name is set to the basename of the
				underlying command. Otherwise, if {{flag .name}} is set, {{.self}} will
				output that value.
				`
				return h, nil
			},
		},
		{
			"text",
			func() (string, placeholder) {
				h := `
				Outputs the last line of text generated by the underlying command.

				{{usage}}

				The line has its trailing end-of-line character removed,
				allowing for it to be referenced multiple times, and at any
				position, in the same template string.
				`
				return h, nil
			},
		},
		{
			"path",
			func() (string, placeholder) {
				h := `
				Outputs the binary path of the underlying command.

				{{usage "[abs|rel]"}}

				{{arg "abs"}}: prints the absolute binary path
				{{arg "rel"}}: prints the relative binary path

				If no argument is provided, {{.self}} defaults to the basename
				of the binary path provided to {{.app}} at execution time.
				`
				return h, nil
			},
		},
		{
			"ts",
			func() (string, placeholder) {
				var h strings.Builder
				fmt.Fprint(&h, `
				Generates a timestamp.

				{{usage "[<format>]"}}

				Available formats:

				`)

				names := make([]string, 0, len(timestampFormats))
				for name := range timestampFormats {
					names = append(names, name)
				}
				sort.Strings(names)
				for _, name := range names {
					fmt.Fprintf(&h, " {{val \"%-14s\"}} %s\n", name, timestampFormats[name])
				}

				fmt.Fprintf(&h, `
				If {{arg "format"}} is not specified, and the environment variable {{val .timestamp}}
				is defined, then {{arg "format"}} takes after it; otherwise, it falls back to
				{{val "datetime"}}.

				If {{arg "format"}} is specified, but does not match any of the available formats,
				it is passed as is to Go's time formatter.

				See https://golang.org/pkg/time/#pkg-constants for more details.
				`)

				return h.String(), placeholderFunc(func(args []string) (string, error) {
					return timestamp(strings.Join(args, " ")), nil
				})
			},
		},
		{
			"delta",
			func() (string, placeholder) {
				h := `
				Outputs the time elapsed since an event.

				{{usage "[last|init|sys]"}}

				{{arg "last"}}: time elapsed since the last log event
				{{arg "init"}}: time elapsed since {{.app}} was initialized
				{{arg "sys"}}: time elapsed since the system was booted

				If no argument is provided, it defaults to {{val "last"}}.
				`
				return h, placeholderMaker(func([]string) placeholder {
					var (
						init = time.Now()
						last time.Time
					)
					return cyclicPlaceholder(func(args []string) (string, error) {
						defer func() { last = time.Now() }()

						var dur time.Duration
						switch arg := strings.Join(args, ""); strings.ToLower(arg) {
						case "":
							fallthrough
						case "last":
							if !last.IsZero() {
								dur = time.Since(last)
							}
						case "init":
							dur = time.Since(init)
						case "sys":
							up, err := uptime.Get()
							if err != nil {
								return "", err
							}
							dur = up
						default:
							return "", fmt.Errorf("invalid argument: %s", arg)
						}
						return ms(dur), nil
					})
				})
			},
		},
		{
			"user",
			func() (string, placeholder) {
				h := `
				Prints the current username.

				{{usage}}
				`

				return h, placeholderFunc(func([]string) (string, error) {
					u, err := user.Current()
					if err != nil {
						return "", err
					}
					return u.Username, nil
				})
			},
		},
		{
			"host",
			func() (string, placeholder) {
				h := `
				Prints the current hostname.

				{{usage}}
				`

				return h, placeholderFunc(func([]string) (string, error) {
					return os.Hostname()
				})
			},
		},
		{
			"env",
			func() (string, placeholder) {
				h := `
				Outputs the value of an environment variable.

				{{usage "<variable>"}}

				If {{arg "variable"}} is not defined, {{.self}} outputs the
				empty string.
				`
				return h, placeholderFunc(func(args []string) (string, error) {
					v := args[0]
					if v[0] == '$' {
						v = v[1:]
					}
					return os.Getenv(v), nil
				})
			},
		},
		{
			"pid",
			func() (string, placeholder) {
				h := `
				Outputs the process ID of the underlying command.

				{{usage}}
				`
				return h, nil
			},
		},
		{
			"ppid",
			func() (string, placeholder) {
				h := `
				Outputs the process ID of {{.app}}.

				{{usage}}
				`
				return h, placeholderFunc(func([]string) (string, error) {
					return strconv.Itoa(os.Getpid()), nil
				})
			},
		},
		{
			"cmd",
			func() (string, placeholder) {
				h := `
				Runs a command and prints its standard output.

				{{usage "<command> [<arguments>...]"}}

				Argument parsing follows shell quoting rules.

				If the output contains multiple lines, they are joined with
				a space character.

				Note that executing a long-running process will slow down
				logging significantly.
				`

				return h, placeholderFunc(run)
			},
		},
		{
			"sh",
			func() (string, placeholder) {
				h := `
				Like {cmd}, but invokes the shell directly.

				{{usage "<command> [<arguments...>]"}}

				This is a shorthand for {cmd sh -c command [<arguments...>]}.

				{{also "cmd"}}
				`

				return h, placeholderFunc(func(args []string) (string, error) {
					args = append([]string{"sh", "-c"}, strings.Join(args, " "))
					return run(args)
				})
			},
		},
		{
			"rjust",
			func() (string, placeholder) {
				h := `
				Right-justifies text.

				{{usage "<width> [<padding>] <arguments...>"}}

				{{arg "padding"}} may be one or more characters. If not provided,
				it defaults to a space character.

				{{also "ljust"}}
				`
				return h, justifier('r')
			},
		},
		{
			"ljust",
			func() (string, placeholder) {
				h := `
				Left-justifies text.

				{{usage "<width> [<padding>] <arguments...>"}}

				{{also "rjust"}}
				`
				return h, justifier('l')
			},
		},
		{
			"upcase",
			func() (string, placeholder) {
				h := `
				Uppercases text.

				{{usage "<arguments...>"}}
				`
				return h, placeholderFunc(func(args []string) (string, error) {
					return strings.ToUpper(strings.Join(args, " ")), nil
				})
			},
		},
		{
			"downcase",
			func() (string, placeholder) {
				h := `
				Lowercases text.

				{{usage "<arguments...>"}}
				`
				return h, placeholderFunc(func(args []string) (string, error) {
					return strings.ToLower(strings.Join(args, " ")), nil
				})
			},
		},
		{
			"bg",
			func() (string, placeholder) {
				h := `
				Sets the background color of text.

				{{usage "<color> [<arguments...>]"}}
				`
				return h, placeholderFunc(func(args []string) (string, error) {
					id, args := args[0], args[1:]
					text := strings.Join(args, " ")
					return bg(id, text)
				})
			},
		},
		{
			"fg",
			func() (string, placeholder) {
				h := `
				Sets the foreground color of text.

				{{usage "<color> [<arguments...>]"}}
				`
				return h, placeholderFunc(func(args []string) (string, error) {
					id, args := args[0], args[1:]
					text := strings.Join(args, " ")
					return fg(id, text)
				})
			},
		},
		{
			"bold",
			func() (string, placeholder) {
				h := `
				Outputs text with a bold font style.

				{{usage "[<arguments...>]"}}
				`
				return h, placeholderFunc(func(args []string) (string, error) {
					return attr("bold", strings.Join(args, " "))
				})
			},
		},
		{
			"italic",
			func() (string, placeholder) {
				h := `
				Outputs text with an italic font style.

				{{usage "[<arguments...>]"}}
				`
				return h, placeholderFunc(func(args []string) (string, error) {
					return attr("italic", strings.Join(args, " "))
				})
			},
		},
		{
			"underline",
			func() (string, placeholder) {
				h := `
				Underlines text.

				{{usage "[<arguments...>]"}}
				`
				return h, placeholderFunc(func(args []string) (string, error) {
					return attr("underline", strings.Join(args, " "))
				})
			},
		},
		{
			"reverse",
			func() (string, placeholder) {
				h := `
				Swaps the active background:foreground colors.

				{{usage "[<arguments...>]"}}
				`
				return h, placeholderFunc(func(args []string) (string, error) {
					return attr("reverse", strings.Join(args, " "))
				})
			},
		},
		{
			"reset",
			func() (string, placeholder) {
				h := `
				Resets all attributes to their default value.

				{{usage}}
				`
				return h, placeholderFunc(func([]string) (string, error) {
					return codes["attrs"]["reset"].String(), nil
				})
			},
		},
	} {
		name := proto.name
		if _, ok := placeholderDefs[name]; ok {
			panic(fmt.Sprintf("placeholder %s already defined", name))
		}

		var (
			def = placeholderDef{}
			fns = gotemplate.FuncMap{
				"bold":   bold,
				"italic": italic,
				"usage":  usage(name, &def.usage),
				"arg":    bold,
				"val":    italic,
				"flag": func(arg string) (s string) {
					s = "-%s"
					if len(arg) > 1 {
						s = "-" + s
					}
					return fmt.Sprintf(italic(s), arg)
				},
				"also": func(args ...string) string {
					return fmt.Sprintf("See also: %s.", strings.Join(args, ", "))
				},
			}
			data = map[string]interface{}{
				"name":      name,
				"self":      fmt.Sprintf("{%s}", name),
				"app":       app,
				"timestamp": timestampEnvVar,
			}
		)

		h, p := proto.build()
		h = trimWhitespace(h, wsBOF, wsBOL)
		def.help = renderHelp(name, h, fns, data)
		def.fn = p
		placeholderDefs[name] = def
		placeholderNames = append(placeholderNames, name)
	}
}

func timestamp(fmt string) string {
	if fmt == "" {
		fmt = defaultTimestampFormat
	}
	if v, ok := timestampFormats[fmt]; ok {
		fmt = v
	}
	return time.Now().Format(fmt)
}

var timestampFormats = map[string]string{
	"rfc3339":     time.RFC3339,
	"time":        stampTime,
	"time ms":     stampTimeMs,
	"time us":     stampTimeUs,
	"date":        stampDate,
	"datetime":    stampDateTime,
	"datetime ms": stampDateTimeMs,
	"datetime us": stampDateTimeUs,
}

const (
	stampTime       = "15:04:05"
	stampTimeMs     = "15:04:05.000"
	stampTimeUs     = "15:04:05.000000"
	stampDate       = "2006/01/02"
	stampDateTime   = "2006/01/02 " + stampTime
	stampDateTimeMs = "2006/01/02 " + stampTimeMs
	stampDateTimeUs = "2006/01/02 " + stampTimeUs
)

// defaultTimestamp returns a timestamp value that's independent of the {ts}
// placeholder. Its format defaults to "datetime" unless the _TIMESTAMP
// environment variable is set.
func defaultTimestamp() string {
	return timestamp(defaultTimestampFormat)
}

var defaultTimestampFormat = func() (ts string) {
	if ts = os.Getenv(timestampEnvVar); ts == "" {
		ts = "datetime"
	}
	return
}()

var timestampEnvVar = fmt.Sprintf("%s_TIMESTAMP", strings.ToUpper(app))

func ms(dur time.Duration) string {
	return dur.Round(time.Millisecond).String()
}

var (
	bold   = codes["attrs"]["bold"].wrapper()
	italic = codes["attrs"]["italic"].wrapper()
)

var codes = ansiCodes{
	"fg": map[string]ansiCode{
		"black":         "30",
		"red":           "31",
		"green":         "32",
		"yellow":        "33",
		"blue":          "34",
		"magenta":       "35",
		"cyan":          "36",
		"gray":          "37",
		"grey":          "37",
		"light-gray":    "37",
		"light-grey":    "37",
		"default":       "39",
		"dark-gray":     "90",
		"dark-grey":     "90",
		"light-red":     "91",
		"light-green":   "92",
		"light-yellow":  "93",
		"light-blue":    "94",
		"light-magenta": "95",
		"light-cyan":    "96",
		"white":         "97",
	},
	"bg": map[string]ansiCode{
		"black":         "40",
		"red":           "41",
		"green":         "42",
		"yellow":        "43",
		"blue":          "44",
		"magenta":       "45",
		"cyan":          "46",
		"gray":          "47",
		"grey":          "47",
		"light-gray":    "47",
		"light-grey":    "47",
		"default":       "49",
		"dark-gray":     "100",
		"dark-grey":     "100",
		"light-red":     "101",
		"light-green":   "102",
		"light-yellow":  "103",
		"light-blue":    "104",
		"light-magenta": "105",
		"light-cyan":    "106",
		"white":         "107",
	},
	"attrs": map[string]ansiCode{
		"reset":     "0",
		"bold":      "1",
		"italic":    "3",
		"underline": "4",
		"reverse":   "7",
	},
}

type ansiCodes map[string]map[string]ansiCode

func (cs ansiCodes) keys(kind string) (res []string) {
	m, ok := cs[kind]
	if !ok {
		return
	}
	for k := range m {
		res = append(res, k)
	}
	sort.Strings(res)
	return
}

type ansiCode string

// wrap wraps s with ANSI code c on the left, and the reset ANSI code on the
// right.
//
// TODO: using generic [0m/[m resets does not work as intended when attributes
// are nested, e.g., {fg red "hello" {bold "world"} "this is reset by bold"}.
// However, attribute-specific resets are not always implemented by terminal
// emulators.
func (c ansiCode) wrap(s string) string {
	return fmt.Sprintf("%s%s\033[m", c, s)
}

func (c ansiCode) wrapper() func(string) string {
	return func(s string) string { return c.wrap(s) }
}

func (c ansiCode) String() string {
	return fmt.Sprintf("\033[%sm", string(c))
}

func newUsageParser(text string) *usageParser {
	return &usageParser{
		text: []rune(text),
	}
}

// usageParser parses strings of the form "<command> [arg1] <arg2> a|b x y z"
// into usage specs that can be used to validate arguments at runtime. Similar
// to docopt to an extent.
type usageParser struct {
	text []rune
	pos  int
}

func (p *usageParser) parse() (specs usageSpecs, err error) {
	defer func() {
		switch v := recover().(type) {
		case nil:
		case error:
			err = v
		default:
			panic(v)
		}
	}()
	for {
		var s usageSpec
		switch r := p.next(); r {
		case '[':
			s = p.parseOpt()
		case '<':
			s = p.parseReq()
		case ' ':
			continue
		case '.':
			p.prev()
			p.expectRun("parse", "...")
			p.expect("parse", len(specs) > 0, "no ellipsis")
			// Replace the last argument with the enum version of itself.
			n := len(specs) - 1
			specs[n] = specs[n].enum()
			continue
		case eof:
			return
		default:
			p.prev()
			s = p.parseWord()
		}
		specs = append(specs, s)
	}
}

func (p *usageParser) parseWord() usageSpec {
	var buf strings.Builder
	for {
		r := p.next()
		switch {
		case isArgChar(r):
			buf.WriteRune(r)
		case r == '|':
			return p.parseAlt(plainWord(buf.String()), ' ')
		default:
			p.expect("parseWord", buf.Len() > 0, "plain word length > 0")
			return plainWord(buf.String())
		}
	}
}

func (p *usageParser) parseReq() usageSpec {
	var buf strings.Builder
	for {
		r := p.next()
		switch {
		case r == '>':
			p.expect("parseReq", buf.Len() > 0, "arg word length > 0")
			return &usageReq{argWord(buf.String())}
		case r == '|':
			alt := p.parseAlt(argWord(buf.String()), '>')
			return &usageReq{alt}
		case r == '.':
			p.prev()
			p.expectRun("parseReq", "...>")
			p.expect("parseReq", buf.Len() > 0, "arg word length > 0")
			return &usageReq{&usageEnum{argWord(buf.String())}}
		case isArgChar(r):
			buf.WriteRune(r)
		default:
			p.err("parseReq", "unexpected char: %q", r)
		}
	}
}

func (p *usageParser) parseOpt() usageSpec {
	var (
		buf strings.Builder
		s   usageSpec
	)
	for {
		r := p.next()
		switch {
		case r == '<':
			p.expect("parseOpt", buf.Len() == 0, "arguments can't have prefixes") // [prefix<arg>]
			s = p.parseReq()
		case r == ']':
			if s == nil {
				p.expect("parseOpt", buf.Len() > 0, "word length > 0") // [word]
				s = plainWord(buf.String())
			}
			return &usageOpt{s}
		case r == '|':
			alt := p.parseAlt(plainWord(buf.String()), ']')
			return &usageOpt{alt}
		case r == '.':
			p.prev()
			p.expectRun("parseOpt", "...]")
			if s == nil {
				p.expect("parseOpt", buf.Len() > 0, "word length > 0") // [word...]
				s = plainWord(buf.String())
			}
			return &usageOpt{&usageEnum{s}}
		case isArgChar(r):
			buf.WriteRune(r)
		default:
			p.err("parseOpt", "unexpected char: %q", r)
		}
	}
}

func (p *usageParser) parseAlt(first *usageWord, end rune) usageSpec {
	p.expect("parseAlt", len(first.val) != 0, "non-empty first word")
	var (
		alt = usageAlt{first}
		buf strings.Builder
	)
	for {
		r := p.next()
		switch {
		case r == '|':
			p.expect("parseAlt", buf.Len() > 0, "%d: buf.Len() > 0", len(alt))
			alt = append(alt, first.convert(buf.String()))
			buf.Reset()
		case isArgChar(r):
			buf.WriteRune(r)
		case r == end, r == eof, r == '.':
			if buf.Len() > 0 {
				alt = append(alt, first.convert(buf.String()))
			}
			p.expect("parseAlt", len(alt) >= 2, "len(alt): %d >= 2", len(alt))
			if r == '.' {
				p.prev()
				p.expectRun("parseAlt", "...")
				p.expectAny("parseAlt", end, eof)
				return &usageEnum{alt}
			}
			return alt
		default:
			p.err("parseAlt", "unexpected char: %q", r)
		}
	}
}

func (p *usageParser) expect(name string, cond bool, msgf string, args ...interface{}) {
	if !cond {
		p.err(name, "expected %s", fmt.Sprintf(msgf, args...))
	}
}

func (p *usageParser) expectRun(name string, run string) {
	for _, n := range run {
		if p.next() != n {
			p.err(name, "expected run %q", run)
		}
	}
}

func (p *usageParser) expectAny(name string, any ...rune) {
	r := p.next()
	for _, n := range any {
		if r == n {
			return
		}
	}
	p.err(name, "expected one of %q", any)
}

func (p *usageParser) err(name, msgf string, args ...interface{}) {
	panic(fmt.Errorf("%s:%d: %s", name, p.pos, fmt.Sprintf(msgf, args...)))
}

func (p *usageParser) next() rune {
	if p.pos == len(p.text) {
		return eof
	}
	r := p.text[p.pos]
	p.pos++
	return r
}

func (p *usageParser) prev() rune {
	if p.pos == 0 {
		return eof
	}
	p.pos--
	return p.text[p.pos]
}

func isArgChar(r rune) bool {
	return r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

var (
	errNotSpecified = errors.New("not specified")
	errNotMatched   = errors.New("not matched")
)

type usageError struct {
	spec  string
	arg   string
	cause error
}

func (e *usageError) Error() string {
	var s string
	switch e.cause {
	case errNotSpecified:
		s = fmt.Sprintf("%s %s", e.spec, e.cause)
	case errNotMatched:
		s = fmt.Sprintf("%q not in %s", e.arg, e.spec)
	default:
		panic(fmt.Sprintf("unexpected error: %s", e.cause))
	}
	return s
}

type usageErrors []*usageError

func (es usageErrors) Error() string {
	switch len(es) {
	case 0:
		return ""
	case 1:
		return es[0].Error()
	case 2:
		return fmt.Sprintf("%s, and 1 more error", es[0])
	default:
		return fmt.Sprintf("%s, and %d more errors", es[0], len(es)-1)
	}
}

type usageSpecs []usageSpec

func (specs usageSpecs) check(args []string) ([]string, error) {
	var errs usageErrors
	for i, spec := range specs {
		var (
			err    error
			prev   = args
			_, opt = spec.(*usageOpt)
			last   = i == len(specs)-1
		)
		args, err = spec.check(args)
		switch {
		case last && opt && len(args) == 0:
			err = nil
		case last && opt:
		case opt:
			err = nil
			args = prev
		}
		if err != nil {
			err := &usageError{
				spec:  spec.String(),
				cause: err,
			}
			if len(args) > 0 {
				err.arg = args[0]
			}
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 { // avoid nil-wrapping interface
		return args, nil
	}
	return args, errs
}

func (specs usageSpecs) enum() usageSpec { return specs }

func (specs usageSpecs) String() string {
	var sb strings.Builder
	for i, spec := range specs {
		if i > 0 {
			sb.WriteRune(' ')
		}
		sb.WriteString(spec.String())
	}
	return sb.String()
}

type usageSpec interface {
	// check checks if the spec matches args and returns an error if it
	// doesn't; otherwise, it returns a slice with the matching arguments
	// removed.
	check(args []string) ([]string, error)

	// enum turns a spec into an enum, be that by wrapping itself in a *usageEnum
	// or its contents.
	enum() usageSpec

	fmt.Stringer
}

type (
	usageWord struct {
		typ wordType
		val string
		fn  func([]string) ([]string, error)
	} // any word, be it plain or an argument (i.e., <arg>)
	usageOpt  struct{ usageSpec } // [a] or [<a>]
	usageReq  struct{ usageSpec } // <a>
	usageAlt  []usageSpec         // a|b or <a|b> or [a|b]
	usageEnum struct{ usageSpec } // any other plus ...
)

func plainWord(word string) *usageWord {
	return &usageWord{
		typ: wordPlain,
		val: word,
		fn: func(args []string) ([]string, error) {
			if len(args) == 0 {
				return args, errNotSpecified
			}
			exp, got := word, args[0]
			if exp != got {
				if got == "" {
					return args, errNotSpecified
				}
				return args, errNotMatched
			}
			return args[1:], nil
		},
	}
}

func argWord(word string) *usageWord {
	return &usageWord{
		typ: wordArg,
		val: word,
		fn: func(args []string) ([]string, error) {
			if len(args) == 0 {
				return args, errNotSpecified
			}
			return args[1:], nil
		},
	}
}

type wordType int

const (
	wordPlain wordType = iota
	wordArg
)

func (a *usageWord) convert(s string) *usageWord {
	switch a.typ {
	case wordPlain:
		return plainWord(s)
	case wordArg:
		return argWord(s)
	default:
		panic(fmt.Sprintf("unexpected word type: %d", a.typ))
	}
}

func (u *usageWord) check(args []string) ([]string, error) { return u.fn(args) }

func (u *usageOpt) check(args []string) ([]string, error) {
	new, err := u.usageSpec.check(args)
	if err != nil {
		return args, err
	}
	return new, nil
}

func (u *usageReq) check(args []string) ([]string, error) {
	if len(args) == 0 {
		return args, errNotSpecified
	}
	new, err := u.usageSpec.check(args)
	if err != nil {
		return args, err
	}
	return new, nil
}

func (u usageAlt) check(args []string) (_ []string, err error) {
	if len(args) == 0 {
		return args, errNotSpecified
	}
	for _, u := range u {
		var new []string
		if new, err = u.check(args); err == nil {
			return new, nil
		}
	}
	return args, err
}

func (u *usageEnum) check(args []string) (_ []string, err error) {
	if len(args) == 0 {
		return args, errNotSpecified
	}
	for len(args) > 0 {
		args, err = u.usageSpec.check(args)
		if err != nil {
			return args, err
		}
	}
	return args, nil
}

func (u *usageWord) enum() usageSpec { return &usageEnum{u} }

func (u *usageOpt) enum() usageSpec {
	if _, ok := u.usageSpec.(*usageEnum); !ok {
		return &usageOpt{&usageEnum{u.usageSpec}}
	}
	return u
}

func (u *usageReq) enum() usageSpec {
	if _, ok := u.usageSpec.(*usageEnum); !ok {
		return &usageReq{&usageEnum{u.usageSpec}}
	}
	return u
}

func (u *usageEnum) enum() usageSpec { return u }

func (u usageAlt) enum() usageSpec { return &usageEnum{u} }

func (u *usageWord) String() string { return u.val }
func (u *usageOpt) String() string  { return fmt.Sprintf("[%s]", u.usageSpec) }
func (u *usageReq) String() string  { return fmt.Sprintf("<%s>", u.usageSpec) }
func (u *usageEnum) String() string { return fmt.Sprintf("%s...", u.usageSpec) }
func (u usageAlt) String() string {
	var buf strings.Builder
	for i, w := range u {
		if i > 0 {
			buf.WriteRune('|')
		}
		fmt.Fprint(&buf, w)
	}
	return buf.String()
}
