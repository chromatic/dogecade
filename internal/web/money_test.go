package web

import "testing"

func TestFormatKoinuAsDoge(t *testing.T) {
	cases := []struct {
		koinu int64
		want  string
	}{
		{0, "0"},
		{1, "0.00000001"},
		{100_000_000, "1"},
		{150_000_000, "1.5"},
		{100_500_000, "1.005"},
		{1_000_000_000, "10"},
		{123_456_789, "1.23456789"},
	}
	for _, tc := range cases {
		if got := formatKoinuAsDoge(tc.koinu); got != tc.want {
			t.Errorf("formatKoinuAsDoge(%d) = %q, want %q", tc.koinu, got, tc.want)
		}
	}
}
