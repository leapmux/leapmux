package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCookieHeader_SingleHeader(t *testing.T) {
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)

	baseURL := "http://localhost"
	u, err := url.Parse(baseURL)
	require.NoError(t, err)

	jar.SetCookies(u, []*http.Cookie{
		{Name: "session", Value: "abc123"},
		{Name: "csrf", Value: "xyz789"},
	})

	proxy := &HubProxy{client: &http.Client{Jar: jar}, baseURL: baseURL}
	h := proxy.cookieHeader()
	require.NotNil(t, h)

	// Must produce exactly one Cookie header, not multiple.
	values := h.Values("Cookie")
	assert.Len(t, values, 1, "should produce a single Cookie header")
	assert.Contains(t, values[0], "session=abc123")
	assert.Contains(t, values[0], "csrf=xyz789")
	assert.Contains(t, values[0], "; ", "cookies should be semicolon-separated")
}

func TestCookieHeader_NoCookies(t *testing.T) {
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)

	proxy := &HubProxy{client: &http.Client{Jar: jar}, baseURL: "http://localhost"}
	h := proxy.cookieHeader()
	assert.Nil(t, h)
}

func TestCookieHeader_InvalidURL(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	proxy := &HubProxy{client: &http.Client{Jar: jar}, baseURL: "://invalid"}
	h := proxy.cookieHeader()
	assert.Nil(t, h)
}
