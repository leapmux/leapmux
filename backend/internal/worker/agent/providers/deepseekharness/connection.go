package deepseekharness

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The native URL includes a fresh token from this process's stdout.
var nativeListenPattern = regexp.MustCompile(`^dsh web: (http://[^[:space:]]+)`)

// parseNativeAnnouncement validates the URL before any credential leaves the process.
func parseNativeAnnouncement(raw string) (*url.URL, string, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return nil, "", fmt.Errorf("DeepSeek Harness announced an invalid URL")
	}
	if u.Path != "/" || u.RawPath != "" || u.User != nil || u.Fragment != "" {
		return nil, "", fmt.Errorf("DeepSeek Harness announced an invalid root URL")
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(values) != 1 || len(values["token"]) != 1 {
		return nil, "", fmt.Errorf("DeepSeek Harness announced invalid token fields")
	}
	token := values.Get("token")
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != token {
		return nil, "", fmt.Errorf("DeepSeek Harness announced an invalid process token")
	}
	port, portErr := strconv.Atoi(u.Port())
	if portErr != nil || port < 1 || port > 65535 {
		return nil, "", fmt.Errorf("DeepSeek Harness announced an invalid port")
	}
	origin := *u
	origin.RawQuery = ""
	origin.Path = ""
	if _, err := providerkit.ParseLoopbackHTTPURL(origin.String()); err != nil {
		return nil, "", fmt.Errorf("DeepSeek Harness announced an invalid loopback endpoint")
	}
	// The launch explicitly binds this literal. It requires no DNS resolution.
	if origin.Hostname() != "127.0.0.1" {
		return nil, "", fmt.Errorf("DeepSeek Harness did not announce its requested loopback address")
	}
	return u, origin.String(), nil
}

// authenticateNative exchanges the process token without following its redirect.
func authenticateNative(ctx context.Context, raw string) (*providerkit.HTTPEndpoint, error) {
	announced, origin, err := parseNativeAnnouncement(raw)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, announced.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("prepare the DeepSeek Harness token exchange: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		var urlError *url.Error
		if errors.As(err, &urlError) {
			err = urlError.Err
		}
		return nil, fmt.Errorf("exchange the DeepSeek Harness process token: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "./" {
		return nil, fmt.Errorf("DeepSeek Harness refused its process token")
	}
	digest := sha256.Sum256([]byte(announced.Host))
	expected := "dsh-auth-" + base64.RawURLEncoding.EncodeToString(digest[:])
	cookies := response.Cookies()
	if len(cookies) != 1 || cookies[0].Name != expected || cookies[0].Value == "" || cookies[0].Domain != "" || cookies[0].Path != "/" || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].MaxAge <= 0 {
		return nil, fmt.Errorf("DeepSeek Harness returned an invalid private session cookie")
	}
	if strings.ContainsAny(cookies[0].Value, "\r\n;") {
		return nil, fmt.Errorf("DeepSeek Harness returned an invalid session credential")
	}
	endpoint, err := providerkit.NewHTTPEndpoint(origin, nil)
	if err != nil {
		return nil, err
	}
	return endpoint.WithHeader("Cookie", cookies[0].Name+"="+cookies[0].Value), nil
}
