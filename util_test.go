package main

import "testing"

func TestCountDigits(t *testing.T) {
	for _, tc := range []struct {
		val int
		exp int
	}{
		{123, 3},
		{1, 1},
		{13, 2},
		{0, 1},
		{1e9, 10},
		{-100, 3},
		{-1, 1},
	} {
		if exp, got := tc.exp, countDigits(tc.val); exp != got {
			t.Errorf("\ncountDigits(%d): -%d +%d", tc.val, exp, got)
		}
	}
}

func TestParseSize(t *testing.T) {
	for _, tc := range []struct {
		in  string
		out int64
	}{
		{"100b", 100},
		{"1B", 1},
		{"101 B", 101},
		{"1 b", 1},
		{"1 kb", 1000},
		{"124Kb", 124 * 1000},
	} {
		exp := tc.out
		got, err := parseSize(tc.in)
		if err != nil {
			t.Error(err)
			continue
		}
		if exp != got {
			t.Errorf("\nparseSize(%q) => -%d +%d", tc.in, exp, got)
		}
	}
}

func TestTrimWhitespace(t *testing.T) {
	for _, tc := range []struct {
		in  string
		out string
		ws  []whitespace
	}{
		{
			" a b c ",
			" a b c ",
			[]whitespace{wsBOF},
		},
		{
			" a b c ",
			"a b c ",
			[]whitespace{wsBOL},
		},
		{
			" a b c ",
			"a b c",
			[]whitespace{wsBOL, wsEOL},
		},
		{
			" a b c ",
			"a b c ",
			[]whitespace{wsBOL, wsEOF},
		},
		{
			" a b c \n",
			"a b c ",
			[]whitespace{wsBOL, wsEOF},
		},
		{
			"\n\n\n\na\nb\nc",
			"a\nb\nc",
			[]whitespace{wsBOF},
		},
		{
			"\n\n\n\n\ta\n  b\n\tc",
			"a\nb\nc",
			[]whitespace{wsBOF, wsBOL},
		},
		{
			`
			a

			b

			c
			`,
			"a\n\nb\n\nc",
			[]whitespace{wsBOF, wsBOL, wsEOF},
		},
	} {
		if exp, got := tc.out, trimWhitespace(tc.in, tc.ws...); exp != got {
			t.Errorf("\n%q - %v:\n -%q\n +%q", tc.in, tc.ws, exp, got)
		}
	}
}

func TestJustify(t *testing.T) {
	for _, tc := range []struct {
		lr    rune
		in    string
		width int
		pad   string
		out   string
	}{
		{
			lr:    'r',
			in:    "abc",
			width: 5,
			out:   "  abc",
		},
		{
			lr:    'l',
			in:    "abc",
			width: 5,
			out:   "abc  ",
		},
		{
			lr:    'l',
			in:    "abc",
			pad:   "XYZ",
			width: 5,
			out:   "abcXY",
		},
		{
			lr:    'l',
			in:    "abc",
			pad:   "XYZ",
			width: 6,
			out:   "abcXYZ",
		},
	} {
		exp, got := tc.out, justify(tc.lr, tc.in, tc.width, tc.pad)
		if exp != got {
			t.Errorf("\njustify(%c, %q, %d, %q): -%q +%q", tc.lr, tc.in, tc.width, tc.pad, exp, got)
		}
	}
}
