package mail

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/util/windowed"
)

// ErrRecipientRateLimited reports a recipient whose per-window mail budget
// the hub spent. It is a sentinel: callers that surface send failures to a
// user map it to Unavailable like any relay refusal, and the account
// recovery path swallows it with the same uniform response it swallows a
// dead relay with.
var ErrRecipientRateLimited = errors.New("the hub mailed this recipient recently; the per-recipient budget is spent")

// NewRecipientLimitedSender caps how much mail one recipient address can
// receive, whatever account requested it and whatever flow sent it. now is
// the hub clock, injected so a test advances time instead of reaching into
// the budget map.
//
// The per-account cooldowns cannot see the bombing shapes: a signup with
// plus-addresses (victim+1@, victim+2@, ...) mints one fresh account-row
// per mail, and RequestEmailChange sends to any address the caller
// specifies. Both cost one inbox one mail per attempt, so the budget keys
// on the RECIPIENT: the local part drops any +tag (those addresses share
// one inbox on every provider that honors tags) and lowercases, and
// everything else -- domain, exact local part -- stays as delivered. The
// fold is a trade, not a free win: a provider that treats '+' as a literal
// local-part character runs distinct mailboxes that share one local part
// onto one shared budget. Counting exact addresses instead would reopen
// the plus-tag bombing this cap exists to stop, so the hub folds.
//
// The budget counts mails the relay ACCEPTED: a send the relay refuses
// refunds its slot, so a dead relay or an unconfigured hub spends nothing.
//
// The budget comes from the settings snapshot on every Send, so
// mail_limits is live-tunable; a max of zero disables the cap entirely.
// Counters are in-memory per process with self-expiring fixed windows
// (the windowed core owns the expiry and the sweep): a restart clears
// them, and the map holds one small entry per recipient mailed inside one
// window -- limited by the hub's own send rate.
func NewRecipientLimitedSender(inner Sender, set *settings.Manager, now func() time.Time) Sender {
	return &recipientLimitedSender{inner: inner, set: set, now: now}
}

type recipientLimitedSender struct {
	inner Sender
	set   *settings.Manager
	now   func() time.Time

	// mu guards budgets; the caller-owned lock is the windowed core's
	// contract (see that package).
	mu      sync.Mutex
	budgets windowed.Windows[string]
}

// Send enforces the per-recipient budget, then delegates. The budget slot
// is reserved before the dial (concurrent sends to one recipient cannot
// all pass the check at once) and refunded when the relay refuses the
// message, so only delivered mail spends the window.
func (s *recipientLimitedSender) Send(ctx context.Context, msg Message) error {
	max, window := settings.MailRecipientBudget(s.set.Snapshot(ctx))
	if max <= 0 || window <= 0 {
		return s.inner.Send(ctx, msg)
	}
	key := normalizeRecipient(msg.To)
	now := s.now().UTC()
	s.mu.Lock()
	s.budgets.Sweep(now)
	b := s.budgets.Anchor(key, now, window)
	if b.Count >= max {
		s.mu.Unlock()
		return ErrRecipientRateLimited
	}
	b.Count++
	s.mu.Unlock()
	if err := s.inner.Send(ctx, msg); err != nil {
		s.mu.Lock()
		if b.Count > 0 {
			b.Count--
		}
		s.mu.Unlock()
		return err
	}
	return nil
}

// normalizeRecipient collapses the addresses one inbox receives onto one
// budget key: lowercase, and drop a +tag from the local part. The message
// itself is never rewritten -- delivery keeps the exact address; only the
// BUDGET counts the shared inbox.
func normalizeRecipient(addr string) string {
	local, domain, found := strings.Cut(addr, "@")
	if !found {
		return strings.ToLower(addr)
	}
	if plus := strings.IndexByte(local, '+'); plus >= 0 {
		local = local[:plus]
	}
	return strings.ToLower(local + "@" + domain)
}
