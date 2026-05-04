package verifycode

import (
	"strings"
	"testing"
)

func TestGenerate_ProducesLengthFromCharset(t *testing.T) {
	t.Parallel()
	for i := 0; i < 200; i++ {
		c := Generate()
		if len(c) != Length {
			t.Fatalf("Generate() = %q (len %d), want length %d", c, len(c), Length)
		}
		for j := 0; j < len(c); j++ {
			if !strings.ContainsRune(Charset, rune(c[j])) {
				t.Fatalf("Generate() = %q contains illegal char %q at %d", c, c[j], j)
			}
		}
	}
}

func TestGenerate_NoAmbiguousChars(t *testing.T) {
	t.Parallel()
	// The whole point of the restricted charset is to exclude these.
	forbidden := "01IOLiol"
	for i := 0; i < 1000; i++ {
		c := Generate()
		if strings.ContainsAny(c, forbidden) {
			t.Fatalf("Generate() = %q contains forbidden char from %q", c, forbidden)
		}
	}
}

func TestGenerate_DistributionLooksReasonable(t *testing.T) {
	t.Parallel()
	// Sanity-check that we're not stuck on one char (not an exhaustive
	// statistical test — just guards against a bug that hardcodes index 0).
	const samples = 5000
	counts := make(map[byte]int, len(Charset))
	for i := 0; i < samples; i++ {
		for _, b := range []byte(Generate()) {
			counts[b]++
		}
	}
	if len(counts) < len(Charset)/2 {
		t.Fatalf("only %d distinct chars across %d samples; charset has %d", len(counts), samples, len(Charset))
	}
}

func TestNormalize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want string
	}{
		{"7XC-8DZ", "7XC8DZ"},
		{"7XC8DZ", "7XC8DZ"},
		{" 7XC-8DZ ", "7XC8DZ"},
		{"7xc-8dz", "7XC8DZ"},
		{"7Xc-8Dz", "7XC8DZ"},
		{"7XC--8DZ", "7XC8DZ"},
		{"\t7XC\t8DZ\t", "7XC8DZ"},

		// Bad lengths.
		{"7XC8D", ""},
		{"7XC8DZA", ""},
		{"", ""},

		// Forbidden chars.
		{"7XC8D0", ""}, // 0 not in charset
		{"7XC8DI", ""}, // I excluded
		{"7XC8DO", ""}, // O excluded
		{"7XC8DL", ""}, // L excluded
		{"1XC8DZ", ""}, // 1 excluded
	}
	for _, tt := range tests {
		got := Normalize(tt.in)
		if got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFormat(t *testing.T) {
	t.Parallel()
	if got := Format("7XC8DZ"); got != "7XC-8DZ" {
		t.Errorf("Format(\"7XC8DZ\") = %q, want %q", got, "7XC-8DZ")
	}
	// Pass-through for unexpected lengths so callers aren't blindsided.
	if got := Format("ABC"); got != "ABC" {
		t.Errorf("Format(\"ABC\") = %q, want pass-through %q", got, "ABC")
	}
}

func TestNormalize_FormatRoundTrip(t *testing.T) {
	t.Parallel()
	for i := 0; i < 50; i++ {
		raw := Generate()
		display := Format(raw)
		if got := Normalize(display); got != raw {
			t.Fatalf("Normalize(Format(%q)) = %q, want %q", raw, got, raw)
		}
	}
}
