package zcode

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// ZCode owns these files. LeapMux reads them and never writes them.
var (
	zcodeConfigRelPath          = []string{".zcode", "v2", "config.json"}
	zcodeDesktopSettingsRelPath = []string{".zcode", "v2", "setting.json"}
	zcodeTelemetryStateRelPath  = []string{".zcode", "v2", "telemetry-state.json"}
	zcodeCodingPlanCacheRelPath = []string{".zcode", "v2", "coding-plan-cache.json"}
)

// errZCodeConfigMissing reports that config.json does not exist. The current
// desktop no longer writes that file, so a caller that can take the account plans
// from the credential store continues without it.
var errZCodeConfigMissing = errors.New("config.json does not exist")

// errZCodeConfigUnusable reports a config.json that lists no usable provider.
// The account plans of the credential store can still serve.
var errZCodeConfigUnusable = errors.New("config.json lists no usable provider")

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
	// ProviderFamilyDomain is the provider family that the desktop currently uses,
	// such as "zai". Only an account plan of this family can be current.
	ProviderFamilyDomain               string `json:"providerFamilyDomain"`
	ProviderFamilyConnectionSelections map[string]struct {
		Kind string `json:"kind"`
		// A team coding plan states the team project that it uses.
		OrganizationID string `json:"organizationId"`
		ProjectID      string `json:"projectId"`
		ProductID      string `json:"productId"`
	} `json:"providerFamilyConnectionSelections"`
}

// zcodeAccountSelection is the account connection that setting.json selects.
type zcodeAccountSelection struct {
	// domain is the current provider family, or "" when setting.json states none.
	domain string
	// planKinds maps a provider family to the coding-plan mode that the user
	// selected for it, such as "team-coding-plan".
	planKinds map[string]string
	// teams maps a provider family whose selection is a team coding plan to the
	// team project that the selection states.
	teams map[string]zcodeTeamProject
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
		ModelConfigRules struct {
			// BuiltinProviderModelRules states the models that the release
			// configures for each built-in provider.
			BuiltinProviderModelRules []struct {
				ProviderID string `json:"providerId"`
				ModelID    string `json:"modelId"`
			} `json:"builtinProviderModelRules"`
		} `json:"modelConfigRules"`
	} `json:"config"`
}

// The access values of an account rule that LeapMux reads.
const (
	zcodeAccountAccessType  = "zhipu-account"
	zcodeStartPlanMode      = "start-plan"
	zcodeTeamCodingPlanMode = "team-coding-plan"
)

type zcodeBuiltinProviderRule struct {
	ProviderID string `json:"providerId"`
	Config     struct {
		Access struct {
			Type        string `json:"type"`
			AccountType string `json:"accountType"`
			Mode        string `json:"mode"`
		} `json:"access"`
		// API states the endpoint that serves the plan's model requests.
		API struct {
			BaseURL string `json:"baseUrl"`
		} `json:"api"`
		BuiltinModelIDs []string `json:"builtinModelIds"`
	} `json:"config"`
}

func (r zcodeBuiltinProviderRule) accountType() string {
	return strings.TrimSpace(r.Config.Access.AccountType)
}

func (r zcodeBuiltinProviderRule) mode() string {
	return strings.TrimSpace(r.Config.Access.Mode)
}

// isStartPlan reports whether the rule describes a Start Plan. It is the one test
// of that fact: the rule binding, the entitlement check and the snapshot all use it.
func (r zcodeBuiltinProviderRule) isStartPlan() bool {
	return r.mode() == zcodeStartPlanMode
}

// canonicalAccountModelIDs maps the lowercase form of each model id that the
// release configures for an account provider to the spelling that the release
// uses. The billing API reports lowercase ids such as "glm-5.3-flash", and the
// app-server and the model list expect the release's spelling.
func (f zcodeBuiltinProviderFile) canonicalAccountModelIDs() map[string]string {
	out := map[string]string{}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		// The first spelling wins, so the result follows the file order.
		if _, seen := out[strings.ToLower(id)]; !seen {
			out[strings.ToLower(id)] = id
		}
	}
	accountProviders := map[string]bool{}
	for _, rule := range f.Config.ProviderConfigRules.ProviderRules {
		if strings.TrimSpace(rule.Config.Access.Type) != zcodeAccountAccessType {
			continue
		}
		accountProviders[strings.TrimSpace(rule.ProviderID)] = true
		for _, id := range rule.Config.BuiltinModelIDs {
			add(id)
		}
	}
	for _, rule := range f.Config.ModelConfigRules.BuiltinProviderModelRules {
		if accountProviders[strings.TrimSpace(rule.ProviderID)] {
			add(rule.ModelID)
		}
	}
	return out
}

// loadZCodeCatalog reads credentials before the provider process starts.
func loadZCodeCatalog(homeDir string) (zcodeCatalog, error) {
	path := zcodeConfigPath(homeDir)
	if path == "" {
		return zcodeCatalog{}, fmt.Errorf("no home directory to read ZCode's configuration from")
	}
	selection, _ := loadZCodeAccountSelection(homeDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return zcodeCatalog{accountSelection: selection}, fmt.Errorf("%w: %s", errZCodeConfigMissing, path)
		}
		return zcodeCatalog{}, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg zcodeConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return zcodeCatalog{}, fmt.Errorf("parse %s: %w", path, err)
	}
	catalog, skipped := buildZCodeCatalog(cfg)
	catalog.accountSelection = selection
	if len(catalog.Providers) == 0 {
		return catalog, fmt.Errorf("%w: no ZCode model provider in %s is usable%s", errZCodeConfigUnusable, path, zcodeSkipDetail(skipped))
	}
	return catalog, nil
}

// loadZCodeAccountSelection reads the account connection that setting.json selects.
// The caller decides whether a read failure permits an empty initial selection
// or requires preserving the last valid selection.
func loadZCodeAccountSelection(homeDir string) (zcodeAccountSelection, error) {
	if homeDir == "" {
		return zcodeAccountSelection{}, errors.New("no home directory for account selection")
	}
	path := filepath.Join(append([]string{homeDir}, zcodeDesktopSettingsRelPath...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		return zcodeAccountSelection{}, err
	}
	var settings *zcodeDesktopSettingsFile
	if err := json.Unmarshal(data, &settings); err != nil {
		return zcodeAccountSelection{}, fmt.Errorf("parse account selection: %w", err)
	}
	if settings == nil {
		return zcodeAccountSelection{}, errors.New("account selection must be an object")
	}
	selection := zcodeAccountSelection{
		domain:    strings.TrimSpace(settings.ProviderFamilyDomain),
		planKinds: make(map[string]string, len(settings.ProviderFamilyConnectionSelections)),
		teams:     map[string]zcodeTeamProject{},
	}
	for family, connection := range settings.ProviderFamilyConnectionSelections {
		family = strings.TrimSpace(family)
		kind := strings.TrimSpace(connection.Kind)
		if kind == "" {
			continue
		}
		selection.planKinds[family] = kind
		team := zcodeTeamProject{
			organizationID: zcodeTrimJSString(connection.OrganizationID),
			projectID:      zcodeTrimJSString(connection.ProjectID),
			productID:      zcodeTrimJSString(connection.ProductID),
		}
		if kind == zcodeTeamCodingPlanMode && team.organizationID != "" && team.projectID != "" {
			selection.teams[family] = team
		}
	}
	return selection, nil
}

// loadZCodeCodingPlanEntitlements decodes the desktop's coding-plan availability.
// The caller retains its previous snapshot when this read fails.
func loadZCodeCodingPlanEntitlements(homeDir string) (map[string]zcodeAccountEntitlement, error) {
	if homeDir == "" {
		return nil, errors.New("no home directory for coding-plan entitlements")
	}
	path := filepath.Join(append([]string{homeDir}, zcodeCodingPlanCacheRelPath...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cache struct {
		EntryStatus *struct {
			Items map[string]struct {
				Status string `json:"status"`
			} `json:"items"`
		} `json:"entryStatus"`
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, err
	}
	if cache.EntryStatus == nil || cache.EntryStatus.Items == nil {
		return nil, errors.New("coding-plan entitlements lack entryStatus.items")
	}
	out := make(map[string]zcodeAccountEntitlement, len(cache.EntryStatus.Items))
	for id, item := range cache.EntryStatus.Items {
		switch item.Status {
		case "available":
			out[id] = zcodeAccountEntitlement{Availability: zcodeAvailabilityAvailable}
		case "unavailable":
			out[id] = zcodeAccountEntitlement{Availability: zcodeAvailabilityUnavailable, UnavailableReason: zcodeUnavailableNotEntitled}
		default:
			return nil, fmt.Errorf("coding-plan entitlement %q has unknown status %q", id, item.Status)
		}
	}
	return out, nil
}

// loadZCodeRegistryProviders reads only the registry inputs needed by account refresh.
func loadZCodeRegistryProviders(homeDir string) ([]zcodeRegistryProvider, error) {
	path := zcodeConfigPath(homeDir)
	if path == "" {
		return nil, errors.New("no home directory for ZCode providers")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errZCodeConfigMissing
		}
		return nil, err
	}
	var cfg zcodeConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	providers, _ := buildZCodeRegistryProviders(cfg)
	return providers, nil
}

// zcodeUUIDPattern matches the device id that the desktop generates.
var zcodeUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// zcodeProcessDeviceMid is the device id of a machine where the desktop has stated
// none. It stays the same for the life of the Worker process.
var zcodeProcessDeviceMid = sync.OnceValue(func() string { return uuid.NewString() })

// loadZCodeDeviceMid returns the device id that the ZCode desktop reports for this
// machine (telemetry-state.json, which the app-server reads too), so a request of
// LeapMux adds no device to the account. Without a valid one, it returns an id
// that this Worker process generated.
func loadZCodeDeviceMid(homeDir string) string {
	if homeDir != "" {
		var state struct {
			DeviceMid string `json:"deviceMid"`
		}
		path := filepath.Join(append([]string{homeDir}, zcodeTelemetryStateRelPath...)...)
		err := sessionstore.ReadSidecarFile(path, sessionstore.MaxSidecarBytes, func(data []byte) error {
			return json.Unmarshal(data, &state)
		})
		if mid := strings.TrimSpace(state.DeviceMid); err == nil && zcodeUUIDPattern.MatchString(mid) {
			return mid
		}
	}
	return zcodeProcessDeviceMid()
}

func loadZCodeBuiltinProviderFile(path string) (zcodeBuiltinProviderFile, string, error) {
	path = zcodeTrimJSString(path)
	if path == "" {
		return zcodeBuiltinProviderFile{}, "", fmt.Errorf("ZCode did not expose its built-in provider configuration path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return zcodeBuiltinProviderFile{}, "", fmt.Errorf("resolve ZCode built-in provider configuration %s: %w", path, err)
	}
	// Native code normalizes for its revision hash, but reads the original path.
	// Cleaning symlink/../file before the read can select a different release.
	data, err := os.ReadFile(path)
	if err != nil {
		return zcodeBuiltinProviderFile{}, "", fmt.Errorf("read ZCode built-in provider configuration %s: %w", path, err)
	}
	var release zcodeBuiltinProviderFile
	if err := json.Unmarshal(data, &release); err != nil {
		return zcodeBuiltinProviderFile{}, "", fmt.Errorf("parse ZCode built-in provider configuration %s: %w", path, err)
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
