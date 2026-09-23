package zcode

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ZCode owns these files. LeapMux reads them and never writes them.
var (
	zcodeConfigRelPath          = []string{".zcode", "v2", "config.json"}
	zcodeDesktopSettingsRelPath = []string{".zcode", "v2", "setting.json"}
)

func zcodeConfigPath(homeDir string) string {
	if homeDir == "" {
		return ""
	}
	return filepath.Join(append([]string{homeDir}, zcodeConfigRelPath...)...)
}

// zcodeConfigFile is the subset of the legacy provider file that LeapMux reads.
type zcodeConfigFile struct {
	Provider map[string]zcodeConfigProvider `json:"provider"`
}

type zcodeDesktopSettingsFile struct {
	ProviderFamilyConnectionSelections map[string]struct {
		Kind string `json:"kind"`
	} `json:"providerFamilyConnectionSelections"`
}

type zcodeConfigProvider struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Source  string `json:"source"`
	Enabled *bool  `json:"enabled"`
	Options struct {
		APIKey  string `json:"apiKey"`
		BaseURL string `json:"baseURL"`
	} `json:"options"`
	Models map[string]zcodeConfigModel `json:"models"`
}

type zcodeConfigModel struct {
	Name      string `json:"name"`
	Reasoning *struct {
		Enabled        bool     `json:"enabled"`
		Variants       []string `json:"variants"`
		DefaultVariant string   `json:"defaultVariant"`
	} `json:"reasoning"`
	Limit *struct {
		Context int64 `json:"context"`
		Output  int64 `json:"output"`
	} `json:"limit"`
	Modalities *struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	// Priority is the only model order that the file supplies. Lower values run first.
	ZCode *struct {
		Priority *float64 `json:"priority"`
	} `json:"zcode"`
}

func (m zcodeConfigModel) priority() (float64, bool) {
	if m.ZCode == nil || m.ZCode.Priority == nil {
		return 0, false
	}
	return *m.ZCode.Priority, true
}

// zcodeBuiltinProviderFile supplies current account relationships and a revision.
type zcodeBuiltinProviderFile struct {
	Revision *int64 `json:"revision"`
	Config   struct {
		ProviderConfigRules struct {
			ProviderRules []zcodeBuiltinProviderRule `json:"providerRules"`
		} `json:"providerConfigRules"`
	} `json:"config"`
}

type zcodeBuiltinProviderRule struct {
	ProviderID string `json:"providerId"`
	Config     struct {
		Access struct {
			Type        string `json:"type"`
			AccountType string `json:"accountType"`
			Mode        string `json:"mode"`
		} `json:"access"`
	} `json:"config"`
}

// loadZCodeCatalog reads credentials before the provider process starts.
func loadZCodeCatalog(homeDir string) (zcodeCatalog, error) {
	path := zcodeConfigPath(homeDir)
	if path == "" {
		return zcodeCatalog{}, fmt.Errorf("no home directory to read ZCode's configuration from")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return zcodeCatalog{}, fmt.Errorf("ZCode is not configured: %s does not exist (sign in with the ZCode application once)", path)
		}
		return zcodeCatalog{}, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg zcodeConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return zcodeCatalog{}, fmt.Errorf("parse %s: %w", path, err)
	}
	catalog, skipped := buildZCodeCatalog(cfg)
	catalog.accountPlanKinds = loadZCodeAccountPlanKinds(homeDir)
	if len(catalog.Providers) == 0 {
		return catalog, fmt.Errorf("no ZCode model provider in %s is usable%s", path, zcodeSkipDetail(skipped))
	}
	return catalog, nil
}

func loadZCodeAccountPlanKinds(homeDir string) map[string]string {
	if homeDir == "" {
		return nil
	}
	path := filepath.Join(append([]string{homeDir}, zcodeDesktopSettingsRelPath...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var settings zcodeDesktopSettingsFile
	if json.Unmarshal(data, &settings) != nil {
		return nil
	}
	out := make(map[string]string, len(settings.ProviderFamilyConnectionSelections))
	for family, selection := range settings.ProviderFamilyConnectionSelections {
		kind := strings.TrimSpace(selection.Kind)
		if kind != "" {
			out[strings.TrimSpace(family)] = kind
		}
	}
	return out
}

func zcodeBuiltinProviderRevision(path string) (string, error) {
	_, revision, err := loadZCodeBuiltinProviderFile(path)
	return revision, err
}

func loadZCodeBuiltinProviderFile(path string) (zcodeBuiltinProviderFile, string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return zcodeBuiltinProviderFile{}, "", fmt.Errorf("ZCode did not expose its built-in provider configuration path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return zcodeBuiltinProviderFile{}, "", fmt.Errorf("resolve ZCode built-in provider configuration %s: %w", path, err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return zcodeBuiltinProviderFile{}, "", fmt.Errorf("read ZCode built-in provider configuration %s: %w", abs, err)
	}
	var release zcodeBuiltinProviderFile
	if err := json.Unmarshal(data, &release); err != nil {
		return zcodeBuiltinProviderFile{}, "", fmt.Errorf("parse ZCode built-in provider configuration %s: %w", abs, err)
	}
	if release.Revision == nil {
		return zcodeBuiltinProviderFile{}, "", fmt.Errorf("ZCode built-in provider configuration %s has no revision", abs)
	}
	if *release.Revision < 0 {
		return zcodeBuiltinProviderFile{}, "", fmt.Errorf("ZCode built-in provider configuration %s has a negative revision", abs)
	}
	pathHash := sha256.Sum256([]byte(filepath.Clean(abs)))
	revision := "zcode-builtin:" + strconv.FormatInt(*release.Revision, 10) + ":" + fmt.Sprintf("%x", pathHash)
	return release, revision, nil
}

func zcodeSkipDetail(skipped []zcodeProviderSkip) string {
	if len(skipped) == 0 {
		return " (it lists no model provider)"
	}
	parts := make([]string, 0, len(skipped))
	for _, skippedProvider := range skipped {
		parts = append(parts, fmt.Sprintf("%q %s", skippedProvider.ProviderID, skippedProvider.Reason))
	}
	return ": " + strings.Join(parts, "; ")
}
