package tunnel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
)

// rekeyController owns the initiator's in-band-rekey state machine for one
// Channel: the fresh key-agreement material, the multi-waiter outcome broadcast,
// the policy fields (lastRekeyAt / rekeyNotBefore), and the watchdog timer's
// disarm signal. It co-locates the two-lock invariant the rekey paths rely on —
// rekeyMu serializes Encrypt against the Ack swap and guards the policy fields;
// ch.mu guards the in-flight waiters / material / done — next to the fields they
// protect, instead of restating it in scattered doc comments on Channel.
//
// All access to the controller goes through Channel methods; the controller does
// not hold a back-reference to the Channel, both to avoid a setup cycle and to
// keep the surface that can touch channel state explicit at each call site (the
// Channel passes itself in). The methods are safe for the Channel's goroutine
// layout: ensure/start/await/runTimer run on sender goroutines, resolve /
// handleRekeyAck / handleRekeyReject run on the recvLoop goroutine, and the two
// locks keep those from racing on the shared fields.
type rekeyController struct {
	// lastRekeyAt is handshake time until the first successful in-band rekey.
	// Captured via time.Now(); Go embeds a monotonic reading, so Before/Sub/Add
	// for age / hard-ceiling / reject-backoff are immune to NTP wall-clock steps
	// within the process. Guarded by rekeyMu together with rekeyNotBefore.
	lastRekeyAt time.Time
	// rekeyNotBefore suppresses age-only retries after a Reject until this time.
	// Guarded by rekeyMu.
	rekeyNotBefore time.Time
	// rekeyMu serializes Encrypt (in sendInnerRaw) against the CipherState swap
	// performed on Ack, and guards lastRekeyAt / rekeyNotBefore. It is held only
	// per-frame around Encrypt and per-resolution around the swap — NOT across
	// the Ack round trip — so a rekey in flight does not block other sends.
	rekeyMu sync.Mutex
	// rekeyWaiters holds one buffered channel per ensureRekeyed caller blocked
	// on the in-flight rekey's outcome. Multiple callers can wait concurrently
	// because rekeyMu is released across the Ack RTT. Guarded by ch.mu.
	rekeyWaiters []chan rekeyOutcome
	// rekeyMaterial holds the fresh ephemeral seed + ML-KEM shared secret
	// produced when a RekeyRequest is sent, consumed when the matching RekeyAck
	// arrives. nil outside an in-flight rekey. Guarded by ch.mu.
	rekeyMaterial *rekeySecrets
	// rekeyDone is closed when the in-flight rekey resolves (Ack/Reject/send
	// error/timeout/channel close) so runRekeyTimer retires the instant the
	// rekey settles instead of waiting out sessionVerifyTimeout. nil when no
	// rekey is in flight. Guarded by ch.mu.
	rekeyDone chan struct{}
	// workerMlkemPub is the worker's static ML-KEM-1024 encapsulation key,
	// retained from GetWorkerHandshakeParams so an in-band rekey can encapsulate
	// a fresh PQ ciphertext without a second round trip. nil on classical-mode
	// channels; its presence is the single source of truth for "is this a PQ
	// channel" (EncapsulateRekeyPQ short-circuits on len==0). Immutable after
	// OpenChannel.
	workerMlkemPub []byte
}

// rekeyOutcome is delivered from recvLoop to the goroutine waiting on an
// in-flight RekeyRequest.
type rekeyOutcome struct {
	accepted   bool
	retryAfter time.Duration // from RekeyReject.retry_after_ms; 0 → DefaultRejectBackoff
	// err is non-nil when the rekey failed terminally (timeout, send error,
	// derivation failure) rather than being peer-rejected. A terminal failure
	// cancels the channel and surfaces as an error to the waiting sender; a peer
	// Reject (err == nil) arms the backoff and returns success to the caller.
	err error
}

// rekeySecrets holds the initiator's fresh key-agreement material for one
// in-flight rekey: the local ephemeral private key (as the raw 32-byte seed the
// caller owns, so it can be genuinely zeroed — see GenerateEphemeralX25519Seed)
// and the ML-KEM shared secret (nil in classic mode). The responder's ephemeral
// public key arrives on the RekeyAck; DeriveRekeySecrets then combines the two
// into the dhSecret both directions rotate on. Zeroed once resolveRekey has
// consumed it.
type rekeySecrets struct {
	ephemeralSeed []byte
	pqShared      []byte
}

// zero wipes both halves of the in-flight rekey material. Called by every
// resolution path through resolveRekey so the fresh DH/ML-KEM secrets do not
// linger past the rekey they were generated for.
func (rm *rekeySecrets) zero() {
	if rm == nil {
		return
	}
	noiseutil.Zero(rm.ephemeralSeed)
	noiseutil.Zero(rm.pqShared)
}

// ensureRekeyed initiates an in-band rekey when age or soft-nonce policy says
// so. Unlike the pre-#321 design it does NOT hold rekeyMu across the
// Request→Ack/Reject RTT: the Request is sent under rekeyMu, then rekeyMu is
// released so other sends continue flowing under the current key (the worker
// accepts them through its grace window). This waits for the outcome so a
// caller observes a hard-ceiling / timeout failure before encrypting. Multiple
// concurrent callers all join the same in-flight rekey and share its outcome.
func (ch *Channel) ensureRekeyed(ctx context.Context) error {
	if ch.session == nil {
		return nil
	}
	// Decide + register the rekey under rekeyMu so two concurrent callers
	// don't both allocate a Request. The Request itself is sent AFTER rekeyMu
	// is released: sendInnerRaw's Encrypt callback also takes rekeyMu (to
	// serialize Encrypt against the Ack swap), so sending under the hold would
	// self-deadlock. The waiter + material are already registered under ch.mu,
	// so an Ack arriving before the send finds them.
	ch.rekey.rekeyMu.Lock()
	wait, _, send, startErr := ch.startRekeyLocked(ctx)
	ch.rekey.rekeyMu.Unlock()
	if startErr != nil {
		return startErr
	}
	if wait == nil {
		return nil // no rekey needed right now, or joined one already completing
	}
	if send != nil {
		// This caller is the starter: put the Request on the wire. A send
		// failure rolls back the in-flight state inside send and returns.
		if err := send(); err != nil {
			return err
		}
	}
	// rekeyMu released: wait for the Ack/Reject/timeout that resolveRekey
	// (or the timer) will deliver to this waiter. Other senders proceed in the
	// meantime, encrypting under the current key.
	return ch.awaitRekeyOutcome(ctx, wait)
}

// startRekeyLocked checks policy and, if a rekey is due, generates the fresh
// ephemeral + ML-KEM material, registers a waiter, stashes the local half for
// handleRekeyAck to combine with the responder's ephemeral, and returns the
// prepared RekeyRequest for the caller to send. Caller holds rekeyMu.
//
// The RekeyRequest is NOT sent here: sendInnerRaw's Encrypt callback also takes
// rekeyMu (to serialize Encrypt against the Ack swap), so sending it under this
// hold would self-deadlock (Go mutexes are not reentrant). The request is sent
// after rekeyMu is released; that is safe because (a) the waiter + material are
// already registered under ch.mu, so an Ack arriving before the send completes
// finds them, and (b) no swap can race this Encrypt — there is no in-flight Ack
// (startRekeyLocked only proceeds when rekeyWaiters is empty).
//
// The X25519 keygen and ML-KEM encapsulation run BEFORE ch.mu is taken: both
// are local-only computations (the ephemeral is fresh random; encapsulation
// keys off the immutable workerMlkemPub) and ch.mu guards per-channel
// bookkeeping (pending RPCs, stream callbacks, reassembly) that must not stall
// behind millisecond-scale crypto. The empty-waiters check (the "join an
// in-flight" decision) is made under ch.mu first, so only the starter pays the
// crypto cost, and the material is registered under ch.mu before the Request
// hits the wire.
//
// Returns (wait, reqID, send, err): when a rekey is in flight the caller should
// await, `send` is non-nil and sends the prepared request once invoked. wait is
// nil (and send nil) if no rekey was needed or this caller joined one already
// started (the returned wait is that rekey's).
func (ch *Channel) startRekeyLocked(ctx context.Context) (wait chan rekeyOutcome, reqID uint64, send func() error, err error) {
	rk := &ch.rekey
	now := time.Now()
	if channelwire.PastHardCeiling(now, rk.lastRekeyAt) {
		ch.cancel()
		return nil, 0, nil, errors.New("session key past hard ceiling")
	}
	soft := ch.session.NeedsRekeyEither()
	if !soft && !rk.rekeyNotBefore.IsZero() && now.Before(rk.rekeyNotBefore) {
		return nil, 0, nil, nil
	}
	if !channelwire.ShouldInitiateRekey(now, rk.lastRekeyAt, soft) {
		return nil, 0, nil, nil
	}

	if cerr := ctx.Err(); cerr != nil {
		return nil, 0, nil, cerr
	}
	if cerr := ch.ctx.Err(); cerr != nil {
		return nil, 0, nil, fmt.Errorf("channel closed: %w", cerr)
	}

	wait = make(chan rekeyOutcome, 1)

	// Fast path under ch.mu: if a rekey is already in flight, join it. The
	// starter owns rekeyMaterial + rekeyDone + the timer; this waiter just
	// shares the outcome resolveRekey will broadcast.
	ch.mu.Lock()
	if len(rk.rekeyWaiters) > 0 {
		rk.rekeyWaiters = append(rk.rekeyWaiters, wait)
		ch.mu.Unlock()
		return wait, 0, nil, nil
	}
	ch.mu.Unlock()

	// Generate the initiator's fresh ephemeral (as a raw seed the caller owns,
	// so it can be genuinely zeroed — see GenerateEphemeralX25519Seed) and (for
	// PQ channels) encapsulate a fresh ML-KEM shared secret under the worker's
	// static key. Done OUTSIDE ch.mu so concurrent RPC delivery on this channel
	// is not stalled behind the keygen/encapsulate.
	ephemeralSeed, gerr := noiseutil.GenerateEphemeralX25519Seed()
	if gerr != nil {
		return nil, 0, nil, fmt.Errorf("rekey ephemeral: %w", gerr)
	}
	pqShared, mlkemCT, perr := noiseutil.EncapsulateRekeyPQ(rk.workerMlkemPub)
	if perr != nil {
		noiseutil.Zero(ephemeralSeed)
		return nil, 0, nil, fmt.Errorf("rekey ML-KEM encapsulate: %w", perr)
	}

	// Register the waiter, material, and the resolution signal under ch.mu. A
	// joiner that arrives after this point but before the Request is sent still
	// finds a non-empty rekeyWaiters and joins; rekeyDone is the timer's disarm
	// signal, closed exactly once by resolveRekey on any resolution path.
	done := make(chan struct{})
	ch.mu.Lock()
	// Re-check under the lock: a concurrent starter could have won the race
	// while we generated material outside the hold. If so, join it and discard
	// our (now-redundant) material.
	if len(rk.rekeyWaiters) > 0 {
		rk.rekeyWaiters = append(rk.rekeyWaiters, wait)
		ch.mu.Unlock()
		noiseutil.Zero(ephemeralSeed)
		noiseutil.Zero(pqShared)
		return wait, 0, nil, nil
	}
	rk.rekeyWaiters = []chan rekeyOutcome{wait}
	rk.rekeyMaterial = &rekeySecrets{ephemeralSeed: ephemeralSeed, pqShared: pqShared}
	rk.rekeyDone = done
	reqID = ch.allocateReqIDLocked()
	ch.mu.Unlock()

	// Capture the prepared request for the caller to send after rekeyMu is
	// released. dhPub is the ephemeral's public point derived from the seed;
	// mlkemCT is nil in classic mode (EncapsulateRekeyPQ returns nil ciphertext
	// on an empty mlkemPub).
	dhPub, perr := noiseutil.X25519PublicFromSeed(ephemeralSeed)
	if perr != nil {
		// The seed came from GenerateEphemeralX25519Seed, so this should not
		// fail; treat it as a terminal rekey failure and clean up.
		ch.resolveRekey(rekeyOutcome{accepted: false, err: fmt.Errorf("rekey ephemeral pubkey: %w", perr)})
		return nil, 0, nil, fmt.Errorf("rekey ephemeral pubkey: %w", perr)
	}
	req := &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_RekeyRequest{RekeyRequest: &leapmuxv1.RekeyRequest{
			DhPub:   dhPub,
			MlkemCt: mlkemCT,
		}},
	}
	send = func() error {
		if serr := ch.sendInnerRaw(ctx, reqID, req); serr != nil {
			// Roll back the in-flight state so a later retry isn't blocked, and
			// wake any joiner that slipped in while we held ch.mu. resolveRekey
			// zeroes the material and closes rekeyDone (disarming the timer
			// before it ever starts, if the send fails fast).
			ch.resolveRekey(rekeyOutcome{
				accepted: false,
				err:      fmt.Errorf("send rekey request: %w", serr),
			})
			return fmt.Errorf("send rekey request: %w", serr)
		}
		// The starter also owns the hard-ceiling watchdog; joiners just wait on
		// their own channel and rekeyDone. Start it detached.
		go ch.runRekeyTimer(done)
		return nil
	}
	return wait, reqID, send, nil
}

// runRekeyTimer is the starter's watchdog: if the rekey does not resolve
// (Ack/Reject/send-failure) within sessionVerifyTimeout, it fails the rekey
// terminally and cancels the channel. It retires the instant rekeyDone closes
// (i.e. the moment the rekey resolves), so a successful Ack does NOT leave a
// timer armed to fire later and cancel a healthy channel.
//
// It selects on ch.ctx (the channel's lifetime), NEVER on the caller's ctx:
// the Request is already on the wire and the worker may have rotated, so
// tearing down the shared E2EE transport because one CallRPC/Write deadline
// expired would RST every multiplexed Conn riding this channel. The caller's
// ctx is checked only in startRekeyLocked before the Request is sent.
func (ch *Channel) runRekeyTimer(done <-chan struct{}) {
	timer := time.NewTimer(sessionVerifyTimeout)
	defer timer.Stop()
	select {
	case <-done:
		// Rekey resolved (Ack/Reject/send-failure on another path): retire.
		return
	case <-timer.C:
		// No resolution in time: terminal failure. resolveRekey broadcasts the
		// error to every waiter and closes done; the channel cancel is the
		// terminal-failure consequence the starter owns.
		ch.resolveRekey(rekeyOutcome{accepted: false, err: errors.New("rekey timeout")})
		ch.cancel()
	case <-ch.ctx.Done():
		ch.resolveRekey(rekeyOutcome{
			accepted: false,
			err:      fmt.Errorf("channel closed: %w", ch.ctx.Err()),
		})
	}
}

// resolveRekey is the single resolution path for an in-flight rekey: it drains
// the waiter list + material under ch.mu, zeroes the fresh secrets, closes
// rekeyDone (disarming the watchdog), and broadcasts the outcome to every
// waiter. handleRekeyAck/handleRekeyReject/runRekeyTimer/the send-failure path
// all funnel through here, so "a resolution path that forgets to disarm the
// timer or zero the material" is mechanically impossible — add a path here,
// not at the call sites. A no-op if no rekey is in flight.
func (ch *Channel) resolveRekey(outcome rekeyOutcome) {
	rk := &ch.rekey
	ch.mu.Lock()
	waiters := rk.rekeyWaiters
	rk.rekeyWaiters = nil
	rm := rk.rekeyMaterial
	rk.rekeyMaterial = nil
	done := rk.rekeyDone
	rk.rekeyDone = nil
	ch.mu.Unlock()
	rm.zero()
	if waiters == nil {
		// A resolution arrived for a rekey that was never in flight, or that a
		// concurrent resolution already drained. Close done (if any) so a timer
		// that captured it still retires, but there is nothing to broadcast.
		if done != nil {
			close(done)
		}
		return
	}
	if done != nil {
		close(done)
	}
	if outcome.err != nil {
		slog.Warn("in-band rekey failed", "channel_id", ch.channelID, "reason", outcome.err)
	}
	for _, w := range waiters {
		select {
		case w <- outcome:
		default:
			slog.Warn("tunnel channel dropped rekey outcome (waiter gone)",
				"channel_id", ch.channelID, "accepted", outcome.accepted)
		}
	}
}

// awaitRekeyOutcome blocks until the in-flight rekey resolves (Ack/Reject from
// handleRekeyAck/handleRekeyReject, a terminal failure via resolveRekey, or
// channel close). Called with rekeyMu NOT held so other sends can proceed.
func (ch *Channel) awaitRekeyOutcome(ctx context.Context, wait chan rekeyOutcome) error {
	select {
	case outcome := <-wait:
		if outcome.accepted {
			return nil
		}
		// A terminal failure (timeout / send error / derivation failure) already
		// cancelled the channel; surface it as an error rather than a retryable
		// Reject.
		if outcome.err != nil {
			return outcome.err
		}
		backoff := outcome.retryAfter
		if backoff <= 0 {
			backoff = channelwire.DefaultRejectBackoff
		}
		// Snapshot rekeyNotBefore and lastRekeyAt under one rekeyMu hold: both
		// fields are documented rekeyMu-guarded, and lastRekeyAt is written by
		// handleRekeyAck on the recvLoop goroutine while this runs on a sender
		// goroutine — reading it without the lock is a data race. Capture `now`
		// once so the backoff anchor and the ceiling evaluation share a single
		// reading (two time.Now() calls could straddle the SessionKeyMaxAge
		// boundary by nanoseconds and arm a backoff the ceiling then rejects).
		now := time.Now()
		ch.rekey.rekeyMu.Lock()
		ch.rekey.rekeyNotBefore = now.Add(backoff)
		pastCeiling := channelwire.PastHardCeiling(now, ch.rekey.lastRekeyAt)
		ch.rekey.rekeyMu.Unlock()
		if pastCeiling {
			ch.cancel()
			return errors.New("session key past hard ceiling after rekey reject")
		}
		return nil
	case <-ch.ctx.Done():
		// Channel closed while waiting. Drain the in-flight rekey via the single
		// funnel so the fresh ephemeral seed + ML-KEM shared secret are zeroed
		// and any joiner is woken — covers the narrow window where Close() fires
		// after the Request was sent but before runRekeyTimer was scheduled
		// (otherwise no goroutine watches rekeyDone, and this arm would return
		// without zeroing rekeyMaterial). resolveRekey is a no-op if a timer /
		// Ack already drained it.
		ch.resolveRekey(rekeyOutcome{
			accepted: false,
			err:      fmt.Errorf("channel closed: %w", ch.ctx.Err()),
		})
		return fmt.Errorf("channel closed: %w", ch.ctx.Err())
	}
}

// handleRekeyAck rotates both CipherStates with the fresh X25519 + ML-KEM
// entropy and resolves the in-flight rekey (waking every waiter). responderPub
// is the responder's ephemeral public key from the Ack, combined with the local
// ephemeral private key stashed at send time. Rotation runs under rekeyMu so it
// is serialized against the Encrypt callback in sendInnerRaw; the previous
// receive key is retained for the grace window so worker data frames still in
// flight under the old send key decrypt. The state drain, material zeroing, and
// watchdog disarm all funnel through resolveRekey so a duplicate Ack cannot
// double-rotate and a resolution path cannot leak the timer.
func (ch *Channel) handleRekeyAck(responderPub []byte) {
	rk := &ch.rekey
	// CLAIM ownership of the in-flight material under ch.mu by nilling the slot,
	// not just snapshotting it. runRekeyTimer (separate goroutine) and the
	// awaitRekeyOutcome ctx.Done arm both call resolveRekey, which re-reads the
	// SAME *rekeySecrets pointer and zeroes rm.ephemeralSeed / rm.pqShared
	// outside any lock. If we left the slot populated, an Ack landing at ~the
	// sessionVerifyTimeout boundary would race our DeriveRekeySecrets read of
	// rm.ephemeralSeed against resolveRekey's zeroBytes write of it — a torn
	// X25519 scalar read that derives a key the responder did not, desyncing
	// the channel. With the slot nilled, resolveRekey finds rekeyMaterial==nil
	// and skips the zero; we own the wipe here once derivation is done.
	ch.mu.Lock()
	inFlight := len(rk.rekeyWaiters) > 0
	rm := rk.rekeyMaterial
	rk.rekeyMaterial = nil
	ch.mu.Unlock()
	// We claimed rm by nilling the slot, so resolveRekey (called on every exit
	// below) finds rekeyMaterial==nil and will NOT zero it — wiping rm is ours.
	// A defer guarantees it on every path (bad-responder-pub, derivation
	// failure, success), so the fresh ephemeral seed + ML-KEM shared secret
	// never linger past the Ack they were generated for.
	defer rm.zero()
	if !inFlight {
		slog.Warn("tunnel channel got rekey ack with no in-flight request",
			"channel_id", ch.channelID)
		return
	}
	if rm == nil || len(responderPub) != noiseutil.EphemeralPublicKeySize {
		// No local material to combine, or the responder's ephemeral is bad:
		// the channel cannot be advanced safely. Fail closed.
		slog.Error("rekey ack missing local material or valid responder ephemeral; closing channel",
			"channel_id", ch.channelID,
			"responder_pub_len", len(responderPub),
		)
		ch.cancel()
		ch.resolveRekey(rekeyOutcome{
			accepted: false,
			err:      fmt.Errorf("rekey ack missing local material or valid responder ephemeral"),
		})
		return
	}

	dhSecret, pq, err := noiseutil.DeriveRekeySecrets(noiseutil.RekeyMaterial{
		LocalEphemeralPriv: rm.ephemeralSeed,
		PeerEphemeralPub:   responderPub,
		PQSharedSecret:     rm.pqShared,
	})
	if err != nil {
		slog.Error("rekey ack DH derivation failed; closing channel",
			"channel_id", ch.channelID, "error", err)
		ch.cancel()
		ch.resolveRekey(rekeyOutcome{accepted: false, err: err})
		return
	}

	// Rotate under rekeyMu: serializes against Encrypt in sendInnerRaw and
	// against the rekeyNotBefore / lastRekeyAt updates. Receive retains the
	// previous key for the grace window; Send does not.
	// Each direction needs its own copy of the secrets: rekeyWithSecret zeroes
	// its inputs, and both directions must mix the SAME entropy.
	dhForRecv := append([]byte(nil), dhSecret...)
	pqForRecv := append([]byte(nil), pq...)
	rk.rekeyMu.Lock()
	ch.session.RekeySendWithSecret(dhSecret, pq)
	ch.session.RekeyReceiveWithSecret(dhForRecv, pqForRecv)
	rk.lastRekeyAt = time.Now()
	rk.rekeyNotBefore = time.Time{}
	rk.rekeyMu.Unlock()

	// resolveRekey drains the waiters and disarms the watchdog. The fresh
	// secrets (rm) were already wiped above (we own them after nilling the
	// slot); resolveRekey finds rekeyMaterial==nil and does not re-zero.
	ch.resolveRekey(rekeyOutcome{accepted: true})
}

// handleRekeyReject leaves both CipherStates unchanged and resolves the
// in-flight rekey with the retry-after backoff (a retryable outcome, distinct
// from a terminal failure: err is nil so callers arm the backoff rather than
// seeing a dead-channel error).
func (ch *Channel) handleRekeyReject(retryAfter time.Duration) {
	rk := &ch.rekey
	ch.mu.Lock()
	inFlight := len(rk.rekeyWaiters) > 0
	ch.mu.Unlock()
	if !inFlight {
		slog.Warn("tunnel channel got rekey reject with no in-flight request",
			"channel_id", ch.channelID)
		return
	}
	ch.resolveRekey(rekeyOutcome{accepted: false, retryAfter: retryAfter})
}
