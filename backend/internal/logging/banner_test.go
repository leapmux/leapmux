package logging

import (
	"strings"
	"testing"
)

func TestAddrToURL(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{":4327", "http://localhost:4327"},
		{"0.0.0.0:4327", "http://localhost:4327"},
		{"127.0.0.1:4327", "http://localhost:4327"},
		{":80", "http://localhost"},
		{"example.com:443", "http://localhost:443"},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			if got := addrToURL(tt.addr); got != tt.want {
				t.Errorf("addrToURL(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

func TestFormatLocalTimestamp(t *testing.T) {
	t.Run("invalid input falls back to raw string", func(t *testing.T) {
		if got := formatLocalTimestamp("not-a-time"); got != "not-a-time" {
			t.Fatalf("formatLocalTimestamp() = %q, want raw input", got)
		}
	})

	t.Run("includes timezone suffix", func(t *testing.T) {
		got := formatLocalTimestamp("2026-04-14T10:20:30Z")
		if got == "" {
			t.Fatal("formatLocalTimestamp() returned empty string")
		}
		if !strings.Contains(got, "20:30") {
			t.Fatalf("formatLocalTimestamp() = %q, expected formatted time component", got)
		}
		fields := strings.Fields(got)
		if len(fields) < 5 {
			t.Fatalf("formatLocalTimestamp() = %q, expected timezone suffix", got)
		}
		last := fields[len(fields)-1]
		if last == "PM" || last == "AM" {
			t.Fatalf("formatLocalTimestamp() = %q, expected timezone after time", got)
		}
	})

	t.Run("empty input stays empty", func(t *testing.T) {
		if got := formatLocalTimestamp(""); got != "" {
			t.Fatalf("formatLocalTimestamp(\"\") = %q, want empty string", got)
		}
	})
}
