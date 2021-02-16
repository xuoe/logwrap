package main

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
)

func TestLineWriter(t *testing.T) {
	for i, tc := range [][]string{
		{
			"abc", "",
			"x \ny", "abcx \n",
			"\n", "abcx \ny\n",
		},
		{
			"\n", "\n",
			"\n\n", "\n\n\n",
		},
		{
			"", "",
			"\n", "\n",
			"", "\n",
		},
		{
			"", "",
			"\n", "\n",
			"a b c", "\n",
			"\n\n", "\na b c\n\n",
		},
	} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			var (
				n   int
				buf bytes.Buffer
			)
			w := newLinewiseWriter(&buf)
			for i := 0; i < len(tc)-1; i += 2 {
				n++
				in, out := tc[i], tc[i+1]
				fmt.Fprint(w, in)
				if exp, got := out, buf.String(); exp != got {
					t.Errorf("\n%d:\n -%q\n +%q", n, exp, got)
				}
			}
		})
	}
}

func TestTemplateWriter(t *testing.T) {
	type test struct {
		name   string
		format string
		io     []string
	}
	T := func(name, format string, io ...string) test {
		return test{
			name:   name,
			format: format,
			io:     io,
		}
	}
	for _, test := range []test{
		T("basic", "{text}",
			"test", "",
			"\n", "test\n",
			"extra", "test\n",
			"", "test\nextra\n",
		),
		T("append eol on close", "{text}",
			"test", "",
			"", "test\n",
		),
		T("text sequence", "{text}",
			"test", "",
			"TEST", "",
			"\n", "testTEST\n",
		),
		T("eol sequence", "{text}",
			"test", "",
			"\n", "test\n",
			"\n", "test\n\n",
			"\n", "test\n\n\n",
			"", "test\n\n\n",
		),
	} {
		t.Run(test.name, func(t *testing.T) {
			tmpl, err := newTemplate(test.name, test.format, defaultPlaceholders())
			if err != nil {
				t.Fatal(err)
			}

			buf := new(bytes.Buffer)
			lw := &linewiseWriter{
				Writer: &templateWriter{
					Writer:   buf,
					template: tmpl,
				},
			}
			for i := 0; i < len(test.io)-1; i += 2 {
				in, out := []byte(test.io[i]), test.io[i+1]
				if len(in) == 0 {
					if err := lw.Close(); err != nil {
						t.Fatal(err)
					}
				} else {
					n, err := lw.Write(in)
					if err != nil {
						t.Fatal(err)
					}
					if exp, got := len(in), n; exp != got {
						t.Fatalf("n = -%d +%d", exp, got)
					}
				}
				if exp, got := out, buf.String(); exp != got {
					t.Errorf("\n %q => -%q +%q", in, exp, got)
				}
			}
		})
	}
}

func TestQuoteEscaper(t *testing.T) {
	for _, tc := range []struct {
		in, out string
	}{
		{
			``,
			``,
		},
		{
			`abc`,
			`abc`,
		},
		{
			`a'`,
			`a\'`,
		},
		{
			`a''b`,
			`a\'\'b`,
		},
		{
			`a''\b`,
			`a\'\'\b`,
		},
		{
			`\\\`,
			`\\\`,
		},
		{
			`\\'\`,
			`\\'\`,
		},
	} {
		var sb strings.Builder
		w := &quoteEscaper{Writer: &sb}
		if _, err := io.WriteString(w, tc.in); err != nil {
			t.Fatal(err)
		}
		if exp, got := tc.out, sb.String(); exp != got {
			t.Errorf("\ninput: %q\n -%q\n +%q", tc.in, exp, got)
		}
	}
}

func TestFileRotator(t *testing.T) {
	t.Skipf("tested elsewhere")
}
