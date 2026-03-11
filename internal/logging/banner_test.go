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

func TestIsLoopbackAddr(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:4327", true},
		{"localhost:4327", true},
		{"[::1]:4327", true},
		{"0.0.0.0:4327", false},
		{":4327", false},
		{"192.168.1.1:4327", false},
		{"example.com:4327", false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			if got := isLoopbackAddr(tt.addr); got != tt.want {
				t.Errorf("isLoopbackAddr(%q) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

func TestIsLoopbackURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"http://127.0.0.1:4327", true},
		{"http://localhost:4327", true},
		{"http://[::1]:4327", true},
		{"http://localhost", true},
		{"http://0.0.0.0:4327", false},
		{"http://192.168.1.1:4327", false},
		{"http://example.com:4327", false},
		{"not-a-url", false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			if got := isLoopbackURL(tt.url); got != tt.want {
				t.Errorf("isLoopbackURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestIsLoopbackHost(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"localhost", true},
		{"0.0.0.0", false},
		{"192.168.1.1", false},
		{"example.com", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(host(tt.host), func(t *testing.T) {
			if got := isLoopbackHost(tt.host); got != tt.want {
				t.Errorf("isLoopbackHost(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

// host returns a non-empty test name for empty strings.
func host(h string) string {
	if h == "" {
		return "(empty)"
	}
	return h
}
