package webauthn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	gowebauthn "github.com/go-webauthn/webauthn/webauthn"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
)

const (
	sessionTTL = 5 * time.Minute
	// reauthProofTTL must span the whole step-up window, not one ceremony:
	// the proof is minted at FinishPasskeyReauth (browser prompt one) and is
	// only consumed at the Finish of the management ceremony that follows
	// (browser prompt two). Two interactive OS prompts plus both RPC legs
	// routinely exceed two minutes.
	reauthProofTTL = 10 * time.Minute
)

// SignupDraft is stored in webauthn_sessions.payload_json during passkey signup.
// UserID is allocated at BeginSignUp and reused as the WebAuthn user handle and
// the users row primary key, so assertion userHandle matches on later logins.
type SignupDraft struct {
	UserID      string `json:"userId"`
	Username    string `json:"username"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	// RPID records the RP ID the ceremony started with so Finish rebuilds
	// the same relying-party parameters.
	RPID string `json:"rpId,omitempty"`
}

// ceremonyMeta is stored in webauthn_sessions.payload_json for the
// login, register, and reauth kinds so Finish rebuilds the RP ID the
// ceremony started with.
type ceremonyMeta struct {
	RPID string `json:"rpId"`
}

// Service orchestrates WebAuthn ceremonies with encrypted session and credential storage.
type Service struct {
	w     *gowebauthn.WebAuthn
	rp    RPConfig
	store store.Store
	ks    *keystore.Keystore
}

// NewService constructs a WebAuthn service from RP config and collaborators.
func NewService(rp RPConfig, st store.Store, ks *keystore.Keystore) (*Service, error) {
	if ks == nil {
		return nil, fmt.Errorf("webauthn service requires keystore")
	}
	cfg := &gowebauthn.Config{
		RPID:          rp.RPID,
		RPDisplayName: rp.RPDisplayName,
		RPOrigins:     rp.RPOrigins,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			UserVerification: protocol.VerificationRequired,
		},
	}
	w, err := gowebauthn.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("create webauthn: %w", err)
	}
	return &Service{w: w, rp: rp, store: st, ks: ks}, nil
}

// ceremonyWebAuthn returns the go-webauthn instance for a ceremony RP ID.
// A ceremony bound to a different allowed origin (for example 127.0.0.1
// instead of localhost) needs the RP ID of that origin in both its options
// and its finish-time validation, because the browser hashes the RP ID into
// the assertion.
func (s *Service) ceremonyWebAuthn(rpID string) (*gowebauthn.WebAuthn, error) {
	if rpID == "" || rpID == s.rp.RPID {
		return s.w, nil
	}
	cfg := *s.w.Config
	cfg.RPID = rpID
	w, err := gowebauthn.New(&cfg)
	if err != nil {
		return nil, fmt.Errorf("create webauthn for rp id %q: %w", rpID, err)
	}
	return w, nil
}

// BeginSignUp starts a passkey-only signup ceremony. origin is the browser
// origin of the request; it must be one the hub allows.
func (s *Service) BeginSignUp(ctx context.Context, draft SignupDraft, origin string) (sessionID string, optionsJSON string, rpID string, err error) {
	if draft.UserID == "" {
		draft.UserID = id.Generate()
	}
	draft.RPID, err = s.ceremonyRPIDForOrigin(origin)
	if err != nil {
		return "", "", "", err
	}
	payload, err := json.Marshal(draft)
	if err != nil {
		return "", "", "", fmt.Errorf("marshal signup draft: %w", err)
	}
	waUser := &user{
		id:          userIDBytes(draft.UserID),
		name:        draft.Username,
		displayName: draft.DisplayName,
	}
	w, err := s.ceremonyWebAuthn(draft.RPID)
	if err != nil {
		return "", "", "", err
	}
	creation, session, err := w.BeginRegistration(waUser)
	if err != nil {
		return "", "", "", fmt.Errorf("begin registration: %w", err)
	}
	sessionID, optionsJSON, err = s.persistSession(ctx, KindSignup, "", string(payload), session, creation)
	return sessionID, optionsJSON, draft.RPID, err
}

// BeginLogin starts a passkey login for an existing user.
func (s *Service) BeginLogin(ctx context.Context, userID, origin string) (sessionID string, optionsJSON string, rpID string, err error) {
	return s.beginAssertion(ctx, KindLogin, userID, origin)
}

// FinishLogin validates a passkey assertion and returns the authenticated
// user (derived from the ceremony session) and the passkey count of the
// loaded credential set.
func (s *Service) FinishLogin(ctx context.Context, sessionID, credentialJSON string) (*store.User, int64, error) {
	return s.validateAssertion(ctx, sessionID, KindLogin, "", credentialJSON)
}

// beginAssertion runs the shared begin flow for login and reauth: resolve
// the ceremony RP ID from the request origin, refuse an account with no
// passkeys, mint assertion options, and persist the per-user ceremony
// session (persistSession replaces any prior open row of the same kind, so
// repeated Begin calls cannot accumulate ceremony rows until TTL cleanup).
func (s *Service) beginAssertion(ctx context.Context, kind, userID, origin string) (sessionID string, optionsJSON string, rpID string, err error) {
	rpID, err = s.ceremonyRPIDForOrigin(origin)
	if err != nil {
		return "", "", "", err
	}
	w, waUser, count, err := s.loadUserWithRPID(ctx, userID, rpID)
	if err != nil {
		return "", "", "", err
	}
	if count == 0 {
		return "", "", "", ErrNoPasskeys
	}
	assertion, session, err := w.BeginLogin(waUser)
	if err != nil {
		return "", "", "", fmt.Errorf("begin assertion: %w", err)
	}
	sessionID, optionsJSON, err = s.persistSession(ctx, kind, userID, ceremonyPayload(rpID), session, assertion)
	return sessionID, optionsJSON, rpID, err
}

// ceremonyRPIDForOrigin resolves the request origin to a ceremony RP ID,
// refusing an origin the hub does not serve before any interactive
// ceremony starts.
func (s *Service) ceremonyRPIDForOrigin(origin string) (string, error) {
	rpID, allowed := s.rp.RPIDForOrigin(origin)
	if !allowed {
		return "", fmt.Errorf("%w: %q", ErrOriginNotAllowed, origin)
	}
	return rpID, nil
}

// validateAssertion runs the assertion pipeline shared by login and reauth:
// consume the ceremony session, bind the session's user (an empty
// expectedUserID derives it from the session row; otherwise the session must
// belong to that user), load the credential set with the ceremony's RP ID,
// parse and validate the assertion, reject clone warnings, and persist the
// sign count. Kind-specific concerns (signup drafts, proof minting) stay
// with the callers.
func (s *Service) validateAssertion(ctx context.Context, sessionID, wantKind, expectedUserID, credentialJSON string) (*store.User, int64, error) {
	row, sessionData, err := s.consumeCeremonySession(ctx, sessionID, wantKind)
	if err != nil {
		return nil, 0, err
	}
	userID := expectedUserID
	if userID == "" {
		if row.UserID == "" {
			return nil, 0, fmt.Errorf("%w: missing user", ErrCeremonyInvalid)
		}
		userID = row.UserID
	} else if row.UserID != expectedUserID {
		return nil, 0, fmt.Errorf("%w: session owner mismatch", ErrCeremonyInvalid)
	}
	if !bytes.Equal(sessionData.UserID, userIDBytes(userID)) {
		return nil, 0, fmt.Errorf("%w: user mismatch", ErrCeremonyInvalid)
	}
	w, stored, count, err := s.loadUserWithRPID(ctx, userID, ceremonyRPID(row.PayloadJSON))
	if err != nil {
		return nil, 0, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(strings.NewReader(credentialJSON))
	if err != nil {
		return nil, 0, fmt.Errorf("%w: parse: %w", ErrAssertionRejected, err)
	}
	cred, err := w.ValidateLogin(stored, *sessionData, parsed)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %w", ErrAssertionRejected, err)
	}
	if err := RejectIfCloneWarning(cred); err != nil {
		return nil, 0, err
	}
	if err := s.applySignCountUpdate(ctx, cred.ID, userID, int64(cred.Authenticator.SignCount), time.Now().UTC()); err != nil {
		return nil, 0, err
	}
	return stored.source, count, nil
}

// FinishedSignUpCredential is the credential material a finish call returns
// for the caller to commit inside its own transaction.
type FinishedSignUpCredential struct {
	CredentialID   []byte
	PublicKey      []byte
	SignCount      int64
	AAGUID         []byte
	BackupEligible bool
	BackupState    bool
	Transports     string
}

// FinishSignUp validates attestation and returns the signup draft plus credential data.
func (s *Service) FinishSignUp(ctx context.Context, sessionID, credentialJSON string) (SignupDraft, FinishedSignUpCredential, error) {
	var zero FinishedSignUpCredential
	row, sessionData, err := s.consumeCeremonySession(ctx, sessionID, KindSignup)
	if err != nil {
		return SignupDraft{}, zero, err
	}
	plain, err := s.decryptPayloadJSON(sessionID, row.PayloadJSON)
	if err != nil {
		return SignupDraft{}, zero, err
	}
	draft, err := ParseSignupDraft(plain)
	if err != nil {
		return SignupDraft{}, zero, fmt.Errorf("parse signup draft: %w", err)
	}
	if draft.UserID == "" {
		return SignupDraft{}, zero, fmt.Errorf("signup draft missing user id")
	}
	waUser := &user{
		id:          sessionData.UserID,
		name:        draft.Username,
		displayName: draft.DisplayName,
	}
	if !bytes.Equal(waUser.id, userIDBytes(draft.UserID)) {
		return SignupDraft{}, zero, fmt.Errorf("signup draft user id mismatch")
	}
	w, err := s.ceremonyWebAuthn(draft.RPID)
	if err != nil {
		return SignupDraft{}, zero, err
	}
	finished, err := verifyAttestation(w, waUser, *sessionData, credentialJSON)
	if err != nil {
		return SignupDraft{}, zero, err
	}
	return draft, finished, nil
}

// BeginRegistration starts adding a passkey to an authenticated account.
func (s *Service) BeginRegistration(ctx context.Context, userID, origin string) (sessionID string, optionsJSON string, rpID string, err error) {
	rpID, err = s.ceremonyRPIDForOrigin(origin)
	if err != nil {
		return "", "", "", err
	}
	w, waUser, _, err := s.loadUserWithRPID(ctx, userID, rpID)
	if err != nil {
		return "", "", "", err
	}
	creation, session, err := w.BeginRegistration(waUser,
		gowebauthn.WithExclusions(gowebauthn.Credentials(waUser.credentials).CredentialDescriptors()),
	)
	if err != nil {
		return "", "", "", fmt.Errorf("begin registration: %w", err)
	}
	sessionID, optionsJSON, err = s.persistSession(ctx, KindRegister, userID, ceremonyPayload(rpID), session, creation)
	return sessionID, optionsJSON, rpID, err
}

// VerifyRegistration consumes the ceremony session and validates the
// attestation, WITHOUT writing the credential. It is the half that needs no
// transaction, so a caller that must write inside one can keep this work
// outside it.
//
// That split is not cosmetic on SQLite, which has a single writer lock:
// this runs a keystore decrypt per existing credential plus a JSON, base64,
// and CBOR parse of a caller-supplied body capped only at the 4 MiB request
// limit. Holding the writer lock for all of that queues every other write
// on the hub behind one registration. The ceremony consume is its own
// atomic conditional UPDATE, so it does not need to share the caller's
// transaction either -- only the credential INSERT does.
//
// FinishSignUp and CompletePasswordReset already order their expensive work
// this way; this is the same shape for the registration path.
func (s *Service) VerifyRegistration(ctx context.Context, userID, sessionID, credentialJSON string) (FinishedSignUpCredential, error) {
	row, sessionData, err := s.consumeCeremonySession(ctx, sessionID, KindRegister)
	if err != nil {
		return FinishedSignUpCredential{}, err
	}
	if row.UserID != userID {
		return FinishedSignUpCredential{}, fmt.Errorf("%w: owner mismatch", ErrCeremonyInvalid)
	}
	w, waUser, _, err := s.loadUserWithRPID(ctx, userID, ceremonyRPID(row.PayloadJSON))
	if err != nil {
		return FinishedSignUpCredential{}, err
	}
	return verifyAttestation(w, waUser, *sessionData, credentialJSON)
}

// BeginReauth starts a passkey assertion for step-up authentication.
func (s *Service) BeginReauth(ctx context.Context, userID, origin string) (sessionID string, optionsJSON string, rpID string, err error) {
	return s.beginAssertion(ctx, KindReauth, userID, origin)
}

// FinishReauth validates a passkey assertion and mints a single-use reauth proof.
func (s *Service) FinishReauth(ctx context.Context, userID, sessionID, credentialJSON string) (string, error) {
	if _, _, err := s.validateAssertion(ctx, sessionID, KindReauth, userID, credentialJSON); err != nil {
		return "", err
	}
	now := time.Now().UTC()

	proofID := id.Generate()
	proofSession := &gowebauthn.SessionData{UserID: userIDBytes(userID)}
	enc, err := s.encryptSessionData(proofID, proofSession)
	if err != nil {
		return "", err
	}
	if err := s.store.WebAuthnSessions().Create(ctx, store.CreateWebAuthnSessionParams{
		ID:          proofID,
		Kind:        KindReauthProof,
		UserID:      userID,
		PayloadJSON: "{}",
		SessionData: enc,
		ExpiresAt:   now.Add(reauthProofTTL),
		CreatedAt:   now,
	}); err != nil {
		return "", err
	}
	return proofID, nil
}

// StoreCredential encrypts and persists a passkey credential row through the
// given store, so a caller that commits inside a transaction writes the row
// in that same transaction. It returns the persisted row, built from the
// same values it wrote, so the credential-to-row mapping has one home.
func (s *Service) StoreCredential(ctx context.Context, st store.Store, rowID, userID string, cred FinishedSignUpCredential, friendlyName string) (*store.PasskeyCredential, error) {
	// The default lives at the WRITE, not at one caller: every path that
	// stores a credential gets it, and a row with an empty name renders as
	// a blank entry in the settings list.
	if friendlyName == "" {
		friendlyName = "Passkey"
	}
	encKey, keyVersion, err := s.encryptPublicKey(rowID, cred.PublicKey)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := st.PasskeyCredentials().Create(ctx, store.CreatePasskeyCredentialParams{
		ID:             rowID,
		UserID:         userID,
		CredentialID:   cred.CredentialID,
		PublicKey:      encKey,
		SignCount:      cred.SignCount,
		AAGUID:         cred.AAGUID,
		BackupEligible: cred.BackupEligible,
		BackupState:    cred.BackupState,
		Transports:     cred.Transports,
		FriendlyName:   friendlyName,
		KeyVersion:     keyVersion,
		CreatedAt:      now,
	}); err != nil {
		return nil, err
	}
	return &store.PasskeyCredential{
		ID:             rowID,
		UserID:         userID,
		CredentialID:   cred.CredentialID,
		SignCount:      cred.SignCount,
		AAGUID:         cred.AAGUID,
		BackupEligible: cred.BackupEligible,
		BackupState:    cred.BackupState,
		Transports:     cred.Transports,
		FriendlyName:   friendlyName,
		KeyVersion:     keyVersion,
		CreatedAt:      now,
	}, nil
}

// verifyAttestation parses an attestation response, validates it against the
// ceremony session, and assembles the finished credential material shared by
// signup and registration finishes.
func verifyAttestation(w *gowebauthn.WebAuthn, waUser *user, sessionData gowebauthn.SessionData, credentialJSON string) (FinishedSignUpCredential, error) {
	parsed, err := protocol.ParseCredentialCreationResponseBody(strings.NewReader(credentialJSON))
	if err != nil {
		return FinishedSignUpCredential{}, fmt.Errorf("%w: parse: %w", ErrAssertionRejected, err)
	}
	cred, err := w.CreateCredential(waUser, sessionData, parsed)
	if err != nil {
		return FinishedSignUpCredential{}, fmt.Errorf("%w: %w", ErrAssertionRejected, err)
	}
	transports, err := json.Marshal(cred.Transport)
	if err != nil {
		return FinishedSignUpCredential{}, fmt.Errorf("marshal transports: %w", err)
	}
	return FinishedSignUpCredential{
		CredentialID:   cred.ID,
		PublicKey:      cred.PublicKey,
		SignCount:      int64(cred.Authenticator.SignCount),
		AAGUID:         cred.Authenticator.AAGUID,
		BackupEligible: cred.Flags.BackupEligible,
		BackupState:    cred.Flags.BackupState,
		Transports:     string(transports),
	}, nil
}

func (s *Service) persistSession(ctx context.Context, kind, userID, payload string, session *gowebauthn.SessionData, options any) (sessionID, optionsJSON string, err error) {
	// Login, register, and reauth ceremonies are per-user. Replace any
	// prior open row of the same kind so repeated Begin calls cannot
	// accumulate encrypted sessions until TTL cleanup. Signup has no
	// users FK yet (the account does not exist), so it cannot replace
	// by user; captcha on BeginSignUp limits anonymous accumulation.
	if userID != "" {
		switch kind {
		case KindLogin, KindRegister, KindReauth:
			if err := s.store.WebAuthnSessions().DeleteByUserAndKind(ctx, userID, kind); err != nil {
				return "", "", fmt.Errorf("clear prior %s ceremony: %w", kind, err)
			}
		}
	}
	sessionID = id.Generate()
	enc, err := s.encryptSessionData(sessionID, session)
	if err != nil {
		return "", "", err
	}
	payloadJSON := payload
	if kind == KindSignup {
		encPayload, encErr := s.encryptPayloadJSON(sessionID, payload)
		if encErr != nil {
			return "", "", encErr
		}
		payloadJSON = encPayload
	}
	now := time.Now().UTC()
	if err := s.store.WebAuthnSessions().Create(ctx, store.CreateWebAuthnSessionParams{
		ID:          sessionID,
		Kind:        kind,
		UserID:      userID,
		PayloadJSON: payloadJSON,
		SessionData: enc,
		ExpiresAt:   now.Add(sessionTTL),
		CreatedAt:   now,
	}); err != nil {
		return "", "", fmt.Errorf("store session: %w", err)
	}
	optionsJSON, err = marshalOptions(options)
	if err != nil {
		return "", "", err
	}
	return sessionID, optionsJSON, nil
}

func marshalOptions(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal options: %w", err)
	}
	return string(b), nil
}

// ceremonyRPID extracts the persisted RP ID from a ceremony payload. An
// empty value means the ceremony used the default RP ID.
func ceremonyRPID(payload string) string {
	var meta ceremonyMeta
	if err := json.Unmarshal([]byte(payload), &meta); err != nil {
		return ""
	}
	return meta.RPID
}

func ceremonyPayload(rpID string) string {
	b, err := json.Marshal(ceremonyMeta{RPID: rpID})
	if err != nil {
		return "{}"
	}
	return string(b)
}

// consumeCeremonySession loads a ceremony row then atomically deletes it so a
// second Finish call cannot reuse the same assertion/attestation. The kind
// and expiry rules live in the consume SQL; Go does not re-check them.
func (s *Service) consumeCeremonySession(ctx context.Context, sessionID, wantKind string) (*store.WebAuthnSession, *gowebauthn.SessionData, error) {
	row, err := s.store.WebAuthnSessions().Get(ctx, sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, fmt.Errorf("%w", ErrCeremonyInvalid)
		}
		return nil, nil, fmt.Errorf("load session: %w", err)
	}
	if row.Kind != wantKind {
		return nil, nil, fmt.Errorf("%w: wrong kind", ErrCeremonyInvalid)
	}
	data, err := s.decryptSessionData(sessionID, row.SessionData)
	if err != nil {
		return nil, nil, err
	}
	n, err := s.store.WebAuthnSessions().ConsumeCeremony(ctx, sessionID, wantKind, time.Now().UTC())
	if err != nil {
		return nil, nil, fmt.Errorf("consume ceremony session: %w", err)
	}
	if n == 0 {
		return nil, nil, fmt.Errorf("%w", ErrCeremonyInvalid)
	}
	return row, data, nil
}

// RPID returns the default relying-party ID for this hub.
func (s *Service) RPID() string {
	return s.rp.RPID
}

// loadUserWithRPID loads the user row and its passkey credentials and returns
// the go-webauthn instance for the given ceremony RP ID.
func (s *Service) loadUserWithRPID(ctx context.Context, userID, rpID string) (*gowebauthn.WebAuthn, *user, int64, error) {
	u, err := s.store.Users().GetByID(ctx, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, 0, fmt.Errorf("user not found")
		}
		return nil, nil, 0, fmt.Errorf("load user: %w", err)
	}
	creds, err := s.store.PasskeyCredentials().ListByUser(ctx, userID)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("list passkeys: %w", err)
	}
	waCreds := make([]gowebauthn.Credential, 0, len(creds))
	for _, c := range creds {
		pub, err := s.decryptPublicKey(c.ID, c.PublicKey)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("decrypt passkey %s: %w", c.ID, err)
		}
		var transports []protocol.AuthenticatorTransport
		if c.Transports != "" && c.Transports != "[]" {
			_ = json.Unmarshal([]byte(c.Transports), &transports)
		}
		waCreds = append(waCreds, gowebauthn.Credential{
			ID:        c.CredentialID,
			PublicKey: pub,
			Transport: transports,
			Flags: gowebauthn.CredentialFlags{
				BackupEligible: c.BackupEligible,
				BackupState:    c.BackupState,
			},
			Authenticator: gowebauthn.Authenticator{
				AAGUID:    c.AAGUID,
				SignCount: uint32(c.SignCount),
			},
		})
	}
	waUser := &user{
		id:          userIDBytes(userID),
		name:        u.Username,
		displayName: u.DisplayName,
		credentials: waCreds,
		source:      u,
	}
	w, err := s.ceremonyWebAuthn(rpID)
	if err != nil {
		return nil, nil, 0, err
	}
	return w, waUser, int64(len(creds)), nil
}

// DecryptPublicKey exposes keystore decrypt for tests and reencrypt tooling.
func (s *Service) DecryptPublicKey(credentialRowID string, ciphertext []byte) ([]byte, error) {
	return s.decryptPublicKey(credentialRowID, ciphertext)
}

// EncryptPublicKey exposes keystore encrypt for tests.
func (s *Service) EncryptPublicKey(credentialRowID string, publicKey []byte) ([]byte, int64, error) {
	return s.encryptPublicKey(credentialRowID, publicKey)
}

// DecryptPayloadJSON exposes signup-draft decrypt for tests.
func (s *Service) DecryptPayloadJSON(sessionID, stored string) (string, error) {
	return s.decryptPayloadJSON(sessionID, stored)
}

// ErrCloneDetected is returned when the authenticator sign count indicates a
// possible cloned credential (WebAuthn CloneWarning).
var ErrCloneDetected = errors.New("authenticator sign count indicates a possible clone")

// ErrCeremonyInvalid reports a ceremony session that is missing, expired,
// of the wrong kind, or bound to another user.
var ErrCeremonyInvalid = errors.New("invalid or expired ceremony session")

// ErrAssertionRejected reports a ceremony that ran but whose attestation or
// assertion failed validation.
var ErrAssertionRejected = errors.New("passkey assertion rejected")

// ErrNoPasskeys reports an account with no registered passkeys at Begin
// time, so callers classify it with errors.Is instead of matching text.
var ErrNoPasskeys = errors.New("no passkeys registered")

// ErrOriginNotAllowed reports a browser origin the hub does not serve. The
// ceremony is refused at Begin instead of failing at Finish, after the user
// already completed an interactive prompt.
var ErrOriginNotAllowed = errors.New("origin is not allowed for passkey ceremonies")

// RejectIfCloneWarning refuses an assertion when go-webauthn flagged a
// non-monotonic sign count. The library still returns success and only sets
// CloneWarning; the relying party must fail the ceremony.
func RejectIfCloneWarning(cred *gowebauthn.Credential) error {
	if cred != nil && cred.Authenticator.CloneWarning {
		return ErrCloneDetected
	}
	return nil
}

// applySignCountUpdate writes the authenticator sign count with one
// monotonic conditional update keyed by the credential id the assertion
// carried: the store advances the row only when the assertion count is
// strictly greater (a zero assertion pairs with a stored zero — an
// authenticator that never maintained a counter). Ceremony sessions are
// already consumed before this runs, so a valid strictly-greater assertion
// must never fail on a concurrent advance; the monotonic predicate cannot
// lose that race, and no retry loop is needed.
//
// WebAuthn clone detection: when both the stored count and the assertion
// count are greater than zero, the assertion must be strictly greater.
// An equality or a regression matches no row (0 rows -> ErrNotFound), and
// the re-read below reports it as a clone. A drop to zero from a positive
// stored count is already rejected before this runs
// (RejectIfCloneWarning), per the WebAuthn clone-detection rule.
func (s *Service) applySignCountUpdate(ctx context.Context, credentialID []byte, userID string, newSignCount int64, lastUsedAt time.Time) error {
	err := s.store.PasskeyCredentials().UpdateSignCount(ctx, store.UpdatePasskeySignCountParams{
		CredentialID: credentialID,
		UserID:       userID,
		SignCount:    newSignCount,
		LastUsedAt:   lastUsedAt,
	})
	if err == nil {
		return nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("update sign count: %w", err)
	}
	// No row advanced: the stored count already reached or passed the
	// assertion count. Classify against the committed row.
	fresh, freshErr := s.store.PasskeyCredentials().GetByCredentialID(ctx, credentialID)
	if freshErr != nil {
		return fmt.Errorf("re-read credential after sign count update: %w", freshErr)
	}
	if fresh.UserID != userID {
		return fmt.Errorf("credential owner mismatch")
	}
	if newSignCount > 0 && fresh.SignCount > 0 && newSignCount <= fresh.SignCount {
		return ErrCloneDetected
	}
	return fmt.Errorf("update sign count: row did not advance (stored %d, assertion %d)", fresh.SignCount, newSignCount)
}

// RoundTripSessionData is a test helper for session encrypt/decrypt.
func (s *Service) RoundTripSessionData(sessionID string, data *gowebauthn.SessionData) (*gowebauthn.SessionData, error) {
	enc, err := s.encryptSessionData(sessionID, data)
	if err != nil {
		return nil, err
	}
	return s.decryptSessionData(sessionID, enc)
}

// ParseSignupDraft decodes a signup payload_json blob.
func ParseSignupDraft(payload string) (SignupDraft, error) {
	var draft SignupDraft
	if err := json.Unmarshal([]byte(payload), &draft); err != nil {
		return SignupDraft{}, err
	}
	return draft, nil
}
