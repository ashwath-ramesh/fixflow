package cost

import (
	"testing"
)

func TestCalculate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		in, out  int
		wantMin  float64
		wantMax  float64
	}{
		{
			name:     "claude zero tokens",
			provider: "claude",
			in:       0, out: 0,
			wantMin: 0, wantMax: 0,
		},
		{
			name:     "claude 1M input 1M output",
			provider: "claude",
			in:       1_000_000, out: 1_000_000,
			wantMin: 18.0, wantMax: 18.0, // $3 + $15
		},
		{
			name:     "codex 1M input 1M output",
			provider: "codex",
			in:       1_000_000, out: 1_000_000,
			wantMin: 15.0, wantMax: 15.0, // $3 + $12
		},
		{
			name:     "unknown provider",
			provider: "gpt5",
			in:       1_000_000, out: 1_000_000,
			wantMin: 0, wantMax: 0,
		},
		{
			name:     "claude small tokens",
			provider: "claude",
			in:       45230, out: 12890,
			wantMin: 0.32, wantMax: 0.34,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Calculate(tc.provider, tc.in, tc.out)
			if got < tc.wantMin || got > tc.wantMax {
				t.Errorf("Calculate(%q, %d, %d) = %f, want [%f, %f]",
					tc.provider, tc.in, tc.out, got, tc.wantMin, tc.wantMax)
			}
		})
	}
}

func TestFormatUSD(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input float64
		want  string
	}{
		{0, "$0.00"},
		{0.42, "$0.42"},
		{1.234, "$1.23"},
		{100.5, "$100.50"},
	}

	for _, tc := range tests {
		got := FormatUSD(tc.input)
		if got != tc.want {
			t.Errorf("FormatUSD(%f) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestFormatRate(t *testing.T) {
	t.Parallel()

	got := FormatRate("claude")
	if got != "$3.00/$15.00 per 1M tokens" {
		t.Errorf("FormatRate(claude) = %q", got)
	}

	got = FormatRate("unknown")
	if got != "unknown pricing" {
		t.Errorf("FormatRate(unknown) = %q", got)
	}
}
