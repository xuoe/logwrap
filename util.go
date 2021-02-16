package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

func isPipe(f *os.File) bool {
	stat, err := f.Stat()
	return err == nil && stat.Mode()&os.ModeCharDevice == 0
}

func countDigits(num int) (n int) {
	if num == 0 {
		num = 1
	} else if num < 0 {
		num = -num
	}
	for num > 0 {
		num /= 10
		n++
	}
	return
}

func abspath(p string) (abs string) {
	switch {
	case filepath.IsAbs(p):
		abs = p
	case strings.ContainsRune(p, filepath.Separator):
		abs, _ = filepath.Abs(p)
	default:
		abs, _ = exec.LookPath(p)
	}
	if abs == "" {
		abs = p
	}
	return
}

// parseSize parses byte sizes of the form: \d+\s*(?i:b|kb|mb|gb)
func parseSize(val string) (size int64, _ error) {
	var unit string
	idx := strings.IndexFunc(val, unicode.IsLetter)
	if idx == -1 {
		return 0, errors.New("invalid size format")
	}

	size, err := strconv.ParseInt(strings.TrimSpace(val[:idx]), 10, 0)
	if err != nil {
		return 0, errors.New("number part must be an integer")
	}
	unit = strings.ToLower(strings.TrimSpace(val[idx:]))
	switch unit {
	case "b":
	case "k", "kb":
		size *= 1e3
	case "m", "mb":
		size *= 1e6
	case "g", "gb":
		size *= 1e9
	default:
		return 0, fmt.Errorf("invalid size unit: %q", unit)
	}
	return size, nil
}

var byteSizes = []string{"b", "kb", "mb", "gb", "tb", "pb", "eb"}

// humanBytes is adapted from github.com/dustin/go-humanize.
func humanBytes(s uint64) string {
	const base = 1000
	if s < 10 {
		return fmt.Sprintf("%db", s)
	}
	e := math.Floor(math.Log(float64(s)) / math.Log(base))
	suffix := byteSizes[int(e)]
	val := math.Floor(float64(s)/math.Pow(base, e)*10+0.5) / 10
	f := "%.0f%s"
	if val < 10 {
		f = "%.1f%s"
	}
	return fmt.Sprintf(f, val, suffix)
}

func drawBox(s string) string {
	var sb strings.Builder
	sep := strings.Repeat("─", len(s)+2)
	fmt.Fprint(&sb, "┌", sep, "┐", "\n")
	fmt.Fprintln(&sb, "│", s, "│")
	fmt.Fprint(&sb, "└", sep, "┘")
	return sb.String()
}

const (
	wsBOF whitespace = iota
	wsBOL
	wsEOL
	wsEOF
)

type whitespace uint

var (
	reLeadingWhitespace  = regexp.MustCompile(`(?m:^(?: |\t)+)`)
	reTrailingWhitespace = regexp.MustCompile(`(?m:(?: |\t)+$)`)
)

func trimWhitespace(s string, ws ...whitespace) string {
	var bof, bol, eol, eof bool
	for _, ws := range ws {
		switch {
		case !bof && ws == wsBOF:
			bof = true
			s = strings.TrimLeft(s, "\n\t")
		case !bol && ws == wsBOL:
			bol = true
			s = reLeadingWhitespace.ReplaceAllLiteralString(s, "")
		case !eol && ws == wsEOL:
			eol = true
			s = reTrailingWhitespace.ReplaceAllLiteralString(s, "")
		case !eof && ws == wsEOF:
			eof = true
			s = strings.TrimRight(s, "\n\t")
		}
	}
	return s
}

// justify justifies text left or right to match the given width.
func justify(lr rune, text string, width int, padding string) string {
	// Set some default sane values.
	if padding == "" {
		padding = " "
	}
	switch lr {
	case 'l', 'r':
	default:
		lr = 'r'
	}
	width -= len(text) // account for the text itself
	var affix strings.Builder

	// Keep writing padding chars until we run out of width.
	for width > 0 {
		for _, r := range padding {
			if width == 0 {
				break
			}
			affix.WriteRune(r)
			width--
		}
	}

	// Pad if need be.
	if affix.Len() > 0 {
		switch lr {
		case 'l':
			text += affix.String()
		case 'r':
			text = affix.String() + text
		}
	}

	return text
}

type strs []string

func (s strs) filter(fn func(string) bool) (res []string) {
	for _, s := range s {
		if fn(s) {
			res = append(res, s)
		}
	}
	return
}

func (s strs) transform(fn func(string) string) (res []string) {
	for _, s := range s {
		res = append(res, fn(s))
	}
	return
}

func uniq(xs []string) (res []string) {
	seen := make(map[string]struct{}, len(xs))
	return strs(xs).filter(func(s string) bool {
		if _, ok := seen[s]; ok {
			return false
		}
		seen[s] = struct{}{}
		return true
	})
}

func exclude(xs []string, patterns ...string) []string {
	return strs(xs).filter(func(s string) bool {
		for _, p := range patterns {
			ok, err := filepath.Match(p, s)
			if err != nil || ok {
				return false
			}
		}
		return true
	})
}

func reduceInt(init int, xs []string, fn func(int, string) int) (res int) {
	res = init
	for _, x := range xs {
		res = fn(res, x)
	}
	return
}
