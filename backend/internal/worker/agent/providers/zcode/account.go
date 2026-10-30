package zcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
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
	rules     []zcodeBuiltinProviderRule
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
		rules:      builtin.Config.ProviderConfigRules.ProviderRules,
		bindings:   bindings,
		canonical:  canonical,
		startPlans: a.startPlanChecker(canonical).start(a.Context(), zcodeStartPlanTargets(bindings), min(zcodeStartPlanCheckTimeout, timeout)),
	}
}

// readAccountBindings binds the account plans against the current credential
// store, reloading selection, coding-plan entitlements, and legacy inline keys
// from account files. A store that LeapMux cannot read leaves the bindings on
// config.json alone, and the warning says why.
func (a *Agent) readAccountBindings(rules []zcodeBuiltinProviderRule) []zcodeAccountBinding {
	selection, selectionErr := loadZCodeAccountSelection(a.homeDir)
	entitlements, entitlementErr := loadZCodeCodingPlanEntitlements(a.homeDir)
	providers, providerErr := loadZCodeRegistryProviders(a.homeDir)
	credentials := a.readAccountCredentials()
	a.Mu.Lock()
	if selectionErr == nil {
		a.catalog.accountSelection = selection
	}
	if entitlementErr == nil {
		a.codingPlanEntitlements = entitlements
	}
	if providerErr == nil || errors.Is(providerErr, errZCodeConfigMissing) {
		a.catalog.Providers = providers
	}
	catalog := a.catalog
	entitlements = a.codingPlanEntitlements
	a.Mu.Unlock()
	if selectionErr != nil && !errors.Is(selectionErr, os.ErrNotExist) {
		slog.Warn("zcode account selection is unreadable; keeping the last valid selection", "agent_id", a.AgentID(), "error", selectionErr)
	}
	if entitlementErr != nil && !errors.Is(entitlementErr, os.ErrNotExist) {
		slog.Warn("zcode coding-plan entitlements are unreadable; keeping the last valid snapshot", "agent_id", a.AgentID(), "error", entitlementErr)
	}
	bindings, err := catalog.accountProviderBindings(rules, credentials, entitlements)
	a.warnCredentialStoreError(err)
	return bindings
}

func (a *Agent) startPlanChecker(canonical map[string]string) zcodeStartPlanChecker {
	return zcodeStartPlanChecker{client: a.startPlanHTTP, identity: a.identity, canonical: canonical, agentID: a.AgentID()}
}

// pushAccountProviderConfig applies the host-owned account snapshot that replaced
// workspace/updateProviderRegistry in ZCode 0.16.9.
func (a *Agent) pushAccountProviderConfig(account zcodeAccountSnapshotInputs, timeout time.Duration) error {
	if account.err != nil {
		return account.err
	}
	results := account.startPlans.wait()
	state := newZCodeAccountState(account, results, a.Clock().Now())
	if err := a.sendAccountSnapshot(state, timeout); err != nil {
		return err
	}
	// Authentication uses account bindings only after the initial snapshot is
	// accepted. sendAccountSnapshot also recorded its model-ID translation.
	a.Mu.Lock()
	a.account = state
	a.Mu.Unlock()
	// The registry refresh comes after the initial account snapshot and before
	// the catalog reads of session/resume: a catalog read against a registry that
	// predates the snapshot would fold models the snapshot changed.
	if err := a.refreshZCodeProviderRegistry(timeout); err != nil {
		return err
	}
	// One refresh owner per accepted account state, regardless of Start Plan rules.
	go a.recheckStartPlans(state)
	return nil
}

// sendAccountSnapshot sends the state's next snapshot and checks the
// acknowledgement.
func (a *Agent) sendAccountSnapshot(state *zcodeAccountState, timeout time.Duration) error {
	params, generation := state.payload(a.registryRevision)
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
	state.accepted(generation, state.bindingsSnapshot())
	bindings := state.bindingsSnapshot()
	a.Mu.Lock()
	a.catalog.recordAccountBridge(bindings)
	a.Mu.Unlock()
	slog.Debug("zcode account provider configuration applied", "agent_id", a.AgentID(),
		"revision", params.Revision, "status", response.Status, "providers", response.ProviderCount)
	return nil
}

// zcodeRuntimeHeadersRequest is the part of interaction/requestProviderRuntimeHeaders
// that LeapMux reads.
type zcodeRuntimeHeadersRequest struct {
	// RequestID is the LOGICAL id of the request, of the form
	// `<session>:provider-runtime-headers:<uuid>`. The JSON-RPC envelope id is
	// only the reply address; a cancellation specifies THIS id.
	RequestID string `json:"requestId"`
	// SessionID is the session the credential serves. A cancellation must specify
	// the same session.
	SessionID string `json:"sessionId"`
	// Workspace identifies the workspace of the request. A cancellation must
	// specify the same workspace.
	Workspace  json.RawMessage `json:"workspace"`
	ProviderID string          `json:"providerId"`
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
// config.json key serves. It uses only accepted bindings and refuses a binding
// whose accepted entitlement is unavailable before minting or discovery.
func (a *Agent) accountRequestAuth(ctx context.Context, req zcodeRuntimeHeadersRequest) (zcodeRequestAuth, error) {
	a.Mu.Lock()
	state := a.account
	a.Mu.Unlock()
	if state == nil {
		return zcodeRequestAuth{}, zcodeMissingCredentialError(req.ProviderID)
	}
	// Capture the accepted generation before discovery or mint.
	state.mu.Lock()
	acceptedGen := state.acceptedGeneration
	state.mu.Unlock()
	binding, ok := state.binding(req.ProviderID)
	if !ok {
		return zcodeRequestAuth{}, zcodeMissingCredentialError(req.ProviderID)
	}
	// Refuse a binding whose accepted entitlement is unavailable.
	if !binding.rule.isStartPlan() && binding.entitlement.Availability == zcodeAvailabilityUnavailable {
		return zcodeRequestAuth{}, fmt.Errorf("the coding plan for provider %q is not available: %s",
			req.ProviderID, binding.entitlement.UnavailableReason)
	}
	credentials := a.readAccountCredentials()
	session, _, err := credentials.session(binding.family())
	a.warnCredentialStoreError(err)
	binding.session = session

	if binding.rule.isStartPlan() {
		return a.startPlanRequestAuth(ctx, state, binding, req.Reason == "usage")
	}
	if session.accessToken == "" {
		if req.ExpectedAccountScope != "" {
			return zcodeRequestAuth{}, errZCodeScopeInvalidated
		}
		key := binding.configKey()
		if key == "" {
			return zcodeRequestAuth{}, zcodeMissingCredentialError(req.ProviderID)
		}
		// Verify the accepted generation did not change.
		if err := a.verifyAcceptedGeneration(state, acceptedGen, binding); err != nil {
			return zcodeRequestAuth{}, err
		}
		return zcodeRequestAuth{APIKey: key}, nil
	}

	var team *zcodeTeamProject
	if binding.rule.mode() == zcodeTeamCodingPlanMode {
		team = binding.team
		if team == nil {
			return zcodeRequestAuth{}, fmt.Errorf("the team coding plan of %q states no team project; select it in the ZCode application", binding.family())
		}
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
		current, err := a.credentials.read()
		if err != nil || !credentials.sameSignIn(current, binding.family(), session) {
			return zcodeRequestAuth{}, errZCodeScopeInvalidated
		}
	}
	// Verify the accepted generation and its binding did not change during mint.
	if err := a.verifyAcceptedGeneration(state, acceptedGen, binding); err != nil {
		return zcodeRequestAuth{}, err
	}
	return zcodeRequestAuth{APIKey: token.token, APIKeyID: token.apiKeyID, AccountScope: scope}, nil
}

// verifyAcceptedGeneration confirms the same generation is still accepted and
// that the binding's team and entitlement are unchanged. A changed accepted
// generation returns errZCodeScopeInvalidated; a merely pending generation does
// not disagree with the app-server's accepted registry.
func (a *Agent) verifyAcceptedGeneration(state *zcodeAccountState, capturedGen int, captured zcodeAccountBinding) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.acceptedGeneration != capturedGen {
		return errZCodeScopeInvalidated
	}
	for _, current := range state.acceptedBindings {
		if current.accountID != captured.accountID {
			continue
		}
		if (current.team == nil) != (captured.team == nil) {
			return errZCodeScopeInvalidated
		}
		if current.team != nil && *current.team != *captured.team {
			return errZCodeScopeInvalidated
		}
		if !zcodeSameEntitlement(current.entitlement, captured.entitlement) {
			return errZCodeScopeInvalidated
		}
		return nil
	}
	return errZCodeScopeInvalidated
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
	return zcodeSHA256Hex(zcodeJSONStringify([]any{binding.family(), binding.rule.mode(), teamIDs, session.accessToken, jwt}))
}

func zcodeMissingCredentialError(providerID string) error {
	return fmt.Errorf("leapmux holds no credential for provider %q; sign in with ZCode again", providerID)
}

func (a *Agent) readAccountCredentials() zcodeCredentials {
	credentials, err := a.credentials.read()
	a.warnCredentialStoreError(err)
	return credentials
}

func (a *Agent) warnCredentialStoreError(err error) {
	if err != nil {
		slog.Warn("zcode credential store contains unreadable material; using the readable credentials", "agent_id", a.AgentID(), "error", err)
	}
}
