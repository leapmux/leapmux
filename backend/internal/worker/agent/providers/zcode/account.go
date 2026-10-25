package zcode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"
)

// The account plans of a current app-server: LeapMux pushes a host-owned snapshot of
// them (provider/updateAccountConfig), checks the entitlement of each Start Plan,
// pushes the snapshot again when a check that failed succeeds later, and answers
// each model request with the plan's credential.

// zcodeAccountSnapshotInputs is what the first account snapshot is built from.
type zcodeAccountSnapshotInputs struct {
	// basedOn identifies the installed release that the snapshot overlays.
	basedOn   string
	bindings  []zcodeAccountBinding
	canonical map[string]string
	// err reports an installed release that LeapMux could not read. Only the
	// account path needs the release, so only that path reports the error.
	err error
	// startPlans is nil when err is set.
	startPlans *zcodeStartPlanCheck
}

// stop ends a Start Plan check that is still running.
func (in zcodeAccountSnapshotInputs) stop() {
	if in.startPlans != nil {
		in.startPlans.stop()
	}
}

// prepareAccountSnapshot reads the installed release and the credential store,
// binds the account plans, and starts the entitlement check of each Start Plan.
// The check runs only after the release is read, so a start that must fail sends no
// credential over the network.
func (a *Agent) prepareAccountSnapshot(timeout time.Duration) zcodeAccountSnapshotInputs {
	builtin, basedOn, err := loadZCodeBuiltinProviderFile(a.builtinProviderConfigPath)
	if err != nil {
		return zcodeAccountSnapshotInputs{err: err}
	}
	bindings := a.readAccountBindings(builtin.Config.ProviderConfigRules.ProviderRules)
	canonical := builtin.canonicalAccountModelIDs()
	return zcodeAccountSnapshotInputs{
		basedOn:    basedOn,
		bindings:   bindings,
		canonical:  canonical,
		startPlans: a.startPlanChecker(canonical).start(a.Context(), zcodeStartPlanTargets(bindings), min(zcodeStartPlanCheckTimeout, timeout)),
	}
}

// readAccountBindings binds the account plans against the current credential
// store. A store that LeapMux cannot read leaves the bindings on config.json
// alone, and the warning says why.
func (a *Agent) readAccountBindings(rules []zcodeBuiltinProviderRule) []zcodeAccountBinding {
	credentials, err := a.credentials.read()
	if err != nil {
		slog.Warn("zcode credential store unreadable; using config.json credentials only", "agent_id", a.AgentID(), "error", err)
	}
	bindings, err := a.catalog.accountProviderBindings(rules, credentials)
	if err != nil {
		slog.Warn("zcode credential store unreadable; using config.json credentials only", "agent_id", a.AgentID(), "error", err)
	}
	return bindings
}

func (a *Agent) startPlanChecker(canonical map[string]string) zcodeStartPlanChecker {
	return zcodeStartPlanChecker{client: a.startPlanHTTP, identity: a.identity, canonical: canonical, agentID: a.AgentID()}
}

// zcodeAccountState is the account snapshot that the app-server accepted.
type zcodeAccountState struct {
	basedOn   string
	bindings  []zcodeAccountBinding
	canonical map[string]string

	mu sync.Mutex
	// entitlements maps the legacy id of each Start Plan to its entitlement.
	entitlements map[string]zcodeAccountEntitlement
	// pushes counts the snapshots that the app-server accepted.
	pushes int
	// learned counts the entitlements that a re-check learned, and pushedLearned is
	// the count that the last accepted snapshot carries. While they differ, the
	// app-server does not hold every entitlement that LeapMux knows.
	learned       int
	pushedLearned int
}

// binding returns the binding of an account provider id.
func (s *zcodeAccountState) binding(accountID string) (zcodeAccountBinding, bool) {
	for _, binding := range s.bindings {
		if binding.accountID == accountID {
			return binding, true
		}
	}
	return zcodeAccountBinding{}, false
}

// unknownStartPlans returns the Start Plans whose entitlement no check has learned.
func (s *zcodeAccountState) unknownStartPlans() []zcodeAccountBinding {
	s.mu.Lock()
	defer s.mu.Unlock()
	var unknown []zcodeAccountBinding
	for _, binding := range s.bindings {
		if binding.rule.isStartPlan() && s.entitlements[binding.legacyID].Availability == zcodeAvailabilityUnknown {
			unknown = append(unknown, binding)
		}
	}
	return unknown
}

// learn records each result that replaces an unknown entitlement.
func (s *zcodeAccountState) learn(results map[string]zcodeAccountEntitlement) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for legacyID, entitlement := range results {
		if entitlement.Availability != zcodeAvailabilityUnknown && s.entitlements[legacyID].Availability == zcodeAvailabilityUnknown {
			s.entitlements[legacyID] = entitlement
			s.learned++
		}
	}
}

// unpushed reports whether a re-check learned an entitlement that no accepted
// snapshot carries.
func (s *zcodeAccountState) unpushed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.learned != s.pushedLearned
}

// payload builds the next snapshot, and returns the count of learned entitlements
// that it carries. Each push carries a new revision, because the app-server
// ignores a snapshot whose revision it already holds.
func (s *zcodeAccountState) payload(baseRevision string) (zcodeAccountProviderPayload, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	revision := baseRevision
	if s.pushes > 0 {
		revision += "#" + strconv.Itoa(s.pushes)
	}
	return newZCodeAccountProviderPayload(s.bindings, s.basedOn, revision, s.entitlements), s.learned
}

// accepted records that the app-server accepted a snapshot that carries learned
// entitlements.
func (s *zcodeAccountState) accepted(learned int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pushes++
	s.pushedLearned = learned
}

// pushAccountProviderConfig applies the host-owned account snapshot that replaced
// workspace/updateProviderRegistry in ZCode 0.16.9.
func (a *Agent) pushAccountProviderConfig(account zcodeAccountSnapshotInputs, timeout time.Duration) error {
	if account.err != nil {
		return account.err
	}
	results := account.startPlans.wait()
	state := &zcodeAccountState{
		basedOn:      account.basedOn,
		bindings:     account.bindings,
		canonical:    account.canonical,
		entitlements: map[string]zcodeAccountEntitlement{},
	}
	for _, binding := range account.bindings {
		if binding.rule.isStartPlan() {
			entitlement, ok := results[binding.legacyID]
			if !ok {
				entitlement = zcodeUnknownEntitlement
			}
			state.entitlements[binding.legacyID] = entitlement
		}
	}
	if err := a.sendAccountSnapshot(state, timeout); err != nil {
		return err
	}
	// The bridge describes the snapshot that the app-server accepted, so it is
	// recorded only now, together with the flag that every reader checks first.
	a.Mu.Lock()
	a.catalog.recordAccountBridge(account.bindings)
	a.account = state
	a.accountProviderConfig = true
	a.Mu.Unlock()
	if len(state.unknownStartPlans()) > 0 {
		go a.recheckStartPlans(state)
	}
	return nil
}

// sendAccountSnapshot sends the state's next snapshot and checks the
// acknowledgement.
func (a *Agent) sendAccountSnapshot(state *zcodeAccountState, timeout time.Duration) error {
	params, learned := state.payload(a.registryRevision)
	raw, err := a.sendZCodeRequest(MethodUpdateAccountConfig, params, timeout)
	if err != nil {
		return err
	}
	var response struct {
		ReceivedRevision string `json:"receivedRevision"`
		ProviderCount    int    `json:"providerCount"`
		Status           string `json:"status"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return fmt.Errorf("decode ZCode account provider acknowledgement: %w", err)
	}
	if response.ReceivedRevision != params.Revision {
		return fmt.Errorf("ZCode account provider acknowledgement returned revision %q, expected %q",
			response.ReceivedRevision, params.Revision)
	}
	if response.Status != "received" && response.Status != "unchanged" {
		return fmt.Errorf("ZCode account provider acknowledgement returned status %q", response.Status)
	}
	state.accepted(learned)
	slog.Debug("zcode account provider configuration applied", "agent_id", a.AgentID(),
		"revision", params.Revision, "status", response.Status, "providers", response.ProviderCount)
	return nil
}

// zcodeStartPlanRecheckDelays are the waits before each new check of a Start Plan
// whose entitlement no check has learned. A check fails closed, so without a new
// check a timeout at startup would hide the plan's models for the agent's life.
var zcodeStartPlanRecheckDelays = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute}

const zcodeStartPlanRecheckTimerTag = "zcode-start-plan-recheck"

// recheckStartPlans checks each Start Plan with an unknown entitlement again, and
// pushes a new snapshot when a check learns one. The open session then reads the
// new model list. A push that fails is sent again at the next attempt. It ends
// when the app-server holds every entitlement and none is unknown, after the last
// attempt, or when the agent stops.
func (a *Agent) recheckStartPlans(state *zcodeAccountState) {
	checker := a.startPlanChecker(state.canonical)
	for _, delay := range zcodeStartPlanRecheckDelays {
		unknown := state.unknownStartPlans()
		if len(unknown) == 0 && !state.unpushed() {
			return
		}
		timer := a.Clock().NewTimer(delay, zcodeStartPlanRecheckTimerTag)
		select {
		case <-timer.C:
		case <-a.Context().Done():
			timer.Stop(zcodeStartPlanRecheckTimerTag)
			return
		}
		if len(unknown) > 0 {
			a.checkStartPlansAgain(checker, state, unknown)
		}
		if !state.unpushed() {
			continue
		}
		if err := a.sendAccountSnapshot(state, a.APITimeout()); err != nil {
			slog.Warn("zcode account provider update failed", "agent_id", a.AgentID(), "error", err)
			continue
		}
		a.refreshZCodeUsageFromSession()
	}
}

// checkStartPlansAgain checks the unknown Start Plans and records what it learns. A
// new sign-in can change the credential, so it reads the store again.
func (a *Agent) checkStartPlansAgain(checker zcodeStartPlanChecker, state *zcodeAccountState, unknown []zcodeAccountBinding) {
	credentials, err := a.credentials.read()
	if err != nil {
		slog.Warn("zcode credential store unreadable; using config.json credentials only", "agent_id", a.AgentID(), "error", err)
	}
	for i := range unknown {
		if session, _, err := credentials.session(unknown[i].family()); err == nil {
			unknown[i].session = session
		}
	}
	state.learn(checker.start(a.Context(), zcodeStartPlanTargets(unknown), zcodeStartPlanCheckTimeout).wait())
}

// zcodeRuntimeHeadersRequest is the part of interaction/requestProviderRuntimeHeaders
// that LeapMux reads.
type zcodeRuntimeHeadersRequest struct {
	ProviderID string `json:"providerId"`
	// ExpectedAccountScope is the scope of the credential that a model request
	// refused. A credential of another scope must not replace it.
	ExpectedAccountScope string `json:"expectedAccountScope"`
	// RejectedProjectTokenFingerprint is the SHA-256 of a project token that a model
	// request refused, so the next one is minted again.
	RejectedProjectTokenFingerprint string `json:"rejectedProjectTokenFingerprint"`
	// Reason is "usage" for a usage readout, which needs no scope.
	Reason string `json:"reason"`
}

// zcodeRequestAuth is the credential of one model request.
type zcodeRequestAuth struct {
	APIKey       string `json:"apiKey"`
	APIKeyID     string `json:"apiKeyId,omitempty"`
	AccountScope string `json:"accountScope,omitempty"`
}

// errZCodeScopeInvalidated means that the sign-in changed while the request ran.
var errZCodeScopeInvalidated = errors.New("project_token_scope_invalidated")

// accountRequestAuth returns the credential of a model request of an account plan,
// as the desktop's AccountProviderRequestAuthService does: a Start Plan sends the
// store's ZCode token, and a coding plan sends a project token that LeapMux mints
// from the store's OAuth sign-in. Where the store holds no sign-in, the plan's
// config.json key serves.
func (a *Agent) accountRequestAuth(ctx context.Context, req zcodeRuntimeHeadersRequest) (zcodeRequestAuth, error) {
	a.Mu.Lock()
	state := a.account
	a.Mu.Unlock()
	if state == nil {
		return zcodeRequestAuth{}, fmt.Errorf("leapmux holds no credential for provider %q; sign in with ZCode again", req.ProviderID)
	}
	binding, ok := state.binding(req.ProviderID)
	if !ok {
		return zcodeRequestAuth{}, fmt.Errorf("leapmux holds no credential for provider %q; sign in with ZCode again", req.ProviderID)
	}
	credentials, err := a.credentials.read()
	if err != nil {
		slog.Warn("zcode credential store unreadable; using config.json credentials only", "agent_id", a.AgentID(), "error", err)
	}
	session, _, err := credentials.session(binding.family())
	if err != nil {
		slog.Warn("zcode credential store unreadable; using config.json credentials only", "agent_id", a.AgentID(), "error", err)
	}
	binding.session = session

	if binding.rule.isStartPlan() {
		credential := binding.startPlanCredential()
		if credential == "" {
			return zcodeRequestAuth{}, fmt.Errorf("leapmux holds no credential for provider %q; sign in with ZCode again", req.ProviderID)
		}
		return zcodeRequestAuth{APIKey: credential}, nil
	}
	if session.accessToken == "" {
		key := binding.configKey()
		if key == "" {
			return zcodeRequestAuth{}, fmt.Errorf("leapmux holds no credential for provider %q; sign in with ZCode again", req.ProviderID)
		}
		return zcodeRequestAuth{APIKey: key}, nil
	}

	var team *zcodeTeamProject
	if binding.rule.mode() == zcodeTeamCodingPlanMode {
		selected, ok := a.catalog.accountSelection.teams[binding.family()]
		if !ok {
			return zcodeRequestAuth{}, fmt.Errorf("the team coding plan of %q states no team project; select it in the ZCode application", binding.family())
		}
		team = &selected
		// BigModel can leave a stale ZCode token as the OAuth token, which no team
		// project accepts (resolveAccountTeamPlanRuntimeMaterial).
		if binding.family() == zcodeFamilyBigModel && session.zcodeJWT != "" && session.accessToken == session.zcodeJWT {
			return zcodeRequestAuth{}, errors.New("the BigModel sign-in is stale; sign in with the ZCode application again")
		}
	}
	scoped := req.Reason != "usage" || req.ExpectedAccountScope != ""
	scope := ""
	if scoped {
		scope = zcodeAccountScope(binding, team, session)
		if req.ExpectedAccountScope != "" && req.ExpectedAccountScope != scope {
			return zcodeRequestAuth{}, errZCodeScopeInvalidated
		}
	}
	token, err := a.projectTokens.resolve(ctx, credentials, zcodeProjectTokenRequest{
		family:              binding.family(),
		loginToken:          session.accessToken,
		accountID:           session.accountID,
		team:                team,
		rejectedFingerprint: req.RejectedProjectTokenFingerprint,
		appVersion:          a.identity.appVersion,
	})
	if err != nil {
		return zcodeRequestAuth{}, err
	}
	if scoped {
		// The sign-in must not have changed while the token was minted.
		if current, err := a.credentials.read(); err == nil {
			if fresh, _, err := current.session(binding.family()); err == nil && zcodeAccountScope(binding, team, fresh) != scope {
				return zcodeRequestAuth{}, errZCodeScopeInvalidated
			}
		}
	}
	return zcodeRequestAuth{APIKey: token.token, APIKeyID: token.apiKeyID, AccountScope: scope}, nil
}

// zcodeAccountScope identifies the sign-in that a coding plan's credential comes
// from. The app-server sends it back with a refused token, and LeapMux refuses to
// answer that retry from another sign-in.
func zcodeAccountScope(binding zcodeAccountBinding, team *zcodeTeamProject, session zcodeAccountSession) string {
	var teamIDs any
	if team != nil {
		teamIDs = []string{team.organizationID, team.projectID, team.productID}
	}
	var jwt any
	if session.zcodeJWT != "" {
		jwt = session.zcodeJWT
	}
	sum := sha256.Sum256([]byte(zcodeJSONStringify([]any{binding.family(), binding.rule.mode(), teamIDs, session.accessToken, jwt})))
	return hex.EncodeToString(sum[:])
}
