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
	// CeremonyTTL is how long one WebAuthn ceremony stays valid, from the
	// Begin that mints its session to the Finish that consumes it. It is
	// exported because a step-up window outside this package must outlast a
	// whole ceremony: a window shorter than one ceremony expires while a
	// ceremony the hub still accepts is on screen, and the user answers the
	// biometric prompt only to have the mutation refuse.
	CeremonyTTL = 5 * time.Minute
)

// The service stores SignupDraft in webauthn_sessions.payload_json during
// passkey signup. BeginSignUp allocates UserID and reuses it as the WebAuthn
// user handle and the users row primary key, so assertion userHandle matches
// on later logins.
type SignupDraft struct {
	UserID      string `json:"userId"`
	Username    string `json:"username"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
}

// The service stores ceremonyMeta in webauthn_sessions.payload_json for the
// login, register, elevation, and recovery kinds. TokenHash is set only on
// recovery: Finish must present the same token Begin charged, so a reminted
// link cannot spend a ceremony minted under the previous token.
//
// It carries no RP ID. The hub has exactly one, so there is nothing for
// Finish to restore -- see RPConfig.AllowsOrigin for why every allowed
// origin resolves to it.
type ceremonyMeta struct {
	TokenHash string `json:"tokenHash,omitempty"`
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
	w, err := newGoWebAuthn(rp)
	if err != nil {
		return nil, fmt.Errorf("create webauthn: %w", err)
	}
	return &Service{w: w, rp: rp, store: st, ks: ks}, nil
}

// newGoWebAuthn builds the hub's one go-webauthn instance from RPConfig, so
// the relying-party parameters have a single home.
//
// It takes the RP ID from rp.RPID and nowhere else. A separate rpID parameter
// beside a struct that already carries one lets a caller pass two values that
// disagree, and the ceremony would then run under one while the rest of the
// parameters described the other.
//
// Building from a fresh literal is also what keeps the library's own
// validation running. gowebauthn.Config carries an unexported `validated`
// flag that New() sets on success, and a struct copy carries that flag with
// it -- so New() on a COPY short-circuits and never checks the RP ID it
// holds. A fresh literal leaves the flag false, which is what rejects an RP
// ID that is not a valid domain string.
func newGoWebAuthn(rp RPConfig) (*gowebauthn.WebAuthn, error) {
	return gowebauthn.New(&gowebauthn.Config{
		RPID:          rp.RPID,
		RPDisplayName: rp.RPDisplayName,
		RPOrigins:     rp.RPOrigins,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			UserVerification: protocol.VerificationRequired,
		},
	})
}

// BeginSignUp starts a passkey-only signup ceremony. origin is the browser
// origin of the request; it must be one the hub allows.
func (s *Service) BeginSignUp(ctx context.Context, draft SignupDraft, origin string) (sessionID string, optionsJSON string, err error) {
	// BeginSignUp allocates the user id HERE, and discards a value the caller
	// put in the draft. It becomes the WebAuthn user handle and then, at Finish,
	// the users row primary key -- so a caller-chosen value chooses a new
	// account's primary key. Overwriting unconditionally makes that mistake
	// impossible instead of merely unmade: today's one caller leaves the
	// field empty, and the next one cannot get it wrong.
	draft.UserID = id.Generate()
	if err = s.CheckOrigin(origin); err != nil {
		return "", "", err
	}
	payload, err := json.Marshal(draft)
	if err != nil {
		return "", "", fmt.Errorf("marshal signup draft: %w", err)
	}
	waUser := &user{
		id:          userIDBytes(draft.UserID),
		name:        draft.Username,
		displayName: draft.DisplayName,
	}
	creation, session, err := s.w.BeginRegistration(waUser)
	if err != nil {
		return "", "", fmt.Errorf("begin registration: %w", err)
	}
	sessionID, optionsJSON, err = s.persistSession(ctx, KindSignup, "", string(payload), session, creation)
	return sessionID, optionsJSON, err
}

// BeginLogin starts a passkey login for an existing user.
func (s *Service) BeginLogin(ctx context.Context, userID, origin string) (sessionID string, optionsJSON string, err error) {
	return s.beginAssertion(ctx, KindLogin, userID, origin)
}

// FinishLogin validates a passkey assertion and returns the authenticated
// user (derived from the ceremony session) and the passkey count of the
// loaded credential set.
func (s *Service) FinishLogin(ctx context.Context, sessionID, credentialJSON string) (*store.User, int64, error) {
	return s.validateAssertion(ctx, sessionID, KindLogin, "", credentialJSON)
}

// beginAssertion runs the shared begin flow for login and elevation: refuse
// an origin the hub does not serve, refuse an account with no passkeys, mint
// assertion options, and persist the per-user ceremony session
// (persistSession replaces any prior open row of the same kind, so repeated
// Begin calls cannot accumulate ceremony rows until TTL cleanup).
func (s *Service) beginAssertion(ctx context.Context, kind, userID, origin string) (sessionID string, optionsJSON string, err error) {
	if err = s.CheckOrigin(origin); err != nil {
		return "", "", err
	}
	waUser, count, err := s.loadUser(ctx, userID)
	if err != nil {
		return "", "", err
	}
	if count == 0 {
		return "", "", ErrNoPasskeys
	}
	assertion, session, err := s.w.BeginLogin(waUser)
	if err != nil {
		return "", "", fmt.Errorf("begin assertion: %w", err)
	}
	sessionID, optionsJSON, err = s.persistSession(ctx, kind, userID, ceremonyPayload(""), session, assertion)
	return sessionID, optionsJSON, err
}

// CheckOrigin refuses an origin the hub does not serve, without starting
// a ceremony or touching the store. Every Begin runs it before any
// interactive work, and recovery Begin runs it before it charges the
// attempt budget.
func (s *Service) CheckOrigin(origin string) error {
	if !s.rp.AllowsOrigin(origin) {
		return fmt.Errorf("%w: %q", ErrOriginNotAllowed, origin)
	}
	return nil
}

// validateAssertion runs the assertion pipeline shared by login and elevation:
// consume the ceremony session, bind the session's user (an empty
// expectedUserID derives it from the session row; otherwise the session must
// belong to that user), load the credential set, parse and validate the
// assertion, reject clone warnings, and persist the sign count.
//
// TWO callers, and each keeps what belongs to its own kind. FinishLogin takes
// the user this returns, and its own caller mints the session. FinishElevation
// mints nothing at all -- the elevation lives on the credential row, which the
// service layer stamps -- so it discards both return values.
//
// A REGISTRATION ceremony never reaches here. It presents an attestation
// rather than an assertion, so both of its paths -- FinishSignUp, which also
// carries the signup draft, and VerifyRegistration -- run verifyAttestation
// instead.
func (s *Service) validateAssertion(ctx context.Context, sessionID, wantKind, expectedUserID, credentialJSON string) (*store.User, int64, error) {
	row, sessionData, err := s.consumeCeremonySession(ctx, sessionID, wantKind, "")
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
	stored, count, err := s.loadUser(ctx, userID)
	if err != nil {
		return nil, 0, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(strings.NewReader(credentialJSON))
	if err != nil {
		return nil, 0, fmt.Errorf("%w: parse: %w", ErrAssertionRejected, err)
	}
	cred, err := s.w.ValidateLogin(stored, *sessionData, parsed)
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
	row, sessionData, err := s.consumeCeremonySession(ctx, sessionID, KindSignup, "")
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
	finished, err := verifyAttestation(s.w, waUser, *sessionData, credentialJSON)
	if err != nil {
		return SignupDraft{}, zero, err
	}
	return draft, finished, nil
}

// BeginRegistration starts adding a passkey to an authenticated account.
func (s *Service) BeginRegistration(ctx context.Context, userID, origin string) (sessionID string, optionsJSON string, err error) {
	return s.beginRegistration(ctx, KindRegister, userID, origin, true, "")
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
// FinishSignUp and CompleteAccountRecoveryPassword already order their expensive work
// this way; this is the same shape for the registration path.
func (s *Service) VerifyRegistration(ctx context.Context, userID, sessionID, credentialJSON string) (FinishedSignUpCredential, error) {
	_, cred, err := s.verifyRegistration(ctx, sessionID, KindRegister, userID, "", credentialJSON)
	return cred, err
}

// BeginRecoveryRegistration starts a passkey registration on an existing
// account WITHOUT a signed-in session: the account-recovery token the
// service layer already validated replaces the session as the ceremony's
// authorization. Unlike BeginRegistration it sends no credential
// exclusions -- the existing passkeys are revoked by the completion this
// ceremony feeds, and their descriptors are not the link-bearer's to read.
// It also loads no existing credentials: those rows are about to be
// deleted, and an undecryptable one must not block the flow that replaces
// them. tokenHash is the hash Begin charged; Finish must present it.
func (s *Service) BeginRecoveryRegistration(ctx context.Context, userID, origin, tokenHash string) (sessionID string, optionsJSON string, err error) {
	if tokenHash == "" {
		return "", "", fmt.Errorf("recovery ceremony requires a token hash")
	}
	return s.beginRegistration(ctx, KindRecovery, userID, origin, false, tokenHash)
}

// VerifyRecoveryRegistration is VerifyRegistration's recovery twin: it
// consumes the KindRecovery ceremony session and validates the attestation
// without writing, so the caller can run the recovery completion and the
// credential INSERT in one transaction. It derives the user from the
// session row (the Begin that minted it resolved the account through the
// recovery token), and returns that user id so the caller's completion can
// target the same row. expectedTokenHash must match the hash Begin stored
// in the session; a reminted link cannot spend a ceremony minted under
// the previous token. A mismatch refuses BEFORE the session is consumed.
func (s *Service) VerifyRecoveryRegistration(ctx context.Context, sessionID, credentialJSON, expectedTokenHash string) (string, FinishedSignUpCredential, error) {
	if expectedTokenHash == "" {
		return "", FinishedSignUpCredential{}, fmt.Errorf("%w: recovery token required", ErrCeremonyInvalid)
	}
	return s.verifyRegistration(ctx, sessionID, KindRecovery, "", expectedTokenHash, credentialJSON)
}

// beginRegistration is the shared begin path for KindRegister and
// KindRecovery. excludeExisting loads the credential list and sends
// WebAuthn exclusions; recovery omits both.
func (s *Service) beginRegistration(ctx context.Context, kind, userID, origin string, excludeExisting bool, tokenHash string) (sessionID, optionsJSON string, err error) {
	if err = s.CheckOrigin(origin); err != nil {
		return "", "", err
	}
	var waUser *user
	if excludeExisting {
		waUser, _, err = s.loadUser(ctx, userID)
	} else {
		waUser, err = s.loadUserIdentity(ctx, userID)
	}
	if err != nil {
		return "", "", err
	}
	var beginOpts []gowebauthn.RegistrationOption
	if excludeExisting {
		beginOpts = append(beginOpts, gowebauthn.WithExclusions(gowebauthn.Credentials(waUser.credentials).CredentialDescriptors()))
	}
	creation, session, err := s.w.BeginRegistration(waUser, beginOpts...)
	if err != nil {
		return "", "", fmt.Errorf("begin registration: %w", err)
	}
	sessionID, optionsJSON, err = s.persistSession(ctx, kind, userID, ceremonyPayload(tokenHash), session, creation)
	return sessionID, optionsJSON, err
}

// verifyRegistration consumes a registration ceremony and validates the
// attestation against an identity-only user (CreateCredential compares
// only WebAuthnID). An empty expectedUserID derives the owner from the
// session row. expectedTokenHash is the recovery bind; empty skips it.
func (s *Service) verifyRegistration(ctx context.Context, sessionID, wantKind, expectedUserID, expectedTokenHash, credentialJSON string) (string, FinishedSignUpCredential, error) {
	row, sessionData, err := s.consumeCeremonySession(ctx, sessionID, wantKind, expectedTokenHash)
	if err != nil {
		return "", FinishedSignUpCredential{}, err
	}
	userID := expectedUserID
	if userID == "" {
		if row.UserID == "" {
			return "", FinishedSignUpCredential{}, fmt.Errorf("%w: missing user", ErrCeremonyInvalid)
		}
		userID = row.UserID
	} else if row.UserID != expectedUserID {
		return "", FinishedSignUpCredential{}, fmt.Errorf("%w: owner mismatch", ErrCeremonyInvalid)
	}
	waUser, err := s.loadUserIdentity(ctx, userID)
	if err != nil {
		return "", FinishedSignUpCredential{}, err
	}
	cred, err := verifyAttestation(s.w, waUser, *sessionData, credentialJSON)
	if err != nil {
		return "", FinishedSignUpCredential{}, err
	}
	return userID, cred, nil
}

// BeginElevation starts a passkey assertion for step-up authentication.
func (s *Service) BeginElevation(ctx context.Context, userID, origin string) (sessionID string, optionsJSON string, err error) {
	return s.beginAssertion(ctx, KindElevation, userID, origin)
}

// FinishElevation validates the step-up assertion. It mints nothing: the
// caller stamps the elevation onto the session row, which is the only place
// the elevation state lives.
//
// A single-use proof returned from here would be a second home for that
// state, with its own TTL, its own replay window and its own consume path,
// and it would say no more than the session row says directly.
func (s *Service) FinishElevation(ctx context.Context, userID, sessionID, credentialJSON string) error {
	_, _, err := s.validateAssertion(ctx, sessionID, KindElevation, userID, credentialJSON)
	return err
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
	// Every PER-USER ceremony replaces any prior open row of the same kind,
	// so repeated Begin calls cannot accumulate encrypted sessions until TTL
	// cleanup. This comment gives the rule as the property rather than as a
	// list of today's kinds: a list is easy to forget when somebody adds the
	// next per-user kind, and the accumulation that follows is invisible until
	// the table grows.
	//
	// Delete-then-Create is two statements. The unique index on (user_id,
	// kind) for a non-NULL user_id is the invariant that two concurrent
	// Begins cannot both insert. When Create returns ErrConflict, one retry
	// deletes the other Begin's row and inserts ours -- the later Begin
	// replaces the earlier one, the same as two sequential Begins.
	//
	// Signup is the one kind that cannot replace by user, because the account
	// does not exist yet and there is no users FK to key on; captcha on
	// BeginSignUp limits anonymous accumulation instead. A blank userID is
	// the same condition seen from the caller's side. Those rows store
	// NULL user_id, so they sit outside the unique index.
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
	params := store.CreateWebAuthnSessionParams{
		ID:          sessionID,
		Kind:        kind,
		UserID:      userID,
		PayloadJSON: payloadJSON,
		SessionData: enc,
		ExpiresAt:   now.Add(CeremonyTTL),
		CreatedAt:   now,
	}
	for attempt := 0; attempt < 2; attempt++ {
		if userID != "" && kind != KindSignup {
			if err := s.store.WebAuthnSessions().DeleteByUserAndKind(ctx, userID, kind); err != nil {
				return "", "", fmt.Errorf("clear prior %s ceremony: %w", kind, err)
			}
		}
		err = s.store.WebAuthnSessions().Create(ctx, params)
		if err == nil {
			break
		}
		if !errors.Is(err, store.ErrConflict) || attempt == 1 {
			return "", "", fmt.Errorf("store session: %w", err)
		}
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

func ceremonyPayload(tokenHash string) string {
	b, err := json.Marshal(ceremonyMeta{TokenHash: tokenHash})
	if err != nil {
		return "{}"
	}
	return string(b)
}

func ceremonyTokenHash(payload string) string {
	var meta ceremonyMeta
	if err := json.Unmarshal([]byte(payload), &meta); err != nil {
		return ""
	}
	return meta.TokenHash
}

// consumeCeremonySession loads a ceremony row then atomically deletes it so a
// second Finish call cannot reuse the same assertion/attestation. The kind
// and expiry rules live in the consume SQL; Go does not re-check them.
func (s *Service) consumeCeremonySession(ctx context.Context, sessionID, wantKind, expectedTokenHash string) (*store.WebAuthnSession, *gowebauthn.SessionData, error) {
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
	if expectedTokenHash != "" {
		stored := ceremonyTokenHash(row.PayloadJSON)
		if stored == "" || stored != expectedTokenHash {
			return nil, nil, fmt.Errorf("%w: recovery token mismatch", ErrCeremonyInvalid)
		}
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

// loadUser loads the user row and its passkey credentials, and reports how
// many credentials it found.
func (s *Service) loadUser(ctx context.Context, userID string) (*user, int64, error) {
	u, err := s.store.Users().GetByID(ctx, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, 0, fmt.Errorf("user not found")
		}
		return nil, 0, fmt.Errorf("load user: %w", err)
	}
	creds, err := s.store.PasskeyCredentials().ListByUser(ctx, userID)
	if err != nil {
		return nil, 0, fmt.Errorf("list passkeys: %w", err)
	}
	waCreds := make([]gowebauthn.Credential, 0, len(creds))
	for _, c := range creds {
		pub, err := s.decryptPublicKey(c.ID, c.PublicKey)
		if err != nil {
			return nil, 0, fmt.Errorf("decrypt passkey %s: %w", c.ID, err)
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
	return waUser, int64(len(creds)), nil
}

// loadUserIdentity loads the user row with no credential list. Recovery
// registration uses it because the existing passkeys are revoked at
// completion and their ciphertext is not needed to mint or verify an
// attestation (CreateCredential compares WebAuthnID only). Authenticated
// registration still uses loadUser so it can send exclusions.
func (s *Service) loadUserIdentity(ctx context.Context, userID string) (*user, error) {
	u, err := s.store.Users().GetByID(ctx, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("load user: %w", err)
	}
	return &user{
		id:          userIDBytes(userID),
		name:        u.Username,
		displayName: u.DisplayName,
		source:      u,
	}, nil
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

// ErrCloneDetected reports an authenticator sign count that indicates a
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
// hub refuses the ceremony at Begin instead of failing at Finish, after the
// user already completed an interactive prompt.
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
// authenticator that never maintained a counter). The caller already
// consumed the ceremony session before this runs, so a valid strictly-greater
// assertion must never fail on a concurrent advance; the monotonic predicate
// cannot lose that race, and this function needs no retry loop.
//
// WebAuthn clone detection: when both the stored count and the assertion
// count are greater than zero, the assertion must be strictly greater.
// An equality or a regression matches no row (0 rows -> ErrNotFound), and
// the re-read below reports it as a clone. RejectIfCloneWarning already
// refuses a drop to zero from a positive stored count before this runs,
// per the WebAuthn clone-detection rule.
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
