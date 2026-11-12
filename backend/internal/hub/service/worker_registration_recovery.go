package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"

	"connectrpc.com/connect/v2"
	"github.com/cloudflare/circl/sign/slhdsa"
	"google.golang.org/protobuf/types/known/timestamppb"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
)

// recoveryContextPrefix is the domain separator of the recovery
// signature. The NUL terminator keeps the prefix unambiguous in the
// fixed-length byte sequence that follows it.
const recoveryContextPrefix = "leapmux.worker.registration.recovery.v1\x00"

// recoverySignedMessage builds the exact byte sequence a recovery
// signature covers:
//
//	UTF-8("leapmux.worker.registration.recovery.v1\0")
//	|| hex_decode(attempt_id)   // 16 bytes
//	|| hex_decode(challenge_id) // 16 bytes
//	|| challenge_nonce          // 32 bytes
//	|| SHA256(registration_key) // 32 bytes
//
// Both identities arrive as 32 lowercase hexadecimal characters, which
// validateAttemptIdentity has already checked.
func recoverySignedMessage(attemptID, challengeID string, nonce, keyDigest []byte) []byte {
	message := make([]byte, 0, len(recoveryContextPrefix)+16+16+len(nonce)+len(keyDigest))
	message = append(message, recoveryContextPrefix...)
	message = append(message, mustHexDecode(attemptID)...)
	message = append(message, mustHexDecode(challengeID)...)
	message = append(message, nonce...)
	message = append(message, keyDigest...)
	return message
}

// mustHexDecode decodes an identity validateAttemptIdentity approved.
func mustHexDecode(s string) []byte {
	raw, err := hex.DecodeString(s)
	if err != nil {
		// Unreachable behind validation; the panic identifies a programming
		// error rather than corrupting a refusal.
		panic(fmt.Sprintf("service: registration identity %q failed its earlier validation: %v", s, err))
	}
	return raw
}

// validateAttemptIdentity reports whether id is the 32 lowercase
// hexadecimal characters the durable attempt identity spells.
func validateAttemptIdentity(id string) bool {
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

// registrationKeyDigest states the digest the Hub stores: the SHA-256
// of the registration key, never the key itself.
func registrationKeyDigest(key string) []byte {
	digest := sha256.Sum256([]byte(key))
	return digest[:]
}

// recoveryWorker loads the worker a committed attempt created and
// checks the presented registration key against the stored digest. A
// consumed key alone recovers nothing: the attempt and the digest must
// both match, and the row must still be recoverable.
func (s *WorkerConnectorService) recoveryWorker(ctx context.Context, attemptID, regKey string) (*store.Worker, error) {
	digest := registrationKeyDigest(regKey)
	worker, err := s.store.Workers().GetByRegistrationAttemptID(ctx, attemptID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.Errorf(connect.CodeUnauthenticated,
				"registration attempt unknown or already recovered elsewhere")
		}
		return nil, wrappedConnectError(connect.CodeInternal, err)
	}
	if subtle.ConstantTimeCompare(worker.RegistrationKeyHash, digest) != 1 {
		return nil, connect.Errorf(connect.CodeUnauthenticated,
			"registration attempt unknown or already recovered elsewhere")
	}
	return worker, nil
}

// refuseUnrecoverableWorker rejects a worker that no recovery may turn
// back into an active registration: deleted or deregistering rows, and
// rows whose owner is gone.
func (s *WorkerConnectorService) refuseUnrecoverableWorker(ctx context.Context, worker *store.Worker) error {
	if worker.DeletedAt != nil {
		return connect.Errorf(connect.CodeFailedPrecondition,
			"this worker was deleted and cannot be recovered")
	}
	if worker.DeregisteringAt != nil {
		return connect.Errorf(connect.CodeFailedPrecondition,
			"this worker is being deregistered and cannot be recovered")
	}
	// The owner's liveness is re-read, not trusted from the worker row:
	// a user deleted after the registration committed owns nothing the
	// recovered worker could serve.
	if _, err := s.store.Users().GetByID(ctx, worker.RegisteredBy); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return connect.Errorf(connect.CodeFailedPrecondition,
				"this worker's owner is gone and it cannot be recovered")
		}
		return wrappedConnectError(connect.CodeInternal, err)
	}
	return nil
}

// GetRegistrationChallenge issues a one-time challenge for a committed
// registration attempt. The original registration key travels as the
// bearer and is authenticated through the digest stored on the worker
// row.
func (s *WorkerConnectorService) GetRegistrationChallenge(
	ctx context.Context,
	req *leapmuxv1.GetRegistrationChallengeRequest,
) (*leapmuxv1.GetRegistrationChallengeResponse, error) {
	info := serverCallInfo(ctx)
	regKey, ok := auth.BearerToken(info.RequestHeader().Get("Authorization"))
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, "registration key required")
	}
	attemptID := req.GetAttemptId()
	if !validateAttemptIdentity(attemptID) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			"attempt_id must hold 32 lowercase hexadecimal characters")
	}
	worker, err := s.recoveryWorker(ctx, attemptID, regKey)
	if err != nil {
		return nil, err
	}
	if err := s.refuseUnrecoverableWorker(ctx, worker); err != nil {
		return nil, err
	}

	id, nonce, expiresAt, err := s.challenges.Issue(attemptID)
	if err != nil {
		if errors.Is(err, errChallengeCapacity) {
			return nil, connect.NewError(connect.CodeResourceExhausted,
				"too many outstanding registration challenges; retry shortly")
		}
		return nil, wrappedConnectError(connect.CodeInternal, err)
	}
	return &leapmuxv1.GetRegistrationChallengeResponse{
		ChallengeId: id,
		Nonce:       nonce,
		ExpiresAt:   timestamppb.New(expiresAt),
	}, nil
}

// RecoverRegistration replaces the token of a committed attempt after the caller
// proves possession of the pinned private keys. The Worker keeps its identity.
// It rechecks that the Worker and its owner remain live before issuing a token.
// The final write refuses a deleted or deregistering Worker.
func (s *WorkerConnectorService) RecoverRegistration(
	ctx context.Context,
	req *leapmuxv1.RecoverRegistrationRequest,
) (*leapmuxv1.RegisterResponse, error) {
	info := serverCallInfo(ctx)
	regKey, ok := auth.BearerToken(info.RequestHeader().Get("Authorization"))
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, "registration key required")
	}
	attemptID, challengeID := req.GetAttemptId(), req.GetChallengeId()
	if !validateAttemptIdentity(attemptID) || !validateAttemptIdentity(challengeID) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			"attempt_id and challenge_id must hold 32 lowercase hexadecimal characters")
	}
	if len(req.GetSignature()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, "signature is required")
	}
	worker, err := s.recoveryWorker(ctx, attemptID, regKey)
	if err != nil {
		return nil, err
	}

	// Consume the challenge first: an unknown, foreign, reused, or
	// expired identity refuses without touching the pinned keys.
	nonce, usable := s.challenges.Consume(challengeID, attemptID)
	if !usable {
		return nil, connect.NewError(connect.CodeAborted,
			"the challenge is unknown, expired, or already used; request a fresh one")
	}
	message := recoverySignedMessage(attemptID, challengeID, nonce, registrationKeyDigest(regKey))
	// The ID must be set before the unmarshal: circl's Unmarshal reads
	// the parameter set from it, and a zero value panics. Every worker
	// key LeapMux pins is SHAKE-256f (workerkeys.Validate enforces it).
	pinned := slhdsa.PublicKey{ID: slhdsa.SHAKE_256f}
	if err := pinned.UnmarshalBinary(worker.PublicKeys.Slhdsa); err != nil {
		return nil, wrappedConnectError(connect.CodeInternal, fmt.Errorf("read the worker's pinned SLH-DSA key: %w", err))
	}
	if !slhdsa.Verify(&pinned, slhdsa.NewMessage(message), req.GetSignature(), nil) {
		return nil, connect.NewError(connect.CodeUnauthenticated,
			"the signature does not match the keys this worker registered with")
	}

	// The proof is good; the row must still be recoverable NOW, not when
	// the challenge was issued.
	fresh, err := s.store.Workers().GetByIDIncludeDeleted(ctx, worker.ID)
	if err != nil {
		return nil, wrappedConnectError(connect.CodeInternal, err)
	}
	if err := s.refuseUnrecoverableWorker(ctx, fresh); err != nil {
		return nil, err
	}

	token := id.Generate()
	rows, err := s.store.Workers().RotateAuthToken(ctx, store.RotateWorkerAuthTokenParams{ID: fresh.ID, AuthTokenHash: s.hasher.Hash(token)})
	if err != nil {
		return nil, wrappedConnectError(connect.CodeInternal, err)
	}
	if rows == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "this worker can no longer be recovered")
	}
	return &leapmuxv1.RegisterResponse{
		WorkerId:  fresh.ID,
		AuthToken: token,
	}, nil
}
