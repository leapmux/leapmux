package zcode

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"
)

// zcodeStartPlanBalancePath is the path of ZCode's billing endpoint on the origin
// of a Start Plan provider. The ZCode desktop builds the same URL from its own
// origin (buildZaiStartPlanBalanceUrl).
const zcodeStartPlanBalancePath = "/api/v1/zcode-plan/billing/balance"

// zcodeStartPlanCheckTimeout limits the whole entitlement check of one agent
// start, for every Start Plan together. The desktop gives the same request 15
// seconds.
const zcodeStartPlanCheckTimeout = 15 * time.Second

// zcodeStartPlanBalanceMaxBytes limits a successful billing reply. A plan list is
// a few kilobytes.
const zcodeStartPlanBalanceMaxBytes = 1 << 20

// zcodeStartPlanErrorExcerptBytes limits the part of a failed reply that an error
// quotes. A log line has no use for the rest.
const zcodeStartPlanErrorExcerptBytes = 4 << 10

// zcodeUnknownAppVersion is the version that a billing request states when
// LeapMux cannot find the version of the installed application.
const zcodeUnknownAppVersion = "unknown"

// zcodeStartPlanBalanceReply is the billing/balance reply. Each field keeps the
// JSON form that the desktop's interpretation reads.
type zcodeStartPlanBalanceReply struct {
	// Success and Code stay raw, because the desktop tests them for exact JSON
	// values: an absent code passes, and a null code fails.
	Success json.RawMessage            `json:"success"`
	Code    json.RawMessage            `json:"code"`
	Data    *zcodeStartPlanBalanceData `json:"data"`
}

type zcodeStartPlanBalanceData struct {
	// ServerTime is the server's clock in seconds since the epoch.
	ServerTime json.RawMessage         `json:"server_time"`
	Plans      []zcodeStartPlanPlan    `json:"plans"`
	Balances   []zcodeStartPlanBalance `json:"balances"`
}

// zcodeStartPlanPlan is one plan that the account bought. The times stay raw,
// because the desktop converts them with JavaScript's Number(), which accepts a
// number and a numeric string alike.
type zcodeStartPlanPlan struct {
	PlanID       string          `json:"plan_id"`
	UserPlanID   string          `json:"user_plan_id"`
	Name         string          `json:"name"`
	Status       string          `json:"status"`
	StartsAt     json.RawMessage `json:"starts_at"`
	EndsAt       json.RawMessage `json:"ends_at"`
	Entitlements []struct {
		EffectiveAt json.RawMessage `json:"effective_at"`
	} `json:"entitlements"`
}

// zcodeStartPlanBalance is the remaining usage of one plan. Its capabilities
// state the models that the plan grants, as "model:<id>" entries.
type zcodeStartPlanBalance struct {
	PlanID       string   `json:"plan_id"`
	UserPlanID   string   `json:"user_plan_id"`
	ShowName     string   `json:"show_name"`
	Capabilities []string `json:"capabilities"`
}

// zcodeCredentialFailedEntitlement is the entitlement of a plan whose credential
// the billing API refused.
var zcodeCredentialFailedEntitlement = zcodeAccountEntitlement{
	Availability:      zcodeAvailabilityUnavailable,
	UnavailableReason: zcodeUnavailableCredentialFailed,
}

// interpretZCodeStartPlanBalance reads a billing/balance reply as the ZCode
// desktop does (resolveStartPlanBalanceAvailability), in this order:
//
//  1. An active plan whose ends_at has passed is expired, and the balances of
//     plans that all expired are dropped.
//  2. A reply that reports a failure means that the credential failed.
//  3. A reply with no active Start Plan means that the account holds none.
//  4. The models are the "model:" capabilities of the remaining balances, or the
//     show_name of a balance that states none, in the release's spelling.
//  5. A plan with no model, whose every entitlement takes effect later, is
//     pending.
//
// dateSeconds is the reply's Date header in seconds since the epoch, or NaN when
// the reply has none. now is the current time in seconds since the epoch.
// canonical maps the lowercase form of a model id to the release's spelling. The
// result is always valid. The error explains an outcome that a failure decided.
func interpretZCodeStartPlanBalance(body []byte, dateSeconds, now float64, canonical map[string]string) (zcodeAccountEntitlement, error) {
	var reply zcodeStartPlanBalanceReply
	if err := json.Unmarshal(body, &reply); err != nil {
		return zcodeUnknownEntitlement, fmt.Errorf("parse the billing/balance reply: %w", err)
	}
	data := reply.Data
	if data == nil {
		data = &zcodeStartPlanBalanceData{}
	}
	data.expirePlans(dateSeconds, now)
	if !reply.succeeded() {
		return zcodeCredentialFailedEntitlement, fmt.Errorf("billing/balance reported a failure: success=%s code=%s",
			zcodeRawOrAbsent(reply.Success), zcodeRawOrAbsent(reply.Code))
	}
	if !data.hasActiveStartPlan() {
		return zcodeAccountEntitlement{
			Availability:      zcodeAvailabilityUnavailable,
			UnavailableReason: zcodeUnavailableNotEntitled,
		}, nil
	}
	models := data.modelIDs(canonical)
	if len(models) == 0 {
		if effectiveAt, pending := data.pendingSince(now); pending {
			return zcodeAccountEntitlement{
				Availability: zcodeAvailabilityPending,
				EffectiveAt:  &effectiveAt,
				Models:       []string{},
			}, nil
		}
	}
	// An available plan with no model offers none, as in the desktop: the list is
	// empty, not absent, so the installed rule's list does not take its place.
	return zcodeAccountEntitlement{Availability: zcodeAvailabilityAvailable, Models: models}, nil
}

// succeeded mirrors the desktop's isSuccessfulBusinessEnvelope: success must not
// be false, and code must be absent, 0, or 200.
func (r zcodeStartPlanBalanceReply) succeeded() bool {
	if strings.TrimSpace(string(r.Success)) == "false" {
		return false
	}
	if r.Code == nil {
		return true
	}
	code, ok := zcodeJSONNumberLiteral(r.Code)
	return ok && (code == 0 || code == 200)
}

// expirePlans marks an active plan whose end has passed as expired, and drops each
// balance whose matching plans all expired, as the desktop's
// normalizeStartPlanExpiry does. The clock is the reply's Date header, then the
// server's clock, then the local clock.
func (d *zcodeStartPlanBalanceData) expirePlans(dateSeconds, now float64) {
	at := dateSeconds
	if math.IsNaN(at) {
		at = now
		if serverTime, ok := zcodeServerTime(d.ServerTime); ok {
			at = serverTime
		}
	}
	for i := range d.Plans {
		plan := &d.Plans[i]
		end, ok := zcodeJSNumber(plan.EndsAt)
		if zcodeTrimLower(plan.Status) == "active" && ok && !math.IsInf(end, 0) && end > 0 && end <= at {
			plan.Status = "expired"
		}
	}
	if d.Balances == nil {
		return
	}
	kept := make([]zcodeStartPlanBalance, 0, len(d.Balances))
	for _, balance := range d.Balances {
		matched, live := false, false
		for _, plan := range d.Plans {
			if balance.UserPlanID != "" && plan.UserPlanID != "" {
				if plan.UserPlanID != balance.UserPlanID {
					continue
				}
			} else if plan.PlanID != balance.PlanID {
				continue
			}
			matched = true
			if zcodeTrimLower(plan.Status) != "expired" {
				live = true
			}
		}
		if !matched || live {
			kept = append(kept, balance)
		}
	}
	d.Balances = kept
}

// hasActiveStartPlan mirrors the desktop's hasActiveStartPlan: some plan is active,
// and its id or its name identifies a Start Plan, or it states neither.
func (d *zcodeStartPlanBalanceData) hasActiveStartPlan() bool {
	for _, plan := range d.Plans {
		id := zcodeTrimLower(plan.PlanID)
		name := zcodeTrimLower(plan.Name)
		identified := (id == "" && name == "") || zcodeIsStartPlanIdentity(id) || zcodeIsStartPlanIdentity(name)
		if zcodeTrimLower(plan.Status) == "active" && identified {
			return true
		}
	}
	return false
}

func zcodeIsStartPlanIdentity(value string) bool {
	return strings.Contains(value, "start-plan") || strings.Contains(value, "start plan")
}

// modelIDs mirrors the desktop's resolveZaiStartPlanBalanceModelIds: the
// "model:" capabilities of each balance, or its show_name when it states none, in
// the release's spelling, without a duplicate that differs only by case. The
// result is never nil.
func (d *zcodeStartPlanBalanceData) modelIDs(canonical map[string]string) []string {
	models := []string{}
	seen := map[string]bool{}
	for _, balance := range d.Balances {
		var ids []string
		for _, capability := range balance.Capabilities {
			capability = zcodeTrimJSString(capability)
			if len(capability) < len("model:") || !strings.EqualFold(capability[:len("model:")], "model:") {
				continue
			}
			if id := zcodeTrimJSString(capability[len("model:"):]); id != "" {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			ids = []string{balance.ShowName}
		}
		for _, id := range ids {
			id = zcodeTrimJSString(id)
			if spelled, ok := canonical[strings.ToLower(id)]; ok {
				id = spelled
			}
			key := strings.ToLower(id)
			if id == "" || seen[key] {
				continue
			}
			seen[key] = true
			models = append(models, id)
		}
	}
	return models
}

// pendingSince mirrors the desktop's pending test: every active plan's
// entitlements (or its start when it lists none) take effect after the server's
// clock. It returns the earliest of those times.
func (d *zcodeStartPlanBalanceData) pendingSince(now float64) (float64, bool) {
	serverNow := now
	if serverTime, ok := zcodeServerTime(d.ServerTime); ok {
		serverNow = serverTime
	}
	earliest := math.Inf(1)
	count := 0
	for _, plan := range d.Plans {
		// The desktop compares this status without trimming it.
		if strings.ToLower(plan.Status) != "active" {
			continue
		}
		times := []json.RawMessage{plan.StartsAt}
		if len(plan.Entitlements) > 0 {
			times = times[:0]
			for _, entitlement := range plan.Entitlements {
				times = append(times, entitlement.EffectiveAt)
			}
		}
		for _, raw := range times {
			count++
			effective, ok := zcodeOptionalJSNumber(raw)
			if !ok || math.IsInf(effective, 0) || effective <= serverNow {
				return 0, false
			}
			earliest = math.Min(earliest, effective)
		}
	}
	return earliest, count > 0
}

// zcodeTrimLower trims and lowercases a value, as the desktop's
// `value?.trim().toLowerCase()` does for a status, a plan id, and a plan name.
func zcodeTrimLower(value string) string {
	return strings.ToLower(zcodeTrimJSString(value))
}

// zcodeServerTime reads server_time as the desktop does: only a finite,
// non-negative JSON number counts.
func zcodeServerTime(raw json.RawMessage) (float64, bool) {
	value, ok := zcodeJSONNumberLiteral(raw)
	if !ok || math.IsInf(value, 0) || value < 0 {
		return 0, false
	}
	return value, true
}

// zcodeRawOrAbsent renders a raw JSON value for a log line.
func zcodeRawOrAbsent(raw json.RawMessage) string {
	if raw == nil {
		return "absent"
	}
	return string(raw)
}

// zcodeStartPlanBalanceURL builds the billing URL on the origin of a Start Plan
// provider's base URL. It refuses a base URL that is not https or that has no host,
// so the credential never travels in clear text or to a host that the provider
// does not state. It adds the application version as the desktop does, when
// LeapMux knows it.
func zcodeStartPlanBalanceURL(baseURL, version string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", errors.New("the Start Plan provider's base URL is not a valid URL")
	}
	if base.Scheme != "https" || base.Host == "" {
		return "", fmt.Errorf("the Start Plan provider's base URL %q is not an https URL with a host", base.Redacted())
	}
	balance := url.URL{Scheme: base.Scheme, Host: base.Host, Path: zcodeStartPlanBalancePath}
	if version != "" && version != zcodeUnknownAppVersion {
		balance.RawQuery = url.Values{"app_version": {version}}.Encode()
	}
	return balance.String(), nil
}

// newZCodeStartPlanHTTPClient returns the client that sends the billing request. It
// refuses to follow a redirect, so the credential reaches only the origin that the
// provider states, and a 3xx reply reads as a failure. The request inherits the
// proxy settings of the Worker process, not those of the user's shell profile.
func newZCodeStartPlanHTTPClient(transport http.RoundTripper) *http.Client {
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// zcodeStartPlanHTTPClient is the client that production agents use.
var zcodeStartPlanHTTPClient = newZCodeStartPlanHTTPClient(nil)

// zcodeNodeOS spells an operating system as Node.js's process.platform does.
func zcodeNodeOS(goos string) string {
	switch goos {
	case "windows":
		return "win32"
	case "solaris", "illumos":
		return "sunos"
	}
	return goos
}

// zcodeNodePlatform spells a platform as Node.js does (process.platform and
// process.arch), which is the X-Platform value that the ZCode desktop sends.
func zcodeNodePlatform(goos, goarch string) string {
	switch goarch {
	case "amd64":
		goarch = "x64"
	case "386":
		goarch = "ia32"
	case "mipsle":
		goarch = "mipsel"
	case "ppc64le":
		goarch = "ppc64"
	}
	return zcodeNodeOS(goos) + "-" + goarch
}

// zcodeClientIdentity is how a billing request identifies its client, as the
// desktop identifies itself.
type zcodeClientIdentity struct {
	// appVersion is the version of the installed ZCode application.
	appVersion string
	// deviceMid is the desktop's device id of this machine (loadZCodeDeviceMid).
	deviceMid string
}

// setZCodeStartPlanHeaders sets the identifying headers that the desktop sends with
// the billing request. The desktop also sends headers that the billing API does not
// require, such as its time zone and release channel, and LeapMux leaves those out.
// The API refuses a request without X-Device-Mid or X-Client-Language with HTTP 400
// "parameter error".
func setZCodeStartPlanHeaders(header http.Header, credential string, identity zcodeClientIdentity) {
	header.Set("Authorization", "Bearer "+credential)
	header.Set("User-Agent", "ZCode/"+identity.appVersion)
	header.Set("X-ZCode-App-Version", identity.appVersion)
	header.Set("X-Title", "Z Code@electron")
	header.Set("X-Platform", zcodeNodePlatform(runtime.GOOS, runtime.GOARCH))
	header.Set("X-Device-Mid", identity.deviceMid)
	header.Set("X-Client-Language", "en-US")
}

// zcodeBalanceReply is a billing/balance reply that several checks can read.
type zcodeBalanceReply struct {
	status int
	// body is the reply, or an excerpt of a failed one.
	body        []byte
	dateSeconds float64
}

// zcodeBalanceRequestKey identifies one billing request. The credential appears as
// its hash, so the map never holds it.
type zcodeBalanceRequestKey struct {
	client     *http.Client
	url        string
	credential [sha256.Size]byte
	identity   zcodeClientIdentity
}

// zcodeBalanceRequests shares one billing request among the agents of the Worker
// that ask for the same plan at once, as the resume sweep does at Worker boot. The
// desktop shares a request the same way (fetchZaiStartPlanBalanceEnvelope).
var zcodeBalanceRequests zcodeSharedCalls[zcodeBalanceRequestKey, zcodeBalanceReply]

// fetchZCodeStartPlanEntitlement asks ZCode's billing API whether the account holds
// the Start Plan that baseURL serves. The result is always valid: a refused
// credential is unavailable, and any other failure is unknown, as in the desktop's
// classifyAvailabilityError. The error explains an outcome that a failure decided.
//
// The credential is the one that also authorizes the plan's model requests
// (zcodeAccountBinding.startPlanCredential), so the check states what those
// requests meet.
func fetchZCodeStartPlanEntitlement(ctx context.Context, client *http.Client, baseURL, credential string, identity zcodeClientIdentity, canonical map[string]string) (zcodeAccountEntitlement, error) {
	credential = zcodeTrimJSString(credential)
	if credential == "" {
		return zcodeUnknownEntitlement, errors.New("the Start Plan holds no credential")
	}
	balanceURL, err := zcodeStartPlanBalanceURL(baseURL, identity.appVersion)
	if err != nil {
		return zcodeUnknownEntitlement, err
	}
	key := zcodeBalanceRequestKey{client: client, url: balanceURL, credential: sha256.Sum256([]byte(credential)), identity: identity}
	reply, err := zcodeBalanceRequests.do(ctx, key, zcodeStartPlanCheckTimeout, func(ctx context.Context) (zcodeBalanceReply, error) {
		return sendZCodeBalanceRequest(ctx, client, balanceURL, credential, identity)
	})
	if err != nil {
		return zcodeUnknownEntitlement, err
	}
	if reply.status < 200 || reply.status > 299 {
		err := fmt.Errorf("billing/balance returned HTTP %d: %s", reply.status, strings.TrimSpace(string(reply.body)))
		if reply.status == http.StatusUnauthorized || reply.status == http.StatusForbidden {
			return zcodeCredentialFailedEntitlement, err
		}
		return zcodeUnknownEntitlement, err
	}
	return interpretZCodeStartPlanBalance(reply.body, reply.dateSeconds, float64(time.Now().UnixMilli())/1000, canonical)
}

// sendZCodeBalanceRequest sends one billing request and reads its reply.
func sendZCodeBalanceRequest(ctx context.Context, client *http.Client, balanceURL, credential string, identity zcodeClientIdentity) (zcodeBalanceReply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, balanceURL, nil)
	if err != nil {
		return zcodeBalanceReply{}, fmt.Errorf("build the billing/balance request: %w", err)
	}
	setZCodeStartPlanHeaders(req.Header, credential, identity)
	resp, err := client.Do(req)
	if err != nil {
		return zcodeBalanceReply{}, fmt.Errorf("billing/balance request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	reply := zcodeBalanceReply{status: resp.StatusCode, dateSeconds: math.NaN()}
	if date, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
		reply.dateSeconds = float64(date.UnixMilli()) / 1000
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		reply.body, _ = io.ReadAll(io.LimitReader(resp.Body, zcodeStartPlanErrorExcerptBytes))
		return reply, nil
	}
	reply.body, err = io.ReadAll(io.LimitReader(resp.Body, zcodeStartPlanBalanceMaxBytes+1))
	if err != nil {
		return zcodeBalanceReply{}, fmt.Errorf("read the billing/balance reply: %w", err)
	}
	if len(reply.body) > zcodeStartPlanBalanceMaxBytes {
		return zcodeBalanceReply{}, fmt.Errorf("the billing/balance reply exceeds %d bytes", zcodeStartPlanBalanceMaxBytes)
	}
	return reply, nil
}

// zcodeStartPlanTarget is one Start Plan that a check asks about.
type zcodeStartPlanTarget struct {
	legacyID   string
	baseURL    string
	credential string
}

// zcodeStartPlanTargets returns the Start Plans among bindings.
func zcodeStartPlanTargets(bindings []zcodeAccountBinding) []zcodeStartPlanTarget {
	var targets []zcodeStartPlanTarget
	for _, binding := range bindings {
		if binding.rule.isStartPlan() {
			targets = append(targets, zcodeStartPlanTarget{
				legacyID: binding.legacyID, baseURL: binding.baseURL(), credential: binding.startPlanCredential(),
			})
		}
	}
	return targets
}

// zcodeStartPlanCheck is the entitlement check of a set of Start Plans. The first
// check runs while the app-server boots, because it needs nothing from the
// app-server.
type zcodeStartPlanCheck struct {
	done   chan struct{}
	cancel context.CancelFunc
	// results maps a legacy provider id to its entitlement. The goroutines write it
	// before done closes, and wait reads it after.
	results map[string]zcodeAccountEntitlement
}

// zcodeStartPlanChecker sends the checks of one agent.
type zcodeStartPlanChecker struct {
	client    *http.Client
	identity  zcodeClientIdentity
	canonical map[string]string
	agentID   string
}

// start starts one request for each target. The requests run concurrently and
// share one deadline, so the check takes no longer than timeout however many plans
// it checks.
func (c zcodeStartPlanChecker) start(ctx context.Context, targets []zcodeStartPlanTarget, timeout time.Duration) *zcodeStartPlanCheck {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	check := &zcodeStartPlanCheck{
		done:    make(chan struct{}),
		cancel:  cancel,
		results: map[string]zcodeAccountEntitlement{},
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, target := range targets {
		wg.Go(func() {
			entitlement, err := fetchZCodeStartPlanEntitlement(ctx, c.client, target.baseURL, target.credential, c.identity, c.canonical)
			if err != nil {
				slog.Warn("zcode start-plan entitlement check failed",
					"agent_id", c.agentID, "provider", target.legacyID,
					"availability", entitlement.Availability, "error", err)
			} else {
				slog.Debug("zcode start-plan entitlement checked",
					"agent_id", c.agentID, "provider", target.legacyID,
					"availability", entitlement.Availability, "reason", entitlement.UnavailableReason,
					"models", entitlement.Models)
			}
			mu.Lock()
			check.results[target.legacyID] = entitlement
			mu.Unlock()
		})
	}
	go func() {
		wg.Wait()
		cancel()
		close(check.done)
	}()
	return check
}

// wait returns the entitlement of each Start Plan, keyed by its legacy provider id.
// The check's deadline limits the wait.
func (c *zcodeStartPlanCheck) wait() map[string]zcodeAccountEntitlement {
	<-c.done
	return c.results
}

// stop ends the requests that are still running. Their plans then report an
// unknown entitlement.
func (c *zcodeStartPlanCheck) stop() {
	c.cancel()
}
