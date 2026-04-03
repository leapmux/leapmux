package logging

import "testing"

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
