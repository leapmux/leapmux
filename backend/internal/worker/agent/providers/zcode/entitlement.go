package zcode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

// zcodePlanBalanceTimeout bounds one entitlement fetch. The desktop uses 15s
// for the same call; a Start Plan agent launch must not block longer.
const zcodePlanBalanceTimeout = 15 * time.Second

// zcodeStartPlanEntitlement is one model_usage entitlement from the
// billing/balance response.
type zcodeStartPlanEntitlement struct {
	ShowName string `json:"show_name"`
	Meter    string `json:"meter"`
}

// zcodeStartPlanPlan is one plan entry from the billing/balance response.
type zcodeStartPlanPlan struct {
	Status       string                      `json:"status"`
	Entitlements []zcodeStartPlanEntitlement `json:"entitlements"`
}

// zcodeStartPlanBalanceData is the `data` object of the billing/balance response.
type zcodeStartPlanBalanceData struct {
	Plans []zcodeStartPlanPlan `json:"plans"`
}

// zcodeStartPlanBalanceResponse is the full billing/balance response envelope.
type zcodeStartPlanBalanceResponse struct {
	Code int                        `json:"code"`
	Data *zcodeStartPlanBalanceData `json:"data"`
}

// fetchStartPlanEntitlementModels calls ZCode's billing/balance API and returns
// the model display names the purchased Start Plan grants.
//
// The endpoint is the start-plan provider's baseURL with the trailing path
// replaced by `/billing/balance`. Authentication is the provider's inline JWT.
// The request carries the same headers the ZCode desktop sends (User-Agent,
// X-ZCode-App-Version, X-Title, X-Platform, X-Device-Mid, X-Client-Language);
// the API returns 400 "parameter error" without X-Device-Mid and
// X-Client-Language (verified against the live endpoint).
//
// Only entitlements with meter "model_usage" on an active plan are returned.
// The caller falls back to the static builtin list when this returns an error.
func fetchStartPlanEntitlementModels(ctx context.Context, baseURL, apiKey, version string) ([]string, error) {
	url := zcodeStartPlanBalanceURL(baseURL)
	if url == "" {
		return nil, fmt.Errorf("start-plan provider has no baseURL to derive the billing/balance URL from")
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("start-plan provider carries no API key for the billing/balance request")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("User-Agent", "ZCode/"+version)
	req.Header.Set("X-ZCode-App-Version", version)
	req.Header.Set("X-Title", "Z Code@electron")
	req.Header.Set("X-Platform", runtime.GOOS+"-"+runtime.GOARCH)
	req.Header.Set("X-Device-Mid", "leapmux")
	req.Header.Set("X-Client-Language", "en-US")

	client := &http.Client{Timeout: zcodePlanBalanceTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("billing/balance request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read billing/balance response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("billing/balance returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var envelope zcodeStartPlanBalanceResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("parse billing/balance response: %w", err)
	}
	if envelope.Code != 0 {
		return nil, fmt.Errorf("billing/balance returned code %d", envelope.Code)
	}
	if envelope.Data == nil {
		return nil, fmt.Errorf("billing/balance response carries no data")
	}

	seen := make(map[string]bool)
	var models []string
	for _, plan := range envelope.Data.Plans {
		if !strings.EqualFold(strings.TrimSpace(plan.Status), "active") {
			continue
		}
		for _, ent := range plan.Entitlements {
			if !strings.EqualFold(strings.TrimSpace(ent.Meter), "model_usage") {
				continue
			}
			name := strings.TrimSpace(ent.ShowName)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			models = append(models, name)
		}
	}
	return models, nil
}

// zcodeStartPlanBalanceURL derives the billing/balance URL from a start-plan
// provider's baseURL. The provider's baseURL ends in `/anthropic` (the
// messages endpoint); the balance endpoint replaces that path segment.
func zcodeStartPlanBalanceURL(baseURL string) string {
	u := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if u == "" {
		return ""
	}
	// Replace the trailing path segment (typically "anthropic") with the
	// billing/balance path. The base is https://zcode.z.ai/api/v1/zcode-plan.
	if i := strings.LastIndex(u, "/"); i > 0 {
		u = u[:i]
	}
	return u + "/billing/balance"
}

// zcodeAppVersion discovers the ZCode application version from the installation
// so the billing/balance request carries the same X-ZCode-App-Version the
// desktop sends. The desktop embeds it in its Electron host bundle; the app
// bundle's Info.plist (macOS) or package.json (Windows/Linux) carries the same
// value.
//
// scriptPath is the resolved zcode.cjs path (see resolve.go). The function
// walks up from the script to the app bundle root. When no version can be
// found it returns "unknown", which the API accepts (verified).
func zcodeAppVersion(scriptPath string) string {
	if scriptPath == "" {
		return "unknown"
	}
	// macOS: <root>/Contents/Resources/glm/zcode.cjs -> <root>/Contents/Info.plist
	// Other: <root>/resources/glm/zcode.cjs -> <root>/resources/../package.json
	// Walk up at most 6 levels looking for Info.plist or package.json.
	dir := strings.TrimRight(scriptPath, "/")
	for i := 0; i < 6; i++ {
		idx := strings.LastIndex(dir, "/")
		if idx <= 0 {
			break
		}
		dir = dir[:idx]
		if v := readBundleVersion(dir + "/Info.plist"); v != "" {
			return v
		}
		if v := readBundleVersion(dir + "/package.json"); v != "" {
			return v
		}
	}
	return "unknown"
}

// readBundleVersion extracts a version string from an Info.plist or
// package.json file. Returns "" when the file is missing or carries no
// version.
func readBundleVersion(path string) string {
	data, err := readFileLimited(path, 1<<20)
	if err != nil {
		return ""
	}
	if strings.HasSuffix(path, ".plist") {
		return plistVersion(data)
	}
	return jsonVersion(data)
}

// readFileLimited reads up to limit bytes from path.
func readFileLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, limit))
}

// plistVersion extracts CFBundleShortVersionString from an XML plist.
func plistVersion(data []byte) string {
	s := string(data)
	const key = "CFBundleShortVersionString"
	i := strings.Index(s, key)
	if i < 0 {
		return ""
	}
	// The value is the next <string>...</string> after the key.
	rest := s[i+len(key):]
	start := strings.Index(rest, "<string>")
	if start < 0 {
		return ""
	}
	rest = rest[start+len("<string>"):]
	end := strings.Index(rest, "</string>")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

// jsonVersion extracts the "version" field from a package.json fragment.
func jsonVersion(data []byte) string {
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return ""
	}
	return strings.TrimSpace(pkg.Version)
}
