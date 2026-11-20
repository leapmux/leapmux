package tunnel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/generated/contracts"
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
// layout: ensure/start/await run on sender goroutines, runTimer on a detached
// watchdog goroutine, resolve / handleRekeyAck / handleRekeyReject on the
// recvLoop goroutine, and the two locks keep those from racing on the shared
// fields.
type rekeyController struct {
	// lastRekeyAt is handshake time until the first successful in-band rekey.
	// Captured via time.Now(); Go embeds a monotonic reading, so Before/Sub/Add
	// for age / hard-ceiling / reject-backoff are immune to NTP wall-clock steps
	// within the process. Guarded by rekeyMu together with rekeyNotBefore.
	lastRekeyAt time.Time
	// rekeyNotBefore suppresses age-only retries after a Reject until this time.
	// Armed by resolveRekey at resolution time (not by the waiting caller) so the
	// backoff is visible to a sender the instant the Reject drains the waiters.
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
	// rekeyMaterial holds the fresh X25519 seed and post-quantum offer produced
	// when a RekeyRequest is sent, consumed when the matching RekeyAck arrives.
	// nil outside an in-flight rekey. Guarded by ch.mu.
	rekeyMaterial *rekeySecrets
	// rekeyDone is closed when the in-flight rekey resolves (Ack/Reject/send
	// error/timeout/channel close) so runRekeyTimer retires the instant the
	// rekey settles instead of waiting out sessionVerifyTimeout. nil when no
	// rekey is in flight. Guarded by ch.mu.
	rekeyDone chan struct{}
	// rekeyReqID is the correlation id of the in-flight RekeyRequest. An Ack or
	// Reject that names a different id belongs to an EARLIER rekey and must be
	// ignored: without this check a stale Ack landing after a later rekey had
	// registered would claim the later rekey's material and derive against the
	// earlier responder's ephemeral, producing keys the peer does not share --
	// i.e. a silently broken channel rather than a clean failure. Guarded by
	// ch.mu.
	rekeyReqID uint64
	// watchdogTimeout bounds an in-flight rekey before runRekeyTimer declares
	// terminal failure. Zero -- the production value -- means
	// sessionVerifyTimeout. It is a seam so the two tests that need the
	// watchdog to actually FIRE can spend a couple of seconds on the real
	// clock instead of the full 10s; what they pin is the watchdog's behaviour
	// at its deadline, which is the same at any deadline. Immutable after
	// construction, so it needs neither lock.
	watchdogTimeout time.Duration
	// watchdogLive counts runRekeyTimer goroutines that have started and not
	// yet returned. It lets a test prove the watchdog RETIRED on resolution
	// directly, instead of inferring it from the channel still being open a
	// whole timeout later -- an inference that costs one full watchdog window
	// of wall clock per run and can only ever observe a leak after the fact.
	watchdogLive atomic.Int64
	// workerMlkemPub is the worker's static ML-KEM-1024 encapsulation key,
	// retained from GetWorkerHandshakeParams so an in-band rekey can encapsulate
	// a fresh PQ ciphertext without a second round trip. nil on classical-mode
	// channels; its presence is the single source of truth for "is this a PQ
	// channel" (OfferRekeyPQ returns an empty offer for len==0). Immutable after
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
	// surfaces as an error to the waiting sender, and — for every cause that
	// leaves the transport unusable — cancels the channel first, via
	// cancelAndResolveRekeyTerminal. A peer Reject (accepted == false,
	// err == nil) arms the backoff in resolveRekey and returns success to the
	// caller. That (accepted, err) pair is how resolveRekey tells a Reject from
	// every other resolution path, so no other path may construct it.
	err error
}

// rekeySecrets holds the initiator's owned X25519 seed and post-quantum offer
// for one in-flight rekey. The responder's public values arrive on the Ack.
type rekeySecrets struct {
	ephemeralSeed []byte
	pq            *noiseutil.RekeyPQOffer
}

// zero wipes both halves of the in-flight rekey material. Called by every
// resolution path through claimRekeyResolution so the fresh DH/ML-KEM secrets
// do not linger past the rekey they were generated for.
func (rm *rekeySecrets) zero() {
	if rm == nil {
		return
	}
	noiseutil.Zero(rm.ephemeralSeed)
	rm.pq.Zero()
}

// request prepares the public message before the attempt owns the secrets.
func (rm *rekeySecrets) request() (*leapmuxv1.InnerMessage, error) {
	dhPub, err := noiseutil.X25519PublicFromSeed(rm.ephemeralSeed)
	if err != nil {
		return nil, fmt.Errorf("rekey ephemeral public key: %w", err)
	}
	return &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_RekeyRequest{RekeyRequest: &leapmuxv1.RekeyRequest{
			DhPub:             dhPub,
			MlkemStaticCt:     slices.Clone(rm.pq.StaticCiphertext),
			MlkemEphemeralPub: slices.Clone(rm.pq.EphemeralPublicKey),
		}},
	}, nil
}

// ensureRekeyed starts or joins an attempt when age or nonce policy requires
// fresh keys. It releases rekeyMu while waiting so other frames can use the
// current key until the peer acknowledges the rotation.
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
	wait, attempt, send, startErr := ch.startRekeyLocked(ctx)
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
	return ch.awaitRekeyOutcome(wait, attempt)
}

// startRekeyLocked joins an in-flight attempt or prepares a new one. The
// caller holds rekeyMu while deciding and publishing the attempt. It sends
// only after releasing that lock because frame encryption also uses it.
func (ch *Channel) startRekeyLocked(ctx context.Context) (wait chan rekeyOutcome, attempt <-chan struct{}, send func() error, err error) {
	rk := &ch.rekey
	now := time.Now()
	if channelwire.PastHardCeiling(now, rk.lastRekeyAt) {
		ch.cancel()
		return nil, nil, nil, errors.New("session key past hard ceiling")
	}
	soft := ch.session.NeedsRekeyEither()
	if !soft && !rk.rekeyNotBefore.IsZero() && now.Before(rk.rekeyNotBefore) {
		return nil, nil, nil, nil
	}
	if !channelwire.ShouldInitiateRekey(now, rk.lastRekeyAt, soft) {
		return nil, nil, nil, nil
	}

	if cerr := ctx.Err(); cerr != nil {
		return nil, nil, nil, cerr
	}
	if cerr := ch.ctx.Err(); cerr != nil {
		return nil, nil, nil, fmt.Errorf("channel closed: %w", cerr)
	}

	wait = make(chan rekeyOutcome, 1)

	// Fast path under ch.mu: if a rekey is already in flight, join it. The
	// starter owns rekeyMaterial + rekeyDone + the timer; this waiter just
	// shares the outcome resolveRekey will broadcast.
	ch.mu.Lock()
	if len(rk.rekeyWaiters) > 0 {
		rk.rekeyWaiters = append(rk.rekeyWaiters, wait)
		attempt := rk.rekeyDone
		ch.mu.Unlock()
		return wait, attempt, nil, nil
	}
	ch.mu.Unlock()

	// Generate the initiator's fresh ephemeral (as a raw seed the caller owns,
	// so it can be genuinely zeroed — see GenerateEphemeralX25519Seed) and (for
	// PQ channels) encapsulate a fresh ML-KEM shared secret under the worker's
	// static key. Done OUTSIDE ch.mu so concurrent RPC delivery on this channel
	// is not stalled behind the keygen/encapsulate.
	ephemeralSeed, gerr := noiseutil.GenerateEphemeralX25519Seed()
	if gerr != nil {
		return nil, nil, nil, fmt.Errorf("rekey ephemeral: %w", gerr)
	}
	offer, perr := noiseutil.OfferRekeyPQ(rk.workerMlkemPub)
	if perr != nil {
		noiseutil.Zero(ephemeralSeed)
		return nil, nil, nil, fmt.Errorf("rekey ML-KEM offer: %w", perr)
	}

	material := &rekeySecrets{ephemeralSeed: ephemeralSeed, pq: offer}
	req, err := material.request()
	if err != nil {
		material.zero()
		return nil, nil, nil, err
	}

	// The request owns its wire buffers. Once the attempt is published, only
	// its resolver may read or wipe the separate key-agreement material.
	done := make(chan struct{})
	ch.mu.Lock()
	if err := ch.ctx.Err(); err != nil {
		ch.mu.Unlock()
		material.zero()
		return nil, nil, nil, fmt.Errorf("channel closed: %w", err)
	}
	rk.rekeyWaiters = []chan rekeyOutcome{wait}
	rk.rekeyMaterial = material
	rk.rekeyDone = done
	reqID := ch.allocateReqIDLocked()
	rk.rekeyReqID = reqID
	ch.mu.Unlock()

	send = func() error {
		if serr := ch.sendInnerRaw(ctx, reqID, req); serr != nil {
			// Roll back the in-flight state so a later retry isn't blocked, and
			// wake any joiner that slipped in while we held ch.mu. resolveRekey
			// zeroes the material and closes rekeyDone (disarming the timer
			// before it ever starts, if the send fails fast).
			ch.resolveRekey(rekeyOutcome{
				accepted: false,
				err:      fmt.Errorf("send rekey request: %w", serr),
			}, done)
			return fmt.Errorf("send rekey request: %w", serr)
		}
		// The starter also owns the hard-ceiling watchdog; joiners just wait on
		// their own channel and rekeyDone. Start it detached.
		go ch.runRekeyTimer(done)
		return nil
	}
	return wait, done, send, nil
}

// rekeyWatchdogTimeout is how long runRekeyTimer waits for an in-flight rekey
// to resolve before failing it terminally. See rekeyController.watchdogTimeout
// for why it is overridable.
func (ch *Channel) rekeyWatchdogTimeout() time.Duration {
	if ch.rekey.watchdogTimeout > 0 {
		return ch.rekey.watchdogTimeout
	}
	return sessionVerifyTimeout
}

// runRekeyTimer is the starter's watchdog: if the rekey does not resolve
// (Ack/Reject/send-failure) within rekeyWatchdogTimeout (sessionVerifyTimeout
// in production), it fails the rekey terminally and cancels the channel. It
// retires the instant rekeyDone closes
// (i.e. the moment the rekey resolves), so a successful Ack does NOT leave a
// timer armed to fire later and cancel a healthy channel.
//
// It selects on ch.ctx (the channel's lifetime), NEVER on the caller's ctx:
// the Request is already on the wire and the worker may have rotated, so
// tearing down the shared E2EE transport because one CallRPC/Write deadline
// expired would RST every multiplexed Conn riding this channel. The caller's
// ctx is checked only in startRekeyLocked before the Request is sent.
func (ch *Channel) runRekeyTimer(done <-chan struct{}) {
	ch.rekey.watchdogLive.Add(1)
	defer ch.rekey.watchdogLive.Add(-1)

	timer := time.NewTimer(ch.rekeyWatchdogTimeout())
	defer timer.Stop()
	select {
	case <-done:
		// Rekey resolved (Ack/Reject/send-failure on another path): retire.
		return
	case <-timer.C:
		// No resolution in time: terminal failure. The cancel is the
		// terminal-failure consequence the starter owns, and it must be visible
		// BEFORE the error reaches a waiter -- see
		// cancelAndResolveRekeyTerminal, which closes done and broadcasts to
		// every waiter around it.
		ch.cancelAndResolveRekeyTerminal(done, errors.New("rekey timeout"))
	case <-ch.ctx.Done():
		ch.resolveRekey(rekeyOutcome{
			accepted: false,
			err:      fmt.Errorf("channel closed: %w", ch.ctx.Err()),
		}, done)
	}
}

// terminalRekeyOutcome shapes a terminal failure, the one outcome shape that is
// never a peer Reject. err must be non-nil: a nil err is exactly what
// resolveRekey reads as "the peer rejected", which arms a backoff and reports
// success to the waiting sender.
func terminalRekeyOutcome(err error) rekeyOutcome {
	if err == nil {
		err = errors.New("rekey resolved terminally with no error")
	}
	return rekeyOutcome{accepted: false, err: err}
}

// cancelAndResolveRekeyTerminal closes the channel only if this attempt is
// still pending. It cancels before waking waiters so they observe the close.
func (ch *Channel) cancelAndResolveRekeyTerminal(attempt <-chan struct{}, err error) {
	ch.rekey.rekeyMu.Lock()
	claim := ch.claimRekeyResolution(attempt)
	if len(claim.waiters) > 0 {
		ch.cancel()
	}
	ch.rekey.rekeyMu.Unlock()
	ch.broadcastRekeyResolution(claim, terminalRekeyOutcome(err))
}

// resolveRekey settles only the supplied attempt. A Reject updates the
// backoff under rekeyMu before another caller can start a rekey.
func (ch *Channel) resolveRekey(outcome rekeyOutcome, attempt <-chan struct{}) {
	if outcome.accepted || outcome.err != nil {
		ch.broadcastRekeyResolution(ch.claimRekeyResolution(attempt), outcome)
		return
	}
	backoff := outcome.retryAfter
	if backoff <= 0 {
		backoff = contracts.DefaultRejectBackoff
	}
	ch.rekey.rekeyMu.Lock()
	claim := ch.claimRekeyResolution(attempt)
	if len(claim.waiters) > 0 {
		ch.rekey.rekeyNotBefore = time.Now().Add(backoff)
	}
	ch.rekey.rekeyMu.Unlock()
	ch.broadcastRekeyResolution(claim, outcome)
}

// rekeyResolution is what one resolution claimed from the controller: every
// waiter registered at claim time, plus the watchdog's disarm signal. Claiming
// and broadcasting are separate steps only so a terminal failure can cancel the
// channel BETWEEN them — see [Channel.cancelAndResolveRekeyTerminal]. Every claimant broadcasts the outcome to its waiters.
type rekeyResolution struct {
	waiters []chan rekeyOutcome
	done    chan struct{}
}

// claimRekeyResolution takes ownership of the in-flight rekey under ch.mu —
// waiters, material and disarm signal — and wipes the fresh secrets. The claim
// is exclusive: a resolution arriving afterwards (a duplicate Ack, a timer that
// fired alongside a Reject) claims an empty resolution and broadcasts nothing.
//
// A claimant MUST pass the claim to broadcastRekeyResolution. Once a claim
// exists, awaitRekeyOutcome parks on its waiter for the claimant's outcome —
// that is what carries the watchdog's "rekey timeout" past the cancel it
// performs between the two steps — so a claim that never broadcasts strands
// every sender waiting on that rekey.
func (ch *Channel) claimRekeyResolution(attempt <-chan struct{}) rekeyResolution {
	rk := &ch.rekey
	ch.mu.Lock()
	if attempt != rk.rekeyDone {
		ch.mu.Unlock()
		return rekeyResolution{}
	}
	claim := rekeyResolution{waiters: rk.rekeyWaiters, done: rk.rekeyDone}
	rk.rekeyWaiters = nil
	rm := rk.rekeyMaterial
	rk.rekeyMaterial = nil
	rk.rekeyDone = nil
	ch.mu.Unlock()
	rm.zero()
	return claim
}

// broadcastRekeyResolution disarms the watchdog and delivers outcome to every
// waiter the claim captured.
func (ch *Channel) broadcastRekeyResolution(claim rekeyResolution, outcome rekeyOutcome) {
	if claim.done != nil {
		// Closed even when the claim is empty, so a timer that captured this
		// done channel still retires.
		close(claim.done)
	}
	if len(claim.waiters) == 0 {
		// A resolution for a rekey that was never in flight, or that a
		// concurrent resolution already claimed: nothing to broadcast.
		return
	}
	if outcome.err != nil {
		slog.Warn("in-band rekey failed", "channel_id", ch.channelID, "reason", outcome.err)
	}
	for _, w := range claim.waiters {
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
//
// It deliberately takes no caller ctx: the Request is already on the wire, and
// the wait is bounded by the watchdog (sessionVerifyTimeout in production) —
// the worst-case latency CallRPC documents.
func (ch *Channel) awaitRekeyOutcome(wait chan rekeyOutcome, attempt <-chan struct{}) error {
	select {
	case outcome := <-wait:
		return ch.rekeyOutcomeError(outcome)
	case <-ch.ctx.Done():
		// Channel closed while waiting. Resolve the in-flight rekey via the
		// single funnel so the fresh ephemeral seed + ML-KEM shared secret are
		// zeroed and any joiner is woken — covers the narrow window where
		// Close() fires after the Request was sent but before runRekeyTimer was
		// scheduled (otherwise no goroutine watches rekeyDone, and this arm
		// would return without zeroing rekeyMaterial).
		//
		// A no-op if a timer / Ack already claimed the rekey, and the receive
		// below then parks for THAT claimant's outcome rather than reporting the
		// cancel — deliberately. The cancel that woke us is often the claimant's
		// own doing (cancelAndResolveRekeyTerminal cancels between its claim and
		// its broadcast), and "rekey timeout" is the cause this caller needs,
		// not the symptom it produced. Either way the receive is bounded:
		// whoever claims a rekey is obliged to broadcast to every waiter it
		// claimed, and if nobody had claimed it, the resolve above just did and
		// delivered to us.
		ch.resolveRekey(rekeyOutcome{
			accepted: false,
			err:      fmt.Errorf("channel closed: %w", ch.ctx.Err()),
		}, attempt)
		return ch.rekeyOutcomeError(<-wait)
	}
}

// rekeyOutcomeError maps a resolved rekey outcome onto what ensureRekeyed's
// caller sees: nil on accept, the failure on a terminal error, and nil on a
// peer Reject — a Reject is retryable, so the sender proceeds under the current
// key unless that key is now past the hard ceiling.
func (ch *Channel) rekeyOutcomeError(outcome rekeyOutcome) error {
	if outcome.accepted {
		return nil
	}
	// A terminal failure (timeout / send error / Ack derivation failure):
	// surface it as an error rather than a retryable Reject. Where the failure
	// also killed the channel the cancel is already visible here, because
	// cancelAndResolveRekeyTerminal cancels before it broadcasts.
	if outcome.err != nil {
		return outcome.err
	}
	// A peer Reject: resolveRekey already armed rekeyNotBefore under rekeyMu
	// (before draining the waiters, so a concurrent sender cannot slip a
	// redundant Request past the backoff). Only the hard-ceiling consequence
	// is this caller's. lastRekeyAt is read under rekeyMu because
	// handleRekeyAck writes it on the recvLoop goroutine while this runs on a
	// sender goroutine.
	ch.rekey.rekeyMu.Lock()
	pastCeiling := channelwire.PastHardCeiling(time.Now(), ch.rekey.lastRekeyAt)
	ch.rekey.rekeyMu.Unlock()
	if pastCeiling {
		ch.cancel()
		return errors.New("session key past hard ceiling after rekey reject")
	}
	return nil
}

// handleRekeyAck rotates both CipherStates with the fresh X25519 + ML-KEM
// entropy and resolves the in-flight rekey (waking every waiter). The Ack
// supplies the responder's X25519 public key and ephemeral ML-KEM ciphertext.
// Rotation runs under rekeyMu so it
// is serialized against the Encrypt callback in sendInnerRaw; the previous
// receive key is retained for the grace window so worker data frames still in
// flight under the old send key decrypt. The state drain, material zeroing, and
// watchdog disarm all funnel through resolveRekey so a duplicate Ack cannot
// double-rotate and a resolution path cannot leak the timer.
func (ch *Channel) handleRekeyAck(correlationID uint64, ack *leapmuxv1.RekeyAck) {
	rk := &ch.rekey
	// CLAIM ownership of the in-flight material under ch.mu by nilling the slot,
	// not just snapshotting it. runRekeyTimer (separate goroutine) and the
	// awaitRekeyOutcome ctx.Done arm both resolve the rekey, and every
	// resolution re-reads the SAME *rekeySecrets pointer and zeroes
	// rm.ephemeralSeed / rm.pq outside any lock (see
	// claimRekeyResolution). If we left the slot populated, an Ack landing at
	// ~the sessionVerifyTimeout boundary would race our DeriveRekeySecrets read
	// of rm.ephemeralSeed against that rm.zero() write of it — a torn X25519
	// scalar read that derives a key the responder did not, desyncing the
	// channel. With the slot nilled, the claim finds rekeyMaterial==nil and
	// skips the zero; we own the wipe here once derivation is done.
	ch.mu.Lock()
	inFlight := len(rk.rekeyWaiters) > 0 && rk.rekeyReqID == correlationID
	staleFor := rk.rekeyReqID
	attempt := rk.rekeyDone
	var rm *rekeySecrets
	if inFlight {
		// Only claim the material when this Ack belongs to the CURRENT rekey.
		// Claiming it for a stale Ack would both wipe the live rekey's secrets
		// and derive against the wrong responder ephemeral.
		rm = rk.rekeyMaterial
		rk.rekeyMaterial = nil
	}
	ch.mu.Unlock()
	// We claimed rm by nilling the slot, so the resolution on every exit below
	// finds rekeyMaterial==nil and will NOT zero it — wiping rm is ours.
	// A defer guarantees it on every path (bad-responder-pub, derivation
	// failure, success), so the fresh ephemeral seed and post-quantum offer
	// never linger past the Ack they were generated for.
	defer rm.zero()
	if !inFlight {
		slog.Warn("tunnel channel ignored rekey ack that does not match the in-flight request",
			"channel_id", ch.channelID, "ack_correlation_id", correlationID, "in_flight_correlation_id", staleFor)
		return
	}
	if rm == nil || len(ack.GetDhPub()) != noiseutil.EphemeralPublicKeySize {
		// No local material to combine, or the responder's ephemeral is bad:
		// the channel cannot be advanced safely. Fail closed.
		slog.Error("rekey ack missing local material or valid responder ephemeral; closing channel",
			"channel_id", ch.channelID,
			"responder_pub_len", len(ack.GetDhPub()),
		)
		ch.cancelAndResolveRekeyTerminal(attempt,
			errors.New("rekey ack missing local material or valid responder ephemeral"))
		return
	}

	offer := rm.pq
	rm.pq = nil
	pqSecret, err := offer.Accept(ack.GetMlkemEphemeralCt())
	if err != nil {
		slog.Error("rekey ack ML-KEM acceptance failed; closing channel",
			"channel_id", ch.channelID, "error", err)
		ch.cancelAndResolveRekeyTerminal(attempt, err)
		return
	}
	defer noiseutil.Zero(pqSecret)
	dhSecret, pq, err := noiseutil.DeriveRekeySecrets(noiseutil.RekeyMaterial{
		LocalEphemeralPriv: rm.ephemeralSeed,
		PeerEphemeralPub:   ack.GetDhPub(),
		PQSharedSecret:     pqSecret,
	})
	if err != nil {
		slog.Error("rekey ack DH derivation failed; closing channel",
			"channel_id", ch.channelID, "error", err)
		ch.cancelAndResolveRekeyTerminal(attempt, err)
		return
	}

	// Rotate under rekeyMu: serializes against Encrypt in sendInnerRaw and
	// against the rekeyNotBefore / lastRekeyAt updates. Receive retains the
	// previous key for the grace window; Send does not.
	// Each direction needs its own copy of the secrets: rekeyWithSecret zeroes
	// its inputs, and both directions must mix the SAME entropy.
	defer noiseutil.Zero(dhSecret)
	rk.rekeyMu.Lock()
	claim := ch.claimRekeyResolution(attempt)
	if len(claim.waiters) == 0 {
		rk.rekeyMu.Unlock()
		return
	}
	dhForRecv := slices.Clone(dhSecret)
	pqForRecv := slices.Clone(pq)
	ch.session.RekeySendWithSecret(dhSecret, pq)
	ch.session.RekeyReceiveWithSecret(dhForRecv, pqForRecv)
	rk.lastRekeyAt = time.Now()
	rk.rekeyNotBefore = time.Time{}
	rk.rekeyMu.Unlock()

	ch.broadcastRekeyResolution(claim, rekeyOutcome{accepted: true})
}

// handleRekeyReject leaves both CipherStates unchanged and resolves the
// in-flight rekey with the retry-after backoff (a retryable outcome, distinct
// from a terminal failure: err is nil, so resolveRekey arms the backoff and the
// waiting callers see success rather than a dead-channel error).
func (ch *Channel) handleRekeyReject(correlationID uint64, retryAfter time.Duration) {
	rk := &ch.rekey
	ch.mu.Lock()
	inFlight := len(rk.rekeyWaiters) > 0 && rk.rekeyReqID == correlationID
	staleFor := rk.rekeyReqID
	attempt := rk.rekeyDone
	ch.mu.Unlock()
	if !inFlight {
		// Correlation-checked for the same reason as the Ack: a stale Reject
		// would arm a backoff (and drain waiters) against a rekey that is still
		// legitimately in flight.
		slog.Warn("tunnel channel ignored rekey reject that does not match the in-flight request",
			"channel_id", ch.channelID, "reject_correlation_id", correlationID, "in_flight_correlation_id", staleFor)
		return
	}
	ch.resolveRekey(rekeyOutcome{accepted: false, retryAfter: retryAfter}, attempt)
}
