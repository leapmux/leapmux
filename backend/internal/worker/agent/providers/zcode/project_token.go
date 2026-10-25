package zcode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/quartz"
)

// A coding plan's model requests carry a short-lived project token, which the
// ZCode desktop mints from the account's OAuth access token
// (ProjectAccessTokenClient in the desktop bundle). The token belongs to an API
// key in a project of the account's organization. The desktop finds that key once,
// caches its location in the credential store, and mints a token from it for each
// request that needs a fresh one. This file ports that protocol. It reads the
// desktop's cached location and never writes the store, and it never creates an
// API key: the desktop creates "zcode-api-key" the first time the user uses the
// coding plan there.

// zcodeProjectUsageCodingPlan is the usage scene of a coding-plan API key.
const zcodeProjectUsageCodingPlan = 1

// The project token timing that the desktop uses: a token is refreshed two
// minutes before it expires, plus up to one minute of jitter, and a transient
// failure keeps a valid token for five more seconds.
const (
	zcodeProjectTokenRefreshMargin = 120 * time.Second
	zcodeProjectTokenRefreshJitter = 60 * time.Second
	zcodeProjectTokenRetryDelay    = 5 * time.Second
	// zcodeProjectTokenTimeout limits one resolution, discovery and mint together.
	zcodeProjectTokenTimeout = 15 * time.Second
	// zcodeProjectReplyMaxBytes limits one reply of the project API.
	zcodeProjectReplyMaxBytes = 64 << 10
)

// The names of the API keys that the desktop creates for ZCode.
const (
	zcodeProjectAPIKeyName     = "zcode-api-key"
	zcodeTeamProjectAPIKeyName = "zcode-team-api-key"
	zcodeTeamProjectAPIKeyType = 2
)

// The names that mark the default organization and project of an account.
const (
	zcodeDefaultOrganizationName = "默认机构"
	zcodeDefaultProjectName      = "默认项目"
)

// zcodeAccountEndpoints are the API origins of the provider families.
type zcodeAccountEndpoints struct {
	zai      string
	bigModel string
}

// zcodeProductionAccountEndpoints are the origins that the desktop uses in
// production.
var zcodeProductionAccountEndpoints = zcodeAccountEndpoints{zai: "https://api.z.ai", bigModel: "https://bigmodel.cn"}

func (e zcodeAccountEndpoints) origin(family string) string {
	if family == zcodeFamilyZAI {
		return e.zai
	}
	return e.bigModel
}

// zcodeTeamProject identifies the team project of a team coding plan. The
// location key carries the organization and the project; the account scope also
// carries the product.
type zcodeTeamProject struct {
	organizationID string
	projectID      string
	productID      string
}

func (t zcodeTeamProject) MarshalJSON() ([]byte, error) {
	return []byte(zcodeJSONStringify(struct {
		OrganizationID string `json:"organizationId"`
		ProjectID      string `json:"projectId"`
	}{t.organizationID, t.projectID})), nil
}

// zcodeProjectTokenRequest asks for a coding plan's project token.
type zcodeProjectTokenRequest struct {
	family     string
	loginToken string
	accountID  string
	// team is nil for an individual plan.
	team *zcodeTeamProject
	// rejectedFingerprint is the SHA-256 of a token that a model request refused.
	// A cached token with that fingerprint is minted again.
	rejectedFingerprint string
	// appVersion is the client version that the mint request states.
	appVersion string
}

// zcodeProjectToken is a minted project token and the API key it belongs to.
type zcodeProjectToken struct {
	token    string
	apiKeyID string
}

// The errors of a resolution, each a code that the desktop also reports.
var (
	errZCodeProjectLoginRequired = errors.New("project_token_login_required")
	errZCodeProjectUnavailable   = errors.New("project_token_project_unavailable")
	errZCodeProjectInvalidReply  = errors.New("project_token_invalid_response")
	errZCodeProjectDisabled      = errors.New("project_token_disabled")
	errZCodeProjectRequestFailed = errors.New("project_token_request_failed")
	errZCodeProjectTransient     = errors.New("project_token_transient_failure")
	// errZCodeProjectAPIKeyMissing means that the project has no API key for ZCode.
	// The desktop would create one. LeapMux does not change the account.
	errZCodeProjectAPIKeyMissing = errors.New("the ZCode API key of this coding plan does not exist yet; use the coding plan in the ZCode application once")
)

// zcodeProjectTokenClient resolves project tokens. One client serves every agent
// of the Worker process, so the agents share each token as the desktop's sessions
// do.
type zcodeProjectTokenClient struct {
	http      *http.Client
	endpoints zcodeAccountEndpoints
	clock     quartz.Clock
	jitter    func() float64

	mu sync.Mutex
	// locations maps a location key to the location that served it.
	locations map[string]zcodeProjectLocation
	// staleStoreLocations holds the location keys whose stored location the
	// project API no longer knows. LeapMux cannot delete the desktop's entry, so it
	// skips it until the desktop replaces it.
	staleStoreLocations map[string]zcodeProjectLocation
	tokens              map[string]*zcodeProjectTokenEntry
	// discoveries and mints share one request among the agents that need the same
	// location or token at once.
	discoveries zcodeSharedCalls[string, zcodeProjectLocation]
	mints       zcodeSharedCalls[string, zcodeProjectTokenValue]
}

// zcodeProjectTokenEntry is a cached token.
type zcodeProjectTokenEntry struct {
	value       zcodeProjectTokenValue
	fingerprint string
	refreshAt   time.Time
}

type zcodeProjectTokenValue struct {
	token     string
	expiresAt time.Time
}

func newZCodeProjectTokenClient(client *http.Client, endpoints zcodeAccountEndpoints, clock quartz.Clock, jitter func() float64) *zcodeProjectTokenClient {
	return &zcodeProjectTokenClient{
		http:                client,
		endpoints:           endpoints,
		clock:               clock,
		jitter:              jitter,
		locations:           map[string]zcodeProjectLocation{},
		staleStoreLocations: map[string]zcodeProjectLocation{},
		tokens:              map[string]*zcodeProjectTokenEntry{},
	}
}

// zcodeProjectTokens is the client of the Worker process.
var zcodeProjectTokens = newZCodeProjectTokenClient(zcodeStartPlanHTTPClient, zcodeProductionAccountEndpoints, quartz.NewReal(), rand.Float64)

// zcodeBearerPrefix matches the scheme that a stored login token can carry, as the
// desktop strips it (`replace(/^Bearer\s+/i, "")`).
var zcodeBearerPrefix = regexp.MustCompile(`(?i)^Bearer\s+`)

// zcodeTokenFingerprint is the SHA-256 that the app-server sends for a rejected
// token.
func zcodeTokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// resolve returns the project token of the coding plan that req describes.
func (c *zcodeProjectTokenClient) resolve(ctx context.Context, credentials zcodeCredentials, req zcodeProjectTokenRequest) (zcodeProjectToken, error) {
	loginToken := zcodeBearerPrefix.ReplaceAllString(strings.TrimSpace(req.loginToken), "")
	if loginToken == "" || strings.TrimSpace(req.accountID) == "" {
		return zcodeProjectToken{}, errZCodeProjectLoginRequired
	}
	origin := c.endpoints.origin(req.family)
	locationKey := zcodeProjectLocationKey(origin, req.family, req.accountID, req.team)
	location, err := c.location(ctx, credentials, req, origin, loginToken, locationKey)
	if err != nil {
		return zcodeProjectToken{}, err
	}
	// The login token is part of the scope, so a new sign-in never reuses a token
	// that the previous one minted.
	scope := zcodeJSONStringify([]any{origin, req.family, req.accountID, location.OrganizationID, location.ProjectID,
		location.APIKeyID, zcodeProjectUsageCodingPlan, zcodeTokenFingerprint(loginToken)})
	value, err := c.token(ctx, scope, req.rejectedFingerprint, func(ctx context.Context) (zcodeProjectTokenValue, error) {
		return c.mint(ctx, origin, loginToken, location, req.appVersion, locationKey)
	})
	if err != nil {
		return zcodeProjectToken{}, err
	}
	return zcodeProjectToken{token: value.token, apiKeyID: location.APIKeyID}, nil
}

// location returns the project and the API key that serve the plan: the one this
// process found earlier, the one the desktop cached, or one that discovery finds.
func (c *zcodeProjectTokenClient) location(ctx context.Context, credentials zcodeCredentials, req zcodeProjectTokenRequest, origin, loginToken, key string) (zcodeProjectLocation, error) {
	c.mu.Lock()
	location, ok := c.locations[key]
	stale, skipStore := c.staleStoreLocations[key]
	c.mu.Unlock()
	if ok {
		return location, nil
	}
	if stored, found := credentials.projectLocation(key); found && (!skipStore || stored != stale) && zcodeLocationServesTeam(stored, req.team) {
		c.remember(key, stored)
		return stored, nil
	}
	return c.discoveries.do(ctx, key, zcodeProjectTokenTimeout, func(ctx context.Context) (zcodeProjectLocation, error) {
		found, err := c.discover(ctx, req, origin, loginToken)
		if err != nil {
			return zcodeProjectLocation{}, err
		}
		c.remember(key, found)
		return found, nil
	})
}

func zcodeLocationServesTeam(location zcodeProjectLocation, team *zcodeTeamProject) bool {
	return team == nil || (location.OrganizationID == team.organizationID && location.ProjectID == team.projectID)
}

func (c *zcodeProjectTokenClient) remember(key string, location zcodeProjectLocation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.locations[key] = location
}

// forget drops a location that the project API no longer knows, so the next
// resolution discovers it again.
func (c *zcodeProjectTokenClient) forget(key string, location zcodeProjectLocation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.locations, key)
	c.staleStoreLocations[key] = location
}

// discover finds the plan's project and its ZCode API key, as the desktop's
// ProjectAccessTokenClient does, except that it never creates the key.
func (c *zcodeProjectTokenClient) discover(ctx context.Context, req zcodeProjectTokenRequest, origin, loginToken string) (zcodeProjectLocation, error) {
	var customer struct {
		Organizations []struct {
			OrganizationID   string `json:"organizationId"`
			OrganizationName string `json:"organizationName"`
			Projects         []struct {
				ProjectID   string          `json:"projectId"`
				ProjectName string          `json:"projectName"`
				ProjectType json.RawMessage `json:"projectType"`
			} `json:"projects"`
		} `json:"organizations"`
	}
	data, err := c.call(ctx, http.MethodGet, origin+"/api/biz/customer/getCustomerInfo",
		http.Header{"Authorization": {loginToken}}, nil)
	if err != nil {
		return zcodeProjectLocation{}, err
	}
	if json.Unmarshal(data, &customer) != nil {
		return zcodeProjectLocation{}, errZCodeProjectInvalidReply
	}

	var organizationID, projectID string
	if req.team != nil {
		found := false
		for _, organization := range customer.Organizations {
			if strings.TrimSpace(organization.OrganizationID) != strings.TrimSpace(req.team.organizationID) {
				continue
			}
			for _, project := range organization.Projects {
				if strings.TrimSpace(project.ProjectID) == strings.TrimSpace(req.team.projectID) {
					found = true
				}
			}
		}
		if !found {
			return zcodeProjectLocation{}, errZCodeProjectUnavailable
		}
		organizationID, projectID = req.team.organizationID, req.team.projectID
	} else {
		// An individual plan uses the account's default organization and project,
		// leaving out team projects (projectType "2").
		type candidate struct{ id, name string }
		var chosenOrganization *candidate
		var chosenProjects []candidate
		for _, organization := range customer.Organizations {
			var projects []candidate
			for _, project := range organization.Projects {
				if strings.TrimSpace(strings.Trim(string(project.ProjectType), `"`)) == "2" {
					continue
				}
				projects = append(projects, candidate{project.ProjectID, project.ProjectName})
			}
			if organization.OrganizationID == "" || len(projects) == 0 {
				continue
			}
			current := candidate{organization.OrganizationID, organization.OrganizationName}
			if chosenOrganization == nil || (!strings.Contains(chosenOrganization.name, zcodeDefaultOrganizationName) && strings.Contains(current.name, zcodeDefaultOrganizationName)) {
				chosenOrganization, chosenProjects = &current, projects
			}
		}
		if chosenOrganization == nil {
			return zcodeProjectLocation{}, errZCodeProjectUnavailable
		}
		project := chosenProjects[0]
		for _, candidate := range chosenProjects {
			if strings.Contains(candidate.name, zcodeDefaultProjectName) {
				project = candidate
				break
			}
		}
		if project.id == "" {
			return zcodeProjectLocation{}, errZCodeProjectUnavailable
		}
		organizationID, projectID = chosenOrganization.id, project.id
	}

	headers := http.Header{"Authorization": {loginToken}}
	name := zcodeProjectAPIKeyName
	if req.team != nil {
		headers.Set("bigmodel-organization", organizationID)
		headers.Set("bigmodel-project", projectID)
		name = zcodeTeamProjectAPIKeyName
	} else if req.family == zcodeFamilyZAI {
		headers.Set("Authorization", "Bearer "+loginToken)
	}
	data, err = c.call(ctx, http.MethodGet, zcodeAPIKeyListURL(origin, organizationID, projectID), headers, nil)
	if err != nil {
		return zcodeProjectLocation{}, err
	}
	var keys []json.RawMessage
	if json.Unmarshal(data, &keys) != nil {
		return zcodeProjectLocation{}, errZCodeProjectInvalidReply
	}
	for _, raw := range keys {
		var key struct {
			APIKey  string `json:"apiKey"`
			Name    string `json:"name"`
			KeyType *int   `json:"keyType"`
		}
		if json.Unmarshal(raw, &key) != nil || key.Name != name {
			continue
		}
		if req.team != nil && (key.KeyType == nil || *key.KeyType != zcodeTeamProjectAPIKeyType) {
			continue
		}
		if strings.TrimSpace(key.APIKey) == "" {
			return zcodeProjectLocation{}, errZCodeProjectInvalidReply
		}
		return zcodeProjectLocation{OrganizationID: organizationID, ProjectID: projectID, APIKeyID: strings.TrimSpace(key.APIKey)}, nil
	}
	return zcodeProjectLocation{}, errZCodeProjectAPIKeyMissing
}

func zcodeAPIKeyListURL(origin, organizationID, projectID string) string {
	return origin + "/api/biz/v1/organization/" + zcodeEncodeURIComponent(organizationID) +
		"/projects/" + zcodeEncodeURIComponent(projectID) + "/api_keys"
}

// mint asks the project API for a new token of the location's API key.
func (c *zcodeProjectTokenClient) mint(ctx context.Context, origin, loginToken string, location zcodeProjectLocation, appVersion, locationKey string) (zcodeProjectTokenValue, error) {
	body, _ := json.Marshal(map[string]string{"clientType": "zcode", "clientVersion": appVersion})
	started := c.clock.Now()
	data, err := c.call(ctx, http.MethodPost,
		zcodeAPIKeyListURL(origin, location.OrganizationID, location.ProjectID)+"/"+zcodeEncodeURIComponent(location.APIKeyID)+"/access_tokens",
		http.Header{"Authorization": {"Bearer " + loginToken}}, body)
	if errors.Is(err, errZCodeProjectNotFound) {
		c.forget(locationKey, location)
		return zcodeProjectTokenValue{}, errZCodeProjectInvalidReply
	}
	if err != nil {
		return zcodeProjectTokenValue{}, err
	}
	var minted struct {
		Enable      *bool    `json:"enable"`
		AccessToken string   `json:"accessToken"`
		TokenType   string   `json:"tokenType"`
		ExpiresIn   *float64 `json:"expiresIn"`
		ExpiresAt   *float64 `json:"expiresAt"`
	}
	if json.Unmarshal(data, &minted) != nil {
		return zcodeProjectTokenValue{}, errZCodeProjectInvalidReply
	}
	// A reply that states no "enable" grants a token, as the desktop reads it. The
	// live API states "enabled" instead, which the desktop's schema ignores, so this
	// reads "enable" only: reading "enabled" would refuse a token that the desktop
	// uses.
	if minted.Enable != nil && !*minted.Enable {
		return zcodeProjectTokenValue{}, errZCodeProjectDisabled
	}
	token := strings.TrimSpace(minted.AccessToken)
	if token == "" || minted.TokenType != "Bearer" || minted.ExpiresIn == nil || minted.ExpiresAt == nil ||
		!(*minted.ExpiresIn > 0) || math.IsInf(*minted.ExpiresIn, 0) || !(*minted.ExpiresAt > 0) || math.IsInf(*minted.ExpiresAt, 0) {
		return zcodeProjectTokenValue{}, errZCodeProjectInvalidReply
	}
	expiresAt := time.UnixMilli(int64(*minted.ExpiresAt * 1000))
	if limit := started.Add(time.Duration(*minted.ExpiresIn * float64(time.Second))); limit.Before(expiresAt) {
		expiresAt = limit
	}
	if !expiresAt.After(c.clock.Now()) {
		return zcodeProjectTokenValue{}, errZCodeProjectInvalidReply
	}
	return zcodeProjectTokenValue{token: token, expiresAt: expiresAt}, nil
}

// token returns the cached token of scope, or mints one, as the desktop's
// ProjectAccessTokenCache does.
func (c *zcodeProjectTokenClient) token(ctx context.Context, scope, rejectedFingerprint string, mint func(context.Context) (zcodeProjectTokenValue, error)) (zcodeProjectTokenValue, error) {
	c.mu.Lock()
	entry := c.tokens[scope]
	if entry != nil && rejectedFingerprint != "" && entry.fingerprint == rejectedFingerprint {
		entry.value, entry.refreshAt = zcodeProjectTokenValue{}, time.Time{}
	}
	now := c.clock.Now()
	if entry != nil && entry.value.token != "" && now.Before(entry.refreshAt) && now.Before(entry.value.expiresAt) {
		value := entry.value
		c.mu.Unlock()
		return value, nil
	}
	c.mu.Unlock()

	return c.mints.do(ctx, scope, zcodeProjectTokenTimeout, func(ctx context.Context) (zcodeProjectTokenValue, error) {
		value, err := mint(ctx)
		c.mu.Lock()
		defer c.mu.Unlock()
		entry := c.tokens[scope]
		if err != nil {
			// A transient failure keeps a token that is still valid for a few more
			// seconds, so a short outage does not fail every model request.
			if errors.Is(err, errZCodeProjectTransient) && entry != nil && entry.value.token != "" && c.clock.Now().Before(entry.value.expiresAt) {
				entry.refreshAt = minTime(c.clock.Now().Add(zcodeProjectTokenRetryDelay), entry.value.expiresAt)
				return entry.value, nil
			}
			delete(c.tokens, scope)
			return zcodeProjectTokenValue{}, err
		}
		jitter := time.Duration(math.Max(0, math.Min(1, c.jitter())) * float64(zcodeProjectTokenRefreshJitter))
		c.dropExpiredTokens()
		c.tokens[scope] = &zcodeProjectTokenEntry{
			value:       value,
			fingerprint: zcodeTokenFingerprint(value.token),
			refreshAt:   value.expiresAt.Add(-zcodeProjectTokenRefreshMargin - jitter),
		}
		return value, nil
	})
}

// dropExpiredTokens deletes each cached token that expired. The scope of a token
// holds the sign-in that minted it, and the desktop renews the sign-in, so
// without this the cache keeps one token for each sign-in for the life of the
// Worker. The caller holds c.mu.
func (c *zcodeProjectTokenClient) dropExpiredTokens() {
	now := c.clock.Now()
	for scope, entry := range c.tokens {
		if !now.Before(entry.value.expiresAt) {
			delete(c.tokens, scope)
		}
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// errZCodeProjectNotFound reports an HTTP 404, which means that the project API
// no longer knows the location.
var errZCodeProjectNotFound = errors.New("the project API returned HTTP 404")

// call sends one request to the project API and returns the data of its
// envelope. It refuses a redirect, as every credentialed request of this package
// does.
func (c *zcodeProjectTokenClient) call(ctx context.Context, method, target string, header http.Header, body []byte) (json.RawMessage, error) {
	if parsed, err := url.Parse(target); err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errZCodeProjectRequestFailed
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, errZCodeProjectRequestFailed
	}
	req.Header = header.Clone()
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %w", errZCodeProjectTransient, ctx.Err())
		}
		return nil, errZCodeProjectTransient
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errZCodeProjectNotFound
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return nil, errZCodeProjectTransient
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, fmt.Errorf("%w: HTTP %d", errZCodeProjectRequestFailed, resp.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, zcodeProjectReplyMaxBytes+1))
	if err != nil || len(payload) > zcodeProjectReplyMaxBytes {
		return nil, errZCodeProjectInvalidReply
	}
	var envelope struct {
		Code json.RawMessage `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return nil, errZCodeProjectInvalidReply
	}
	switch strings.TrimSpace(string(envelope.Code)) {
	case "0", "200", `"0"`, `"200"`:
	case "404":
		return nil, errZCodeProjectNotFound
	default:
		return nil, errZCodeProjectInvalidReply
	}
	return envelope.Data, nil
}
