package service

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/verifycode"
	"github.com/leapmux/leapmux/locallisten"
)

// --- Device-code flow ---

func (h *APIAuthHandler) handleDeviceAuthorization(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	deviceName := r.FormValue("device_name")
	deviceCode := id.Generate()
	userCode := generateUserCode()
	if err := h.store.DeviceAuthorizations().Create(r.Context(), store.CreateDeviceAuthorizationParams{
		DeviceCode:      deviceCode,
		UserCode:        userCode,
		DeviceName:      deviceName,
		IntervalSeconds: int64(DeviceCodePollInterval / time.Second),
		ExpiresAt:       time.Now().Add(DeviceCodeTTL),
	}); err != nil {
		writeInternalError(w, "device authorization creation failed", err)
		return
	}
	verifyURI := locallisten.JoinPath(h.hubURL, "/auth/cli/activate")
	complete := verifyURI + "?user_code=" + url.QueryEscape(verifycode.Format(userCode))
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               deviceCode,
		"user_code":                 verifycode.Format(userCode),
		"verification_uri":          verifyURI,
		"verification_uri_complete": complete,
		"expires_in":                int(DeviceCodeTTL / time.Second),
		"interval":                  int(DeviceCodePollInterval / time.Second),
	})
}

// handleActivate is the user-facing page where the user enters the
// user_code displayed by the CLI. GET shows the form; POST processes it.
func (h *APIAuthHandler) handleActivate(w http.ResponseWriter, r *http.Request) {
	user := h.requireSession(r)
	if user == nil {
		next := url.QueryEscape(r.URL.RequestURI())
		http.Redirect(w, r, "/login?next="+next, http.StatusFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		userCode := r.URL.Query().Get("user_code")
		page := fmt.Sprintf(`<!doctype html><html><body style="font-family:sans-serif;max-width:480px;margin:48px auto;">
<h1>Authorize CLI device</h1>
<p>Enter the code displayed by the CLI:</p>
<form method="POST" action="/auth/cli/activate">
<input name="user_code" value="%s" pattern="[A-Z0-9-]{6,8}" autofocus required style="font-size:24px;letter-spacing:2px;text-align:center;width:100%%;padding:8px;"/>
<p><button type="submit" style="margin-top:16px;padding:10px 16px;">Authorize</button></p>
</form>
</body></html>`, html.EscapeString(userCode))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		raw := r.FormValue("user_code")
		normalized := verifycode.Normalize(raw)
		if normalized == "" {
			http.Error(w, "invalid user_code", http.StatusBadRequest)
			return
		}
		rows, err := h.store.DeviceAuthorizations().ApproveByUserCode(r.Context(), store.ApproveDeviceAuthorizationByUserCodeParams{
			UserCode: normalized,
			UserID:   user.ID,
		})
		if err != nil {
			writeInternalError(w, "device authorization approval failed", err)
			return
		}
		if rows == 0 {
			http.Error(w, "code not found or expired", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><body style="font-family:sans-serif;max-width:480px;margin:48px auto;"><h1>Device authorized</h1><p>You can close this window and return to the CLI.</p></body></html>`))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
