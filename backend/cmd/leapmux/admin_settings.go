package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/internal/hub/captcha"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/ratelimit"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// settingsManagerFor builds the same settings manager the hub resolves
// with (same descriptors, same cross rules), so the CLI's idea of
// "effective" and the hub's can never diverge. The keystore is loaded
// once per invocation for the secret-bearing keys; LoadOrGenerate, not
// LoadFromFile, because the very first admin action on a fresh data dir
// may precede any hub run.
func settingsManagerFor(cfg *config.Config, st store.Store) (*settings.Manager, error) {
	ks, err := keystore.LoadOrGenerate(cfg.EncryptionKeyFilePath())
	if err != nil {
		return nil, fmt.Errorf("load encryption key: %w", err)
	}
	descs := append(settings.CoreDescriptors(), captcha.SettingsDescriptors()...)
	descs = append(descs, ratelimit.SettingsDescriptors()...)
	m := settings.NewManager(st, ks, descs, settings.WithCrossValidation(settings.SMTPConfigured))
	if err := m.Load(context.Background()); err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	return m, nil
}

// settingsListEntry is one row of `settings list` output: the key, its
// effective value with secrets redacted, whether it is at its default,
// and how a change reaches a running hub.
type settingsListEntry struct {
	Key         string `json:"key"`
	Value       any    `json:"value"`
	Propagation string `json:"propagation"`
	Customized  bool   `json:"customized"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

func runSettingsList(cmd adminCmdCtx, args []string) error {
	return withAdminStore(cmd, args, nil, func(ctx context.Context, cfg *config.Config, st store.Store) error {
		m, err := settingsManagerFor(cfg, st)
		if err != nil {
			return err
		}
		snap := m.Snapshot(ctx)
		entries := make([]settingsListEntry, 0, 64)
		for _, desc := range m.Registered() {
			v := snap.ValueOf(desc)
			if v == nil {
				continue
			}
			entries = append(entries, settingsListEntry{
				Key:         desc.Name(),
				Value:       desc.Redacted(v),
				Propagation: desc.Propagation().String(),
				Customized:  snap.Customized(desc),
				UpdatedAt:   snap.UpdatedAt(desc).UTC().Format("2006-01-02T15:04:05Z"),
			})
		}
		return printJSON(entries)
	})
}

// runSettingsGet prints one key's effective value with secrets redacted.
func runSettingsGet(cmd adminCmdCtx, args []string) error {
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {}, func(ctx context.Context, cfg *config.Config, st store.Store) error {
		if len(args) != 1 {
			return fmt.Errorf("usage: leapmux admin settings get KEY")
		}
		m, err := settingsManagerFor(cfg, st)
		if err != nil {
			return err
		}
		desc, ok := m.Descriptor(args[0])
		if !ok {
			return fmt.Errorf("unknown setting key %q (see `leapmux admin settings list`)", args[0])
		}
		snap := m.Snapshot(ctx)
		return printJSON(map[string]any{
			"key":          desc.Name(),
			"value":        desc.Redacted(snap.ValueOf(desc)),
			"propagation":  desc.Propagation().String(),
			"customized":   snap.Customized(desc),
			"updated_at":   snap.UpdatedAt(desc).UTC().Format("2006-01-02T15:04:05Z"),
			"description":  settingsKeyDescription(desc.Name()),
			"value_schema": settingsKeyShape(desc),
		})
	})
}

// runSettingsSet writes one key's public half from a JSON document (or a
// bare scalar for scalar keys). Fields the document omits keep their
// current or default values.
func runSettingsSet(cmd adminCmdCtx, args []string) error {
	return withAdminStore(cmd, args, nil, func(ctx context.Context, cfg *config.Config, st store.Store) error {
		if len(args) != 2 {
			return fmt.Errorf("usage: leapmux admin settings set KEY VALUE  (VALUE is JSON, or a bare scalar for scalar keys)")
		}
		m, err := settingsManagerFor(cfg, st)
		if err != nil {
			return err
		}
		desc, ok := m.Descriptor(args[0])
		if !ok {
			return fmt.Errorf("unknown setting key %q (see `leapmux admin settings list`)", args[0])
		}
		doc, err := parseSettingValue(args[1])
		if err != nil {
			return fmt.Errorf("invalid value for %q: %w", args[0], err)
		}
		if err := m.Update(ctx, desc, doc); err != nil {
			return err
		}
		return reportSettingWrite(m, desc, ctx)
	})
}

// runSettingsSetSecret writes one key's secret half. Mechanically an
// Update whose fields land in the encrypted column; a separate verb so
// the intent (and the redaction guarantees around it) are explicit.
func runSettingsSetSecret(cmd adminCmdCtx, args []string) error {
	return withAdminStore(cmd, args, nil, func(ctx context.Context, cfg *config.Config, st store.Store) error {
		if len(args) != 2 {
			return fmt.Errorf("usage: leapmux admin settings set-secret KEY JSON")
		}
		m, err := settingsManagerFor(cfg, st)
		if err != nil {
			return err
		}
		desc, ok := m.Descriptor(args[0])
		if !ok {
			return fmt.Errorf("unknown setting key %q (see `leapmux admin settings list`)", args[0])
		}
		if err := m.UpdateSecret(ctx, desc, json.RawMessage(args[1])); err != nil {
			return err
		}
		return reportSettingWrite(m, desc, ctx)
	})
}

// runSettingsReset removes one key's row, returning it to its code
// default.
func runSettingsReset(cmd adminCmdCtx, args []string) error {
	return withAdminStore(cmd, args, nil, func(ctx context.Context, cfg *config.Config, st store.Store) error {
		if len(args) != 1 {
			return fmt.Errorf("usage: leapmux admin settings reset KEY")
		}
		m, err := settingsManagerFor(cfg, st)
		if err != nil {
			return err
		}
		desc, ok := m.Descriptor(args[0])
		if !ok {
			return fmt.Errorf("unknown setting key %q (see `leapmux admin settings list`)", args[0])
		}
		if err := m.Reset(ctx, desc); err != nil {
			return err
		}
		fmt.Printf("Reset %s to its default\n", desc.Name())
		return nil
	})
}

// parseSettingValue accepts a JSON document or, for readability on the
// command line, a bare unquoted scalar (string, integer, boolean) which
// is re-quoted into valid JSON.
func parseSettingValue(raw string) (json.RawMessage, error) {
	if v, err := strconv.ParseBool(raw); err == nil {
		return json.RawMessage(fmt.Sprintf("%v", v)), nil
	}
	if _, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return json.RawMessage(raw), nil
	}
	if _, err := strconv.ParseFloat(raw, 64); err == nil {
		return json.RawMessage(raw), nil
	}
	if json.Valid([]byte(raw)) {
		return json.RawMessage(raw), nil
	}
	// Bare string scalar: quote it.
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// reportSettingWrite confirms the write and states how it reaches a
// running hub — the one piece of operational information every set
// carries.
func reportSettingWrite(m *settings.Manager, desc settings.Descriptor, ctx context.Context) error {
	if desc.Propagation() == settings.PropagationRestart {
		fmt.Printf("Saved %s (applies after a hub restart)\n", desc.Name())
		return nil
	}
	snap := m.Snapshot(ctx)
	fmt.Printf("Saved %s = %s (reaches a running hub within ~30s)\n",
		desc.Name(), mustMarshal(desc.Redacted(snap.ValueOf(desc))))
	return nil
}

func mustMarshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "<undecodable>"
	}
	return string(b)
}

// settingsKeyDescription gives the generic CLI's `get` a one-line
// explanation per key. The domain verbs (captcha, rate-limit) carry
// their own richer help.
func settingsKeyDescription(key string) string {
	switch {
	case strings.HasPrefix(key, "captcha."):
		return "captcha provider configuration (prefer `leapmux admin captcha ...`)"
	case strings.HasPrefix(key, "rate_limit."):
		return "per-operation rate limit override (prefer `leapmux admin rate-limit ...`)"
	}
	switch key {
	case "signup_enabled":
		return "whether user sign-up is open"
	case "email_verification_required":
		return "require verified email before sign-in (needs smtp configured)"
	case "session_duration_seconds":
		return "idle session lifetime in seconds (minimum 300)"
	case "secure_cookies":
		return "use __Host- prefixed cookies (behind TLS); changing it signs everyone out"
	case "public_url":
		return "public base URL when running behind a reverse proxy (scheme+host only)"
	case "smtp":
		return "SMTP relay configuration; the password lives in the secret half"
	case "timeouts":
		return "API/agent-startup/worktree-create timeouts in seconds"
	case "limits":
		return "per-user connection and worker caps (0 = unlimited)"
	case "max_message_size_bytes":
		return "maximum application payload size (64 KiB - 64 MiB); applies after restart"
	case "queue_budget":
		return "outbound queue memory pool budgets in bytes (0 = auto-size); applies after restart"
	}
	return ""
}

// settingsKeyShape renders the JSON shape hint for `get` output.
func settingsKeyShape(desc settings.Descriptor) string {
	switch v := desc.Default().(type) {
	case bool:
		return "boolean"
	case int64:
		return "integer"
	case string:
		return "string"
	case settings.SMTPValue:
		return `{"host", "port", "username", "from_address", "tls_mode"} + secret {"password"}`
	case settings.TimeoutsValue:
		return `{"api_seconds", "agent_startup_seconds", "worktree_create_seconds"}`
	case settings.LimitsValue:
		return `{"max_connections_per_user", "max_workers_per_user"}`
	case settings.QueueBudgetValue:
		return `{"relay_bytes", "worker_bytes", "userevents_bytes"}`
	default:
		_ = v
		return "object"
	}
}
