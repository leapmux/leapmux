package mail

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/testutil"
)

// recordingSink captures sends and can fail them, so the limiter's
// pass-through behavior is asserted alongside its budgets.
type recordingSink struct {
	sent []Message
	err  error
}

func (r *recordingSink) Send(_ context.Context, msg Message) error {
	if r.err != nil {
		return r.err
	}
	r.sent = append(r.sent, msg)
	return nil
}

// setBudget writes the recipient cap the limiter reads per Send.
func setBudget(t *testing.T, set *settings.Manager, max int64, window time.Duration) {
	t.Helper()
	require.NoError(t, settings.KeyMailLimits.Set(context.Background(), set, settings.MailLimitsValue{
		FailureCooldownSeconds: 10,
		RecipientMax:           max,
		RecipientWindowSeconds: int64(window.Seconds()),
	}))
}

// newLimiter wires the sender over a fake clock the test advances, so a
// window elapses by moving time instead of reaching into the budget map.
func newLimiter(t *testing.T, sink Sender, max int64, window time.Duration) (Sender, *settings.Manager, *time.Time) {
	t.Helper()
	set := settings.NewManager(testutil.OpenTestStore(t), nil, settings.CoreDescriptors())
	setBudget(t, set, max, window)
	clock := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	now := &clock
	return NewRecipientLimitedSender(sink, set, func() time.Time { return *now }), set, now
}

func TestRecipientLimitCapsOneAddress(t *testing.T) {
	sink := &recordingSink{}
	s, _, _ := newLimiter(t, sink, 2, time.Hour)

	for i := 0; i < 2; i++ {
		require.NoError(t, s.Send(context.Background(), Message{To: "victim@example.com"}))
	}
	assert.Len(t, sink.sent, 2)

	err := s.Send(context.Background(), Message{To: "victim@example.com"})
	assert.ErrorIs(t, err, ErrRecipientRateLimited)
	assert.Len(t, sink.sent, 2, "a refused send must not reach the relay")
}

func TestRecipientLimitCountsPlusTagsAsOneInbox(t *testing.T) {
	sink := &recordingSink{}
	s, _, _ := newLimiter(t, sink, 2, time.Hour)

	// The signup bombing shape: one victim inbox, a fresh tagged address
	// per account. Delivery keeps the exact address; the budget does not.
	require.NoError(t, s.Send(context.Background(), Message{To: "victim+1@example.com"}))
	require.NoError(t, s.Send(context.Background(), Message{To: "Victim+two@Example.com"}))
	err := s.Send(context.Background(), Message{To: "victim+3@example.com"})
	assert.ErrorIs(t, err, ErrRecipientRateLimited)
	assert.Len(t, sink.sent, 2)

	// A different inbox has its own budget.
	require.NoError(t, s.Send(context.Background(), Message{To: "other@example.com"}))
}

func TestRecipientLimitWindowElapses(t *testing.T) {
	sink := &recordingSink{}
	s, _, now := newLimiter(t, sink, 1, time.Minute)

	require.NoError(t, s.Send(context.Background(), Message{To: "a@example.com"}))
	assert.ErrorIs(t, s.Send(context.Background(), Message{To: "a@example.com"}), ErrRecipientRateLimited)

	// One minute passes: the window closed, and the budget is whole again.
	*now = now.Add(61 * time.Second)
	require.NoError(t, s.Send(context.Background(), Message{To: "a@example.com"}))
}

// The fold's trade, pinned: on a provider that treats '+' as a literal
// local-part character, two distinct mailboxes share one budget, and the
// second one's mail is refused on the first one's spend. Counting exact
// addresses would reopen the bombing above, so this is the accepted cost.
func TestRecipientLimitFoldSharesOneBudgetAcrossTaggedMailboxes(t *testing.T) {
	sink := &recordingSink{}
	s, _, _ := newLimiter(t, sink, 1, time.Hour)

	require.NoError(t, s.Send(context.Background(), Message{To: "alice+ops@example.com"}))
	err := s.Send(context.Background(), Message{To: "alice+audit@example.com"})
	assert.ErrorIs(t, err, ErrRecipientRateLimited)
}

func TestRecipientLimitDisabledAtZero(t *testing.T) {
	sink := &recordingSink{}
	s, _, _ := newLimiter(t, sink, 0, time.Hour)

	for i := 0; i < 25; i++ {
		require.NoError(t, s.Send(context.Background(), Message{To: "a@example.com"}))
	}
	assert.Len(t, sink.sent, 25, "a zero max disables the cap")
}

func TestRecipientLimitDelegatesErrors(t *testing.T) {
	relayErr := errors.New("smtp unavailable")
	s, _, _ := newLimiter(t, &recordingSink{err: relayErr}, 1, time.Hour)

	assert.ErrorIs(t, s.Send(context.Background(), Message{To: "a@example.com"}), relayErr)
}

// A send the relay refuses delivered nothing, so it must not spend the
// inbox's window: the outage and recovery shape, where the budget would
// otherwise stay spent after the relay healed.
func TestRecipientLimitRefundsRefusedSends(t *testing.T) {
	relayErr := errors.New("smtp unavailable")
	sink := &recordingSink{err: relayErr}
	s, _, _ := newLimiter(t, sink, 1, time.Hour)

	for i := 0; i < 3; i++ {
		assert.ErrorIs(t, s.Send(context.Background(), Message{To: "a@example.com"}), relayErr)
	}

	// The relay heals: the three refused sends left the budget whole.
	sink.err = nil
	require.NoError(t, s.Send(context.Background(), Message{To: "a@example.com"}))
	assert.ErrorIs(t, s.Send(context.Background(), Message{To: "a@example.com"}), ErrRecipientRateLimited)
}

// Expired entries leave at the first Send past their window, and only
// those: a recipient whose window closed starts fresh while a sibling
// whose window still runs keeps its spend. (The sweep's
// no-traversal-before-expiry gate is the windowed core's own contract,
// pinned in its package.)
func TestRecipientLimitSweepDropsOnlyExpiredWindows(t *testing.T) {
	sink := &recordingSink{}
	s, _, now := newLimiter(t, sink, 1, time.Minute)

	// "early" anchors at T and closes at T+1m; "late" anchors 30s later
	// and closes at T+1m30s.
	require.NoError(t, s.Send(context.Background(), Message{To: "early@example.com"}))
	*now = now.Add(30 * time.Second)
	require.NoError(t, s.Send(context.Background(), Message{To: "late@example.com"}))

	// Past "early"'s close but inside "late"'s window: early starts a
	// fresh budget, late is still spent.
	*now = now.Add(31 * time.Second)
	require.NoError(t, s.Send(context.Background(), Message{To: "early@example.com"}),
		"an expired window must not block its recipient")
	assert.ErrorIs(t, s.Send(context.Background(), Message{To: "late@example.com"}), ErrRecipientRateLimited,
		"a live window must keep its spend")
}
