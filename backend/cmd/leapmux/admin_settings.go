package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/settingsregistry"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// settingsManagerFor builds the same settings manager the hub resolves
// with (settingsregistry: every domain's keys, both cross rules), so the
// CLI's idea of "effective" and the hub's can never diverge. The keystore
// is loaded once per invocation for the secret-bearing keys;
// LoadOrGenerate, not LoadFromFile, because the very first admin action
// on a fresh data dir may precede any hub run.
func settingsManagerFor(cfg *config.Config, st store.Store) (*settings.Manager, error) {
	ks, err := keystore.LoadOrGenerate(cfg.EncryptionKeyFilePath())
	if err != nil {
		return nil, fmt.Errorf("load encryption key: %w", err)
	}
	m := settingsregistry.NewManager(st, ks)
	if err := m.Load(context.Background()); err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	return m, nil
}

// openSetting resolves the manager and the descriptor the get/set/
// set-secret/reset verbs all start from, with the shared unknown-key
// error.
func openSetting(ctx context.Context, cfg *config.Config, st store.Store, key string) (*settings.Manager, settings.Descriptor, error) {
	m, err := settingsManagerFor(cfg, st)
	if err != nil {
		return nil, nil, err
	}
	desc, ok := m.Descriptor(key)
	if !ok {
		return nil, nil, fmt.Errorf("unknown setting key %q (see `leapmux admin settings list`)", key)
	}
	return m, desc, nil
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
			// A zero UpdatedAt means "no stored row": rendering it would
			// print the year-1 zero time, which reads as corrupt data.
			var updatedAt string
			if t := snap.UpdatedAt(desc); !t.IsZero() {
				updatedAt = t.UTC().Format("2006-01-02T15:04:05Z")
			}
			entries = append(entries, settingsListEntry{
				Key:         desc.Name(),
				Value:       desc.Redacted(v),
				Propagation: desc.Propagation().String(),
				Customized:  snap.Customized(desc),
				UpdatedAt:   updatedAt,
			})
		}
		return printJSON(entries)
	})
}

// runSettingsGet prints one key's effective value with secrets redacted.
func runSettingsGet(cmd adminCmdCtx, args []string) error {
	return withAdminStoreArgs(cmd, args, func(fs *flag.FlagSet) {}, func(ctx context.Context, cfg *config.Config, st store.Store, rest []string) error {
		if len(rest) != 1 {
			return fmt.Errorf("usage: leapmux admin settings get KEY (flags first, then KEY)")
		}
		m, desc, err := openSetting(ctx, cfg, st, rest[0])
		if err != nil {
			return err
		}
		snap := m.Snapshot(ctx)
		var updatedAt string
		if t := snap.UpdatedAt(desc); !t.IsZero() {
			updatedAt = t.UTC().Format("2006-01-02T15:04:05Z")
		}
		return printJSON(map[string]any{
			"key":          desc.Name(),
			"value":        desc.Redacted(snap.ValueOf(desc)),
			"propagation":  desc.Propagation().String(),
			"customized":   snap.Customized(desc),
			"updated_at":   updatedAt,
			"description":  settingDescription(desc),
			"value_schema": settingShape(desc),
		})
	})
}

// runSettingsSet writes one key's public half from a JSON document (or a
// bare scalar for scalar keys). Fields the document omits keep their
// current or default values.
func runSettingsSet(cmd adminCmdCtx, args []string) error {
	return withAdminStoreArgs(cmd, args, nil, func(ctx context.Context, cfg *config.Config, st store.Store, rest []string) error {
		if len(rest) != 2 {
			return fmt.Errorf("usage: leapmux admin settings set KEY VALUE  (VALUE is JSON, or a bare scalar for scalar keys; flags first, then KEY VALUE)")
		}
		m, desc, err := openSetting(ctx, cfg, st, rest[0])
		if err != nil {
			return err
		}
		doc, err := parseSettingValue(rest[1])
		if err != nil {
			return fmt.Errorf("invalid value for %q: %w", rest[0], err)
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
	return withAdminStoreArgs(cmd, args, nil, func(ctx context.Context, cfg *config.Config, st store.Store, rest []string) error {
		if len(rest) != 2 {
			return fmt.Errorf("usage: leapmux admin settings set-secret KEY JSON (flags first, then KEY JSON)")
		}
		m, desc, err := openSetting(ctx, cfg, st, rest[0])
		if err != nil {
			return err
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
	return withAdminStoreArgs(cmd, args, nil, func(ctx context.Context, cfg *config.Config, st store.Store, rest []string) error {
		if len(rest) != 1 {
			return fmt.Errorf("usage: leapmux admin settings reset KEY (flags first, then KEY)")
		}
		m, desc, err := openSetting(ctx, cfg, st, rest[0])
		if err != nil {
			return err
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

// settingDescription renders the `get` output's one-line explanation:
// the key's declared summary, prefixed with the domain-verb pointer for
// the captcha and rate-limit domains (their dedicated verbs carry the
// richer help).
func settingDescription(desc settings.Descriptor) string {
	summary, _ := desc.Doc()
	switch {
	case strings.HasPrefix(desc.Name(), "captcha."):
		return summary + " (prefer `leapmux admin captcha ...`)"
	case strings.HasPrefix(desc.Name(), "rate_limit."):
		return summary + " (prefer `leapmux admin rate-limit ...`)"
	}
	return summary
}

// settingShape renders the JSON shape hint for `get` output: the key's
// declared shape, falling back to the scalar type of the default for
// keys that declared none.
func settingShape(desc settings.Descriptor) string {
	if _, shape := desc.Doc(); shape != "" {
		return shape
	}
	switch desc.Default().(type) {
	case bool:
		return "boolean"
	case int64:
		return "integer"
	case string:
		return "string"
	default:
		return "object"
	}
}
