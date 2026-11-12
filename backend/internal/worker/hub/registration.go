package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/workerkeys"
)

// RegistrationResult contains the credentials obtained after registration.
type RegistrationResult struct {
	WorkerID  string
	AuthToken string
}

// ErrRegistrationRefused reports a permanent application refusal: the
// configured worker maximum, an invalid or consumed key, malformed
// input, or a recovery proof the Hub rejected. Retrying cannot change
// the answer, so the caller fails at once.
var ErrRegistrationRefused = errors.New("the hub permanently refused this registration")

// RegistrationOptions states what one registration run carries and how
// long it may take.
type RegistrationOptions struct {
	// AttemptID is the durable attempt identity, 32 lowercase hex
	// characters, saved by the caller BEFORE the first network call.
	AttemptID string
	// RegistrationKey is the key the hub UI minted.
	RegistrationKey string
	// Keys is the public key set the Hub pins to the new worker.
	Keys *leapmuxv1.WorkerPublicKeys
	// Signer proves possession of the SLH-DSA private half when a lost
	// response turns the attempt into a recovery. It may be nil when the
	// caller cannot recover (nothing is lost by trying: a hub that never
	// answers AlreadyExists never calls it).
	Signer func(message []byte) ([]byte, error)
	// Deadline limits the WHOLE registration run, retries included.
	// Zero applies DefaultRegistrationDeadline.
	Deadline time.Duration
}

// RegistrationOptionsFromKeypair builds the options from a complete
// keypair: the public halves ride the request, and the SLH-DSA private
// half signs any recovery challenge.
func RegistrationOptionsFromKeypair(attemptID, registrationKey string, ck *noiseutil.CompositeKeypair) (RegistrationOptions, error) {
	keys, err := workerkeys.Of(ck)
	if err != nil {
		return RegistrationOptions{}, fmt.Errorf("derive the worker's public keys: %w", err)
	}
	return RegistrationOptions{
		AttemptID:       attemptID,
		RegistrationKey: registrationKey,
		Keys:            keys,
		Signer: func(message []byte) ([]byte, error) {
			return ck.SlhdsaPrivateKey.Sign(rand.Reader, message, nil)
		},
	}, nil
}

// DefaultRegistrationDeadline limits one registration command: five
// minutes of retries with visible progress, then a failure the operator
// can read.
const DefaultRegistrationDeadline = 5 * time.Minute

// Register presents the registration key as a bearer credential to the
// hub's Register RPC and returns permanent worker credentials. A
// response lost after the hub committed turns into AlreadyExists on the
// retry, and Register then drives the challenge protocol -- a fresh
// challenge, an SLH-DSA signature over it, RecoverRegistration -- to
// return the ORIGINAL credentials, never a second worker.
//
// Application refusals are permanent (ErrRegistrationRefused): the
// worker maximum, an invalid or consumed key, malformed input, or a
// rejected recovery proof. Transport failures retry under the deadline
// with visible progress.
func Register(ctx context.Context, endpoint *hubtransport.Endpoint, opts RegistrationOptions) (*RegistrationResult, error) {
	client := newConnectorClient(endpoint)
	deadline := opts.Deadline
	if deadline == 0 {
		deadline = DefaultRegistrationDeadline
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	return registerWithClient(ctx, client, opts.RegistrationKey,
		registerRetry{
			backoff:        newDefaultBackoff(),
			attemptTimeout: registerAttemptTimeout,
			clock:          quartz.NewReal(),
		}, opts)
}

const registerRetryTimerTag = "hub-register-retry"

// registerRetry is the retry policy one registration run follows.
//
// The three travel as one value rather than as three more parameters on an
// already long list, because they only make sense together: a caller that
// shortens the backoff also wants the clock that makes the wait between
// attempts instant, and the attempt limit is the third dial of the same
// policy.
type registerRetry struct {
	backoff        backoff
	attemptTimeout time.Duration
	// clock supplies the wait between attempts. A test drives it, so a
	// registration ladder costs no wall time and its rungs are exact.
	clock quartz.Clock
}

// registerOutcome states how one full attempt at the protocol ended, so
// the retry loop and the caller agree on what is retriable.
type registerOutcome int

const (
	outcomeIncomplete registerOutcome = iota
	outcomeSuccess
	outcomePermanentRefusal
	outcomeTransient
)

func registerWithClient(
	ctx context.Context,
	client leapmuxv1connect.WorkerConnectorServiceClient,
	registrationKey string,
	retry registerRetry,
	opts RegistrationOptions,
) (*RegistrationResult, error) {
	if registrationKey == "" {
		return nil, fmt.Errorf("%w: a registration key is required", ErrRegistrationRefused)
	}
	if !validAttemptIdentity(opts.AttemptID) {
		return nil, fmt.Errorf("%w: the attempt identity must hold 32 lowercase hexadecimal characters", ErrRegistrationRefused)
	}
	if opts.Keys == nil {
		return nil, fmt.Errorf("%w: a public key set is required", ErrRegistrationRefused)
	}

	// lastFailure is the error of the most recent transient attempt. A run
	// that ends at its deadline reports it, because the deadline alone does
	// not say why five minutes of attempts failed.
	var lastFailure error
	for {
		result, outcome, err := registerProtocolOnce(ctx, client, registrationKey, retry.attemptTimeout, opts)
		switch outcome {
		case outcomeSuccess:
			slog.Info("worker registered", "worker_id", result.WorkerID, "attempt_id", opts.AttemptID)
			return result, nil
		case outcomePermanentRefusal:
			if errors.Is(err, ErrRegistrationRefused) {
				return nil, err
			}
			return nil, fmt.Errorf("%w: %w", ErrRegistrationRefused, err)
		case outcomeTransient:
			lastFailure = err
		case outcomeIncomplete:
			return nil, withLastFailure(err, lastFailure)
		}

		if ctx.Err() != nil {
			return nil, withLastFailure(ctx.Err(), lastFailure)
		}

		interval := retry.backoff.Next()
		slog.Warn("registration attempt failed, retrying...", "error", err, "backoff", interval, "attempt_id", opts.AttemptID)
		if !waitOrCancel(ctx, retry.clock, interval, registerRetryTimerTag) {
			return nil, withLastFailure(ctx.Err(), lastFailure)
		}
	}
}

// withLastFailure adds the error of the last failed attempt to the error
// that ended the run, such as its deadline. Both stay reachable through
// errors.Is.
func withLastFailure(ended, lastFailure error) error {
	if lastFailure == nil {
		return ended
	}
	return fmt.Errorf("%w; the last attempt failed: %w", ended, lastFailure)
}

// registerProtocolOnce runs ONE pass of the registration protocol: a
// Register, and -- when the hub answers AlreadyExists because this
// attempt already committed -- the challenge recovery that replaces the token
// while keeping the Worker's identity.
func registerProtocolOnce(
	ctx context.Context,
	client leapmuxv1connect.WorkerConnectorServiceClient,
	registrationKey string,
	attemptTimeout time.Duration,
	opts RegistrationOptions,
) (*RegistrationResult, registerOutcome, error) {
	attemptCtx, cancelAttempt := context.WithTimeout(ctx, attemptTimeout)
	defer cancelAttempt()
	req := &leapmuxv1.RegisterRequest{Keys: opts.Keys, AttemptId: opts.AttemptID}
	// The handler authenticates by reading the bearer key from the
	// Authorization header — this is *not* the long-lived auth_token
	// flow, that one is bound to a different RPC. The header is request
	// metadata on the call's CallInfo, so it rides the call context.
	callCtx, info := connect.NewClientContext(attemptCtx)
	info.RequestHeader().Set("Authorization", "Bearer "+registrationKey)

	resp, err := client.Register(callCtx, req)
	if err == nil {
		result, credentialErr := registrationCredentials(resp)
		if credentialErr != nil {
			return nil, outcomePermanentRefusal, credentialErr
		}
		return result, outcomeSuccess, nil
	}
	if ctx.Err() != nil {
		return nil, outcomeIncomplete, ctx.Err()
	}
	if connectErr := new(connect.Error); errors.As(err, &connectErr) {
		switch connectErr.Code() {
		case connect.CodeAlreadyExists:
			// This attempt already committed and its response was lost.
			// Recover the Worker's identity with a replacement token by proving
			// possession of the private keypair the Hub pinned.
			result, recoverErr := recoverRegistration(attemptCtx, client, opts)
			if recoverErr != nil {
				if isPermanentRefusal(recoverErr) {
					return nil, outcomePermanentRefusal, recoverErr
				}
				return nil, outcomeTransient, recoverErr
			}
			return result, outcomeSuccess, nil
		case connect.CodeUnauthenticated, connect.CodeInvalidArgument,
			connect.CodePermissionDenied, connect.CodeResourceExhausted:
			return nil, outcomePermanentRefusal, err
		default:
			return nil, outcomeTransient, err
		}
	}
	return nil, outcomeTransient, err
}

// recoverRegistration drives the challenge half of the protocol: obtain
// a fresh one-time challenge and sign it with the SLH-DSA private key.
func recoverRegistration(
	ctx context.Context,
	client leapmuxv1connect.WorkerConnectorServiceClient,
	opts RegistrationOptions,
) (*RegistrationResult, error) {
	challengeCtx, info := connect.NewClientContext(ctx)
	info.RequestHeader().Set("Authorization", "Bearer "+opts.RegistrationKey)
	challenge, err := client.GetRegistrationChallenge(challengeCtx,
		&leapmuxv1.GetRegistrationChallengeRequest{AttemptId: opts.AttemptID})
	if err != nil {
		return nil, fmt.Errorf("obtain a recovery challenge: %w", err)
	}

	if opts.Signer == nil {
		return nil, fmt.Errorf("the hub committed this attempt but its response was lost, and this caller cannot sign the recovery challenge")
	}
	message := recoveryMessage(opts.AttemptID, challenge.GetChallengeId(), challenge.GetNonce(), opts.RegistrationKey)
	signature, err := opts.Signer(message)
	if err != nil {
		return nil, fmt.Errorf("sign the recovery challenge: %w", err)
	}

	recoverCtx, recoverInfo := connect.NewClientContext(ctx)
	recoverInfo.RequestHeader().Set("Authorization", "Bearer "+opts.RegistrationKey)
	resp, err := client.RecoverRegistration(recoverCtx, &leapmuxv1.RecoverRegistrationRequest{
		AttemptId:   opts.AttemptID,
		ChallengeId: challenge.GetChallengeId(),
		Signature:   signature,
	})
	if err != nil {
		return nil, fmt.Errorf("recover the committed registration: %w", err)
	}
	result, err := registrationCredentials(resp)
	if err != nil {
		return nil, err
	}
	slog.Info("recovered the committed registration", "worker_id", resp.GetWorkerId(), "attempt_id", opts.AttemptID)
	return result, nil
}

// registrationCredentials refuses an unusable success from either protocol path.
func registrationCredentials(response *leapmuxv1.RegisterResponse) (*RegistrationResult, error) {
	if response.GetWorkerId() == "" || response.GetAuthToken() == "" {
		return nil, fmt.Errorf("%w: the hub answered success with incomplete credentials (worker_id %q)",
			ErrRegistrationRefused, response.GetWorkerId())
	}
	return &RegistrationResult{WorkerID: response.GetWorkerId(), AuthToken: response.GetAuthToken()}, nil
}

// isPermanentRefusal reports a recovery refusal that retrying cannot
// change: the attempt is unknown or its digest changed, the row is
// deleted or its owner gone, or the proof itself was rejected.
func isPermanentRefusal(err error) bool {
	if errors.Is(err, ErrRegistrationRefused) {
		return true
	}
	connectErr := new(connect.Error)
	if !errors.As(err, &connectErr) {
		return false
	}
	switch connectErr.Code() {
	case connect.CodeUnauthenticated, connect.CodeInvalidArgument,
		connect.CodePermissionDenied, connect.CodeFailedPrecondition:
		return true
	default:
		return false
	}
}

// validAttemptIdentity mirrors the hub's validation of the attempt id.
func validAttemptIdentity(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// registerAttemptTimeout limits ONE registration attempt.
//
// Each registration attempt needs its own deadline so a stalled request can
// return to the retry loop. The unary client also limits the whole exchange;
// this shorter attempt limit preserves registration's retry policy.
//
// It is generous: registration is one round trip, but it runs while a hub may
// still be starting, and the retry costs a fresh key nothing.
const registerAttemptTimeout = 30 * time.Second

// recoveryMessage builds the exact byte sequence the recovery signature
// covers, mirroring the hub's verifier:
//
//	UTF-8("leapmux.worker.registration.recovery.v1\0")
//	|| hex_decode(attempt_id) || hex_decode(challenge_id)
//	|| challenge_nonce || SHA256(registration_key)
//
// The message is rebuilt here rather than shared with the hub so the
// client proves the format the hub VERIFIES, and a drift between the
// two surfaces fails visibly instead of verifying transitively.
func recoveryMessage(attemptID, challengeID string, nonce []byte, registrationKey string) []byte {
	digest := sha256.Sum256([]byte(registrationKey))
	message := append([]byte{}, "leapmux.worker.registration.recovery.v1\x00"...)
	attemptRaw, _ := hex.DecodeString(attemptID)
	challengeRaw, _ := hex.DecodeString(challengeID)
	message = append(message, attemptRaw...)
	message = append(message, challengeRaw...)
	message = append(message, nonce...)
	return append(message, digest[:]...)
}
