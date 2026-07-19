package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// probeHub carries the same origin pin as every other hub-directed client, so a
// hub-side (or MITM'd) off-origin 3xx on the reachability probe is refused rather
// than followed by this CORS-free desktop process to a loopback service or cloud
// metadata. Without the CheckRedirect pin the default policy would follow it.
func TestProbeHubRefusesOffOriginRedirect(t *testing.T) {
	var reachedTarget bool
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reachedTarget = true
		_, _ = w.Write([]byte("secret"))
	}))
	t.Cleanup(internal.Close)

	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL+"/latest/meta-data/", http.StatusFound)
	}))
	t.Cleanup(hub.Close)

	err := probeHub(context.Background(), hub.URL)
	require.Error(t, err, "a probe redirect leaving the hub origin must not be followed")
	assert.False(t, reachedTarget, "the off-origin target must never be reached")
}

// A same-origin hub redirect is still followed, so the pin does not break a
// legitimate hub 3xx on the probe endpoint (an auth bounce, a trailing-slash 301).
func TestProbeHubFollowsSameOriginRedirect(t *testing.T) {
	var finalHit bool
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/leapmux.v1.AuthService/GetSystemInfo":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			finalHit = true
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(hub.Close)

	err := probeHub(context.Background(), hub.URL)
	require.NoError(t, err, "a same-origin probe redirect must be followed")
	assert.True(t, finalHit, "the same-origin target must be reached")
}

// The happy path: a reachable hub that answers 200 makes the probe succeed.
func TestProbeHubSucceedsOnOK(t *testing.T) {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(hub.Close)

	require.NoError(t, probeHub(context.Background(), hub.URL))
}

// A non-200 status is surfaced as an error so ConnectDistributed does not treat an
// unreachable or erroring hub as reachable.
func TestProbeHubFailsOnNon200(t *testing.T) {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(hub.Close)

	err := probeHub(context.Background(), hub.URL)
	require.Error(t, err, "a non-200 probe response must be an error")
	assert.Contains(t, err.Error(), "unexpected status")
}
