package main

import (
	"bytes"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestTemplateParse(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			in   string
			out  string
		}{
			{
				"empty",
				"",
				"",
			},
			{
				"basic",
				"a {ts} c",
				"a {ts} c",
			},
			{
				"text",
				"{text}",
				"{text}",
			},
			{
				"multiple",
				"{pid} {pid} {text}{text}",
				"{pid} {pid} {text}{text}",
			},
			{
				"multiple spaced",
				"{ts   a b c} asdf {ts} ",
				`{ts "a" "b" "c"} asdf {ts} `,
			},
			{
				"multiple spaced",
				" {sh} {ts} ",
				" {sh} {ts} ",
			},
			{
				"nested",
				"{sh {sh echo hi}} you",
				`{sh {sh "echo" "hi"}} you`,
			},
			{
				"nested",
				"{sh {sh {sh } } test}",
				"{sh {sh {sh} } test}",
			},
			{
				"nested",
				"xyz{sh {sh}}abc",
				"xyz{sh {sh}}abc",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				tmpl, err := newTemplate("test", tc.in, defaultPlaceholders())
				if err != nil {
					t.Fatal(err)
				}
				if exp, got := tc.out, tmpl.String(); exp != got {
					t.Errorf("\n-%q\n+%q", exp, got)
				}
			})

		}
	})
	t.Run("invalid", func(t *testing.T) {
		type test struct {
			in  string
			out *templateError
		}
		T := func(in string, pos int, cause error) test {
			return test{
				in: in,
				out: &templateError{
					name:  "test",
					text:  in,
					pos:   pos,
					cause: cause,
				},
			}
		}
		for i, tc := range []test{
			T("{dunno}", 1, errNoSuchPlaceholder),
			T("{NOPE {", 1, errNoSuchPlaceholder),
			T("{{", 1, errMissingPlaceholderName),
			T("{", 1, errMissingPlaceholderName),
			T("{}", 0, errEmptyPlaceholder),
			T("{ {y", 1, errMissingPlaceholderName),
			T("}", 0, errUnexpectedClosingDelimiter),
			T("{text", 1, errUnterminatedPlaceholder),
			T("{text{", 5, errExpectedSpace),
			T("  {text{", 7, errExpectedSpace),
			T(" {text} { a", 10, errNoSuchPlaceholder),
			T(" {text} {text", 9, errUnterminatedPlaceholder),
		} {
			t.Run(strconv.Itoa(i), func(t *testing.T) {
				_, err := newTemplate("test", tc.in, defaultPlaceholders())
				if err == nil {
					t.Fatalf("\nexpected error in %q", tc.in)
				}
				if exp, got := tc.out, err; !reflect.DeepEqual(got, exp) {
					t.Errorf("\ntemplate: %q\nerror:\n -%q\n +%q", tc.in, exp, got)
				}
			})
		}
	})
}

func TestTemplateRender(t *testing.T) {
	newPs := func(ps placeholders) (res placeholders) {
		res = placeholders{
			"echo": placeholderFunc(func(args []string) (string, error) {
				return strings.Join(args, " "), nil
			}),
			"cyclic": placeholderMaker(func(args []string) placeholder {
				i, _ := strconv.Atoi(args[0])
				return cyclicPlaceholder(func([]string) (string, error) {
					defer func() { i++ }()
					return strconv.Itoa(i), nil
				})
			}),
			"sgl": placeholderFunc(func(args []string) (string, error) { return `'`, nil }),
			"dbl": placeholderFunc(func(args []string) (string, error) { return `"`, nil }),
		}
		for name, p := range ps {
			res[name] = p
		}
		return
	}
	for _, tc := range []struct {
		name string
		tmpl string
		io   []string
		placeholders
	}{
		{
			name: "use inbound newlines",
			tmpl: "a{text} {text}{text} b",
			io: []string{
				"test\n", "atest\n test\ntest\n b",
			},
		},
		{
			name: "no newline auto-insert",
			tmpl: "a{text} {text}{text} b",
			io: []string{
				"test", "atest testtest b",
			},
		},
		{
			name: "cyclic placeholder",
			tmpl: "a{text} {cyclic 20} {cyclic 20} {cyclic 20}",
			io: []string{
				"A", "aA 20 20 20",
				"B", "aA 20 20 20\naB 21 21 21",
				"C", "aA 20 20 20\naB 21 21 21\naC 22 22 22",
			},
		},
		{
			name: "placeholder outputs quotes",
			tmpl: `{echo x{echo A{sgl}C} {echo A{dbl}C}}`,
			io: []string{
				"\n", `xA'C A"C`,
			},
		},
		{
			name: "placeholder outputs quotes",
			tmpl: `{echo {echo {sgl}{dbl}{sgl}{dbl}}}`,
			io: []string{
				"\n", `'"'"`,
			},
		},
		{
			name: "placeholder outputs quotes",
			tmpl: `"{echo '{echo {echo {sgl}{dbl}{sgl}{dbl}}}'}"`,
			io: []string{
				"\n", `"'"'""`,
			},
		},
		{
			name: "placeholder treats arguments as shell words (discards quotes)",
			tmpl: `{echo ''""}`,
			io: []string{
				"\n", ``,
			},
		},
		{
			name: "placeholder treats arguments as shell words (keeps quoted whitespace)",
			tmpl: `{echo '  '}`,
			io: []string{
				"\n", `  `,
			},
		},
		{
			name: "placeholder treats arguments as shell words (keeps quoted whitespace)",
			tmpl: `{echo '{echo '  X  '}'}`,
			io: []string{
				"\n", `  X  `,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ps := tc.placeholders
			if ps == nil {
				ps = defaultPlaceholders()
			}
			ps = newPs(ps)
			tmpl, err := newTemplate("test", tc.tmpl, ps)
			if err != nil {
				t.Fatal(err)
			}

			var buf bytes.Buffer
			for i := 0; i < len(tc.io)-1; i += 2 {
				in, out := tc.io[i], tc.io[i+1]
				// Insert a newline to mimic how templates are used in the app.
				if i > 0 {
					fmt.Fprintln(&buf)
				}
				if _, err := tmpl.render(&buf, []byte(in)); err != nil {
					t.Fatal(err)
				}
				if exp, got := out, buf.String(); exp != got {
					t.Errorf("\ntemplate: %q\n -%s\n +%s", tc.tmpl, exp, got)
				}
			}
		})
	}
}

func TestUsageParser(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		for _, tc := range []struct {
			in  string
			out usageSpecs
		}{
			// Words
			{
				"a b c",
				usageSpecs{
					plainWord("a"),
					plainWord("b"),
					plainWord("c"),
				},
			},
			{
				"a bb ccc ",
				usageSpecs{
					plainWord("a"),
					plainWord("bb"),
					plainWord("ccc"),
				},
			},
			// Alts
			{
				"aa|bb",
				usageSpecs{
					usageAlt{
						plainWord("aa"),
						plainWord("bb"),
					},
				},
			},
			{
				"aa|bb|cc",
				usageSpecs{
					usageAlt{
						plainWord("aa"),
						plainWord("bb"),
						plainWord("cc"),
					},
				},
			},
			{
				"aa|bb|cc...",
				usageSpecs{
					&usageEnum{
						usageAlt{
							plainWord("aa"),
							plainWord("bb"),
							plainWord("cc"),
						},
					},
				},
			},
			// Opts
			{
				"[abc]",
				usageSpecs{
					&usageOpt{plainWord("abc")},
				},
			},
			{
				"[a|b|c]",
				usageSpecs{
					&usageOpt{
						usageAlt{
							plainWord("a"),
							plainWord("b"),
							plainWord("c"),
						},
					},
				},
			},
			{
				"[<a|b|c>]",
				usageSpecs{
					&usageOpt{
						&usageReq{
							usageAlt{
								argWord("a"),
								argWord("b"),
								argWord("c"),
							},
						},
					},
				},
			},
			{
				"[<abc>]",
				usageSpecs{
					&usageOpt{&usageReq{argWord("abc")}},
				},
			},
			// Reqs
			{
				"<arg> <ARG>",
				usageSpecs{
					&usageReq{argWord("arg")},
					&usageReq{argWord("ARG")},
				},
			},
			{
				"<arg>...",
				usageSpecs{
					&usageReq{&usageEnum{argWord("arg")}},
				},
			},
			{
				"<arg...>",
				usageSpecs{
					&usageReq{&usageEnum{argWord("arg")}},
				},
			},
			{
				"<arg...>...",
				usageSpecs{
					&usageReq{&usageEnum{argWord("arg")}},
				},
			},
			// All
			{
				"abc <XYZ>... [1|2]...",
				usageSpecs{
					plainWord("abc"),
					&usageReq{
						&usageEnum{argWord("XYZ")},
					},
					&usageOpt{
						&usageEnum{
							usageAlt{
								plainWord("1"),
								plainWord("2"),
							},
						},
					},
				},
			},
			{
				"<abc> [<xyz...>]",
				usageSpecs{
					&usageReq{
						argWord("abc"),
					},
					&usageOpt{
						&usageReq{&usageEnum{argWord("xyz")}},
					},
				},
			},
			{
				"[abc...] <xyz>",
				usageSpecs{
					&usageOpt{
						&usageEnum{plainWord("abc")},
					},
					&usageReq{argWord("xyz")},
				},
			},
			{
				"<width> [<padding>] <arguments>...",
				usageSpecs{
					&usageReq{argWord("width")},
					&usageOpt{&usageReq{argWord("padding")}},
					&usageReq{&usageEnum{argWord("arguments")}},
				},
			},
			{
				"<width> [<padding>] <arguments...>",
				usageSpecs{
					&usageReq{argWord("width")},
					&usageOpt{&usageReq{argWord("padding")}},
					&usageReq{&usageEnum{argWord("arguments")}},
				},
			},
		} {
			t.Run(tc.in, func(t *testing.T) {
				p := newUsageParser(tc.in)
				args, err := p.parse()
				if err != nil {
					t.Errorf("\n%q: %s", tc.in, err)
					return
				}
				exp, got := tc.out.String(), args.String()
				if exp != got {
					t.Errorf("\n%q\n -%q\n +%q", tc.in, exp, got)
				}
			})
		}
	})
}

func TestUsageArgsCheck(t *testing.T) {
	notSpecified := func(arg string) *usageError {
		return &usageError{
			spec:  arg,
			cause: errNotSpecified,
		}
	}
	notMatched := func(spec, arg string) *usageError {
		return &usageError{
			spec:  spec,
			arg:   arg,
			cause: errNotMatched,
		}
	}
	type test struct {
		in   string
		errs usageErrors
	}
	for _, tc := range []struct {
		spec   string
		checks []test
	}{
		{
			"<arg> <ARG>",
			[]test{
				{
					"a b",
					nil,
				},
				{
					"a",
					usageErrors{
						notSpecified("<ARG>"),
					},
				},
				{
					"a b c",
					nil,
				},
			},
		},
		{
			"<a> [<b>] <c...>",
			[]test{
				{
					"A B",
					nil,
				},
				{
					"A",
					usageErrors{
						notSpecified("<c...>"),
					},
				},
				{
					"A B C",
					nil,
				},
			},
		},
		{
			"<a> [<b>] [c] <d...>",
			[]test{
				{
					"A B",
					nil,
				},
				{
					"A",
					usageErrors{
						notSpecified("<d...>"),
					},
				},
				{
					"A B C",
					nil,
				},
			},
		},
		{
			"a|b|c",
			[]test{
				{"a", nil},
				{"b", nil},
				{"c", nil},
				{
					"",
					usageErrors{
						notSpecified("a|b|c"),
					},
				},
				{
					"d",
					usageErrors{
						notMatched("a|b|c", "d"),
					},
				},
			},
		},
		{
			"a|b|c...",
			[]test{
				{"a a a a a", nil},
				{"b b b ", nil}, // space at the end
				{"c c c", nil},
				{"a b c", nil},
				{
					"d",
					usageErrors{
						notMatched("a|b|c...", "d"),
					},
				},
			},
		},
		{
			"<a|b|c...>", // a, b, c are names, not words; pretty useless as a spec
			[]test{
				{"a a a a a", nil},
				{"b b b ", nil}, // space at the end
				{"c c c", nil},
				{"a b c", nil},
				{" x y z", nil},
			},
		},
		{
			"<a|b|c>...", // a, b, c are names, not words; pretty useless as a spec
			[]test{
				{"a a a a a", nil},
				{"b b b ", nil}, // space at the end
				{"c c c", nil},
				{"a b c", nil},
				{"d", nil}, // spec does not contain plain words, so this is fine
			},
		},
		{
			"[a|b|c]...", // this doesn't make much sense: an optional that
			[]test{
				{"a a a a a", nil},
				{"b b b ", nil}, // space at the end
				{"c c c", nil},
				{"a b c", nil},
				{
					"d",
					usageErrors{
						notMatched("[a|b|c...]", "d"),
					},
				},
			},
		},
		{
			"[a|b|c...]",
			[]test{
				{"a a a a a", nil},
				{"b b b ", nil}, // space at the end
				{"c c c", nil},
				{"a b c", nil},
				{
					"d",
					usageErrors{
						notMatched("[a|b|c...]", "d"),
					},
				},
			},
		},
		{
			"[a|b|c]",
			[]test{
				{"", nil},
			},
		},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			spec, err := newUsageParser(tc.spec).parse()
			if err != nil {
				t.Error(err)
				return
			}
			specStr := tc.spec
			for _, tc := range tc.checks {
				args := strings.Fields(tc.in)
				_, err := spec.check(args)
				errs, _ /*may be nil*/ := err.(usageErrors)
				if exp, got := tc.errs, errs; !reflect.DeepEqual(exp, got) {
					t.Errorf("\ninput: %q\nspec: %q\n\t -%q\n\t +%q", tc.in, specStr, exp, got)
				}
			}
		})
	}
}
