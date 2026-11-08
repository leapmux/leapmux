package logging

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
			assert.Equal(t, tt.want, addrToURL(tt.addr))
		})
	}
}

func TestAccessURL(t *testing.T) {
	tests := []struct {
		name, publicURL, listen, want string
	}{
		{"the public URL wins over the listen address", "https://leapmux.example.com", ":4327", "https://leapmux.example.com"},
		{"the public URL serves a hub with no TCP address", "https://leapmux.example.com", "", "https://leapmux.example.com"},
		{"a listen address derives a local URL", "", "127.0.0.1:4327", "http://localhost:4327"},
		{"a wildcard listen address derives a local URL", "", ":4327", "http://localhost:4327"},
		{"no public URL and no TCP address give no URL", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, AccessURL(tt.publicURL, tt.listen))
		})
	}
}
