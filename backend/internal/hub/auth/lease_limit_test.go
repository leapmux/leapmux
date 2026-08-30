package auth

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// registerLease is the shape every test here needs: a lease for one user under
// one credential, with a context the caller can watch for the refusal's cancel.
func registerLease(t *testing.T, c *AuthContextRegistry, user, session string) (context.Context, func(), LeaseOutcome) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	release, outcome := c.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID:         userid.MustNew(user),
		Credential: SessionCredential(session),
	}, cancel)
	return ctx, release, outcome
}

func TestRegisterAuthenticatedLeaseRefusesAtTheUserCap(t *testing.T) {
	c := &AuthContextRegistry{state: &authState{}}
	c.SetMaxConnectionsPerUser(2)

	_, releaseA, outcomeA := registerLease(t, c, "user", "s1")
	require.Equal(t, LeaseGranted, outcomeA)
	defer releaseA()
	_, releaseB, outcomeB := registerLease(t, c, "user", "s2")
	require.Equal(t, LeaseGranted, outcomeB)
	defer releaseB()

	refusedCtx, _, outcomeC := registerLease(t, c, "user", "s3")
	assert.Equal(t, LeaseRefusedTooManyConnections, outcomeC,
		"the connection past the cap must be refused, and say that is why")

	// The refused connection's own context is cancelled, so the handler unwinds
	// rather than serving a socket the registry never indexed.
	select {
	case <-refusedCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("a refused registration must cancel the connection it declined")
	}
	assert.Len(t, c.state.leasesByUser["user"], 2, "the refused lease must not be indexed")
}

// The NEWEST connection pays. Refusing at the cap is only defensible because
// nothing already open is disturbed -- an eviction policy would move the failure
// to a connection the user is not looking at.
func TestRegisterAuthenticatedLeaseRefusesTheNewestNotTheOldest(t *testing.T) {
	c := &AuthContextRegistry{state: &authState{}}
	c.SetMaxConnectionsPerUser(1)

	establishedCtx, release, outcome := registerLease(t, c, "user", "s1")
	require.Equal(t, LeaseGranted, outcome)
	defer release()

	_, _, refused := registerLease(t, c, "user", "s2")
	require.Equal(t, LeaseRefusedTooManyConnections, refused)

	select {
	case <-establishedCtx.Done():
		t.Fatal("the connection that was already open must survive a refused newcomer")
	default:
	}
}

// Zero is unlimited, and it is the ZERO VALUE -- a registry nobody configures
// enforces nothing. Every test in this package that predates the cap depends on
// it, and so does any embedder that never calls the setter.
func TestRegisterAuthenticatedLeaseUnconfiguredIsUnlimited(t *testing.T) {
	c := &AuthContextRegistry{state: &authState{}}
	for i := range 100 {
		_, release, outcome := registerLease(t, c, "user", "s")
		require.Equal(t, LeaseGranted, outcome, "lease %d", i)
		defer release()
	}

	// And an explicit zero says the same thing.
	c.SetMaxConnectionsPerUser(0)
	_, release, outcome := registerLease(t, c, "user", "s")
	assert.Equal(t, LeaseGranted, outcome)
	release()
}

func TestRegisterAuthenticatedLeaseCapIsPerUser(t *testing.T) {
	c := &AuthContextRegistry{state: &authState{}}
	c.SetMaxConnectionsPerUser(1)

	_, releaseA, outcomeA := registerLease(t, c, "user-a", "s1")
	require.Equal(t, LeaseGranted, outcomeA)
	defer releaseA()

	// A different user is not affected by the first one's budget.
	_, releaseB, outcomeB := registerLease(t, c, "user-b", "s2")
	assert.Equal(t, LeaseGranted, outcomeB, "one user's cap must not bind another's")
	defer releaseB()

	_, _, refused := registerLease(t, c, "user-a", "s3")
	assert.Equal(t, LeaseRefusedTooManyConnections, refused)
}

// The budget is the USER's, not a credential's: a cookie session and a bearer
// token belonging to one person draw on one allowance, or the cap is trivially
// bypassed by minting another token.
func TestRegisterAuthenticatedLeaseCapCountsEveryCredentialKind(t *testing.T) {
	c := &AuthContextRegistry{state: &authState{}}
	c.SetMaxConnectionsPerUser(1)

	sessionCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release, outcome := c.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID:         userid.MustNew("user"),
		Credential: SessionCredential("s1"),
	}, cancel)
	require.Equal(t, LeaseGranted, outcome)
	defer release()
	_ = sessionCtx

	bearerCancel := func() {}
	_, bearerOutcome := c.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID:         userid.MustNew("user"),
		Credential: APICredential("token-1"),
	}, bearerCancel)
	assert.Equal(t, LeaseRefusedTooManyConnections, bearerOutcome,
		"a second credential for the same user must draw on the same budget")
}

// Every teardown path frees a slot because the cap counts leasesByUser itself
// rather than a side counter somebody would have to remember to decrement.
func TestRegisterAuthenticatedLeaseReleaseFreesASlot(t *testing.T) {
	c := &AuthContextRegistry{state: &authState{}}
	c.SetMaxConnectionsPerUser(1)

	_, release, outcome := registerLease(t, c, "user", "s1")
	require.Equal(t, LeaseGranted, outcome)

	_, _, refused := registerLease(t, c, "user", "s2")
	require.Equal(t, LeaseRefusedTooManyConnections, refused)

	release()

	_, release2, readmitted := registerLease(t, c, "user", "s3")
	assert.Equal(t, LeaseGranted, readmitted, "closing a connection must free its slot")
	release2()
}

// A credential that is dead AND at the cap reports the credential, because that
// is the one the user has to act on -- closing tabs would not get them back in.
func TestRegisterAuthenticatedLeaseReportsTheCredentialBeforeTheCap(t *testing.T) {
	c := &AuthContextRegistry{state: &authState{}}
	c.SetMaxConnectionsPerUser(1)

	_, release, outcome := registerLease(t, c, "user", "s1")
	require.Equal(t, LeaseGranted, outcome)
	defer release()

	// Already at the cap; now make the arriving credential expired too.
	expiredCancel := func() {}
	_, expired := c.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID:                  userid.MustNew("user"),
		Credential:          SessionCredential("s2"),
		CredentialExpiresAt: DeadlineAt(time.Now().Add(-time.Hour)),
	}, expiredCancel)
	assert.Equal(t, LeaseRefusedCredential, expired,
		"an expired credential must not be reported as a connection-cap refusal")
}

// The cap is tested and committed inside one critical section. Checked outside
// it -- an atomic pre-read, or a lookup before revocationMu -- every connection
// in a burst reads the same under-cap count and all of them are admitted, which
// is exactly the reconnect storm a cap exists for.
func TestRegisterAuthenticatedLeaseConcurrentRegistrationsCannotOvershoot(t *testing.T) {
	const (
		cap        = 8
		contenders = 64
	)
	c := &AuthContextRegistry{state: &authState{}}
	c.SetMaxConnectionsPerUser(cap)

	var (
		ready    sync.WaitGroup
		done     sync.WaitGroup
		admitted atomic.Int64
		releases = make([]func(), contenders)
	)
	release := make(chan struct{})
	ready.Add(contenders)
	for i := range contenders {
		done.Add(1)
		go func() {
			defer done.Done()
			ready.Done()
			<-release
			_, rel, outcome := registerLease(t, c, "user", "s")
			if outcome == LeaseGranted {
				admitted.Add(1)
				releases[i] = rel
			}
		}()
	}
	ready.Wait()
	close(release)
	done.Wait()

	assert.Equal(t, int64(cap), admitted.Load(),
		"exactly the cap may be admitted, however many arrive at once")
	assert.Len(t, c.state.leasesByUser["user"], cap,
		"and the index must agree with the verdicts handed out")

	for _, rel := range releases {
		if rel != nil {
			rel()
		}
	}
}

func TestSetMaxConnectionsPerUserIsSafeOnAZeroRegistry(t *testing.T) {
	var nilRegistry *AuthContextRegistry
	assert.NotPanics(t, func() { nilRegistry.SetMaxConnectionsPerUser(4) })

	// A nil registry enforces nothing at all, cap included.
	release, outcome := nilRegistry.RegisterAuthenticatedLease(
		context.Background(), &UserInfo{ID: userid.MustNew("user")}, func() {})
	assert.Equal(t, LeaseGranted, outcome)
	assert.NotPanics(t, release)
}

// The label set is what a dashboard groups by, so it has to be a complete
// partition with no two outcomes sharing a value.
func TestLeaseOutcomeLabelsPartitionTheOutcomes(t *testing.T) {
	seen := map[string]LeaseOutcome{}
	for _, o := range []LeaseOutcome{LeaseGranted, LeaseRefusedCredential, LeaseRefusedTooManyConnections} {
		label := o.Label()
		require.NotEqual(t, "unknown", label, "every real outcome needs its own label")
		if prev, dup := seen[label]; dup {
			t.Fatalf("outcomes %d and %d share the label %q", prev, o, label)
		}
		seen[label] = o
	}
	assert.Equal(t, "unknown", LeaseOutcome(-1).Label(),
		"an outcome added without a label must be visibly wrong, not silently blank")
}

// Label doubles as the WebSocket close REASON the Hub sends when it refuses a
// connection, so every value has to be legal as one. RFC 6455 caps a reason at
// 123 bytes and coder/websocket enforces that on send, so an over-long value
// would not fail loudly -- the close would simply go out without the token the
// client branches on.
//
// The spellings are asserted literally because they ARE that client contract:
// "close a tab" and "re-authenticate" are opposite advice, and the browser tells
// them apart by this exact string. Renaming one has to be a deliberate edit
// here, in channelwire's pinned testdata, and in the frontend copy -- not a
// silent one in a single place.
func TestLeaseOutcomeLabelsAreValidCloseReasons(t *testing.T) {
	// RFC 6455 section 5.5: control frame payloads are capped at 125 bytes, of
	// which the close status code takes two.
	const maxCloseReasonBytes = 123

	for _, tc := range []struct {
		outcome LeaseOutcome
		want    string
	}{
		{LeaseGranted, "granted"},
		{LeaseRefusedCredential, "credential"},
		{LeaseRefusedTooManyConnections, "too_many_connections"},
		{LeaseOutcome(-1), "unknown"},
	} {
		label := tc.outcome.Label()
		assert.Equal(t, tc.want, label)
		assert.LessOrEqual(t, len(label), maxCloseReasonBytes,
			"%q would be dropped rather than sent", label)
		assert.Equal(t, label, strings.TrimSpace(label),
			"a close reason a client compares must not carry surrounding whitespace")
		assert.NotContains(t, label, "\n")
	}

	// The refusal token is channelwire's, not a second copy of the same string.
	// Nothing checked that the two agreed, and the frontend asserts its own copy
	// against channelwire's pinned testdata -- so a fork here would leave the
	// metric label and the wire token describing the same refusal differently.
	assert.Equal(t, contracts.CloseReasonTooManyConnections,
		LeaseRefusedTooManyConnections.Label())
}

// The refusal's log line is the operator's only view of how close a user is to
// the cap, so the count it reports has to be the one that actually gated the
// decision -- not a second observation taken afterwards.
func TestRegisterAuthenticatedLeaseLogsTheCountThatGatedTheRefusal(t *testing.T) {
	buf := testutil.CaptureDefaultLogger(t)

	c := &AuthContextRegistry{state: &authState{}}
	c.SetMaxConnectionsPerUser(2)
	_, releaseA, outcomeA := registerLease(t, c, "user", "s1")
	require.Equal(t, LeaseGranted, outcomeA)
	defer releaseA()
	_, releaseB, outcomeB := registerLease(t, c, "user", "s2")
	require.Equal(t, LeaseGranted, outcomeB)
	defer releaseB()
	buf.Reset()

	_, _, refused := registerLease(t, c, "user", "s3")
	require.Equal(t, LeaseRefusedTooManyConnections, refused)

	logged := buf.String()
	assert.Contains(t, logged, "held=2",
		"the logged count must be the leases actually held when the cap bound")
	assert.Contains(t, logged, "limit=2")
	assert.Contains(t, logged, "user_id=user")
}
