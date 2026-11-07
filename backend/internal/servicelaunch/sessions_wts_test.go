package servicelaunch

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sessionAccountProbe struct {
	username string
	domain   string
	sid      string
	fail     string
	calls    []string
}

func (p *sessionAccountProbe) Username(id uint32) (string, error) {
	p.calls = append(p.calls, fmt.Sprintf("username:%d", id))
	if p.fail == "username" {
		return "", assert.AnError
	}
	return p.username, nil
}
func (p *sessionAccountProbe) Domain(id uint32) (string, error) {
	p.calls = append(p.calls, fmt.Sprintf("domain:%d", id))
	if p.fail == "domain" {
		return "", assert.AnError
	}
	return p.domain, nil
}
func (p *sessionAccountProbe) SID(account string) (string, error) {
	p.calls = append(p.calls, "sid:"+account)
	if p.fail == "sid" {
		return "", assert.AnError
	}
	return p.sid, nil
}

func TestWindowsSessionNativeUserlessEntries(t *testing.T) {
	for _, state := range []wtsConnectState{wtsIdle, wtsConnected} {
		t.Run(fmt.Sprint(state), func(t *testing.T) {
			reader := &sessionAccountProbe{}
			entries, err := translateWindowsSessions([]windowsSessionSnapshot{{ID: 1, State: state}}, reader)
			require.NoError(t, err)
			assert.Empty(t, entries)
			assert.Equal(t, []string{"username:1"}, reader.calls)
		})
	}
}

func TestWindowsSessionNativeAccountQueries(t *testing.T) {
	for _, state := range []wtsConnectState{wtsActive, wtsConnected, wtsDisconnected, wtsIdle} {
		t.Run(fmt.Sprint(state), func(t *testing.T) {
			for _, fail := range []string{"", "username", "domain", "sid", "empty sid", "empty username"} {
				t.Run(fail, func(t *testing.T) {
					reader := &sessionAccountProbe{username: "user", domain: "DOMAIN", sid: "S-1-5-21-1", fail: fail}
					if fail == "empty sid" {
						reader.sid = ""
					}
					if fail == "empty username" {
						reader.username = ""
					}
					entries, err := translateWindowsSessions([]windowsSessionSnapshot{{ID: 9, State: state}}, reader)
					wantCalls := []string{"username:9"}
					if fail != "username" && fail != "empty username" {
						wantCalls = append(wantCalls, "domain:9")
						if fail != "domain" {
							wantCalls = append(wantCalls, `sid:DOMAIN\user`)
						}
					}
					assert.Equal(t, wantCalls, reader.calls)
					if fail == "" {
						require.NoError(t, err)
						kind := SessionInteractive
						if state == wtsDisconnected {
							kind = SessionDisconnected
						}
						assert.Equal(t, []windowsSessionEntry{{ID: 9, Kind: kind, SID: reader.sid}}, entries)
						return
					}
					if fail == "empty username" && (state == wtsConnected || state == wtsIdle) {
						require.NoError(t, err)
						assert.Empty(t, entries)
						return
					}
					require.Error(t, err)
					assert.Nil(t, entries)
					if fail == "username" || fail == "domain" || fail == "sid" {
						assert.ErrorIs(t, err, assert.AnError)
					}
					stop, _ := LastSessionRule(nil, err)
					assert.False(t, stop)
				})
			}
		})
	}
	for _, state := range []wtsConnectState{wtsConnectQuery, wtsShadow, wtsListen, wtsReset, wtsDown, wtsInit} {
		reader := &sessionAccountProbe{fail: "username"}
		entries, err := translateWindowsSessions([]windowsSessionSnapshot{{ID: 1, State: state}, {ID: 0, State: wtsActive}}, reader)
		require.NoError(t, err)
		assert.Empty(t, entries)
		assert.Empty(t, reader.calls)
	}
	reader := &sessionAccountProbe{username: "user", sid: "S-1-5-21-2"}
	entries, err := translateWindowsSessions([]windowsSessionSnapshot{{ID: 1, State: wtsActive}}, reader)
	require.NoError(t, err)
	assert.Equal(t, []string{"username:1", "domain:1", "sid:user"}, reader.calls)
	monitor := &windowsSessionMonitor{enumerate: func() ([]windowsSessionEntry, error) { return entries, nil }}
	sessions, err := monitor.Sessions(t.Context(), AccountRef{ID: "S-1-5-21-1", Name: "user"})
	require.NoError(t, err)
	assert.Empty(t, sessions, "equal usernames do not prove equal accounts")
	_, err = monitor.Sessions(t.Context(), AccountRef{ID: "invalid"})
	require.Error(t, err)
	_, err = translateWindowsSessions([]windowsSessionSnapshot{{ID: 1, State: wtsActive}, {ID: 2, State: 99}}, reader)
	require.Error(t, err, "unknown native states cannot prove logout")
}

type scriptedSessionAccounts struct{}

func (scriptedSessionAccounts) Username(id uint32) (string, error) {
	if id == 5 {
		return "", assert.AnError
	}
	if id >= 10 {
		return "", nil
	}
	return fmt.Sprint(id), nil
}
func (scriptedSessionAccounts) Domain(uint32) (string, error) { return "", nil }
func (scriptedSessionAccounts) SID(account string) (string, error) {
	if account == "3" {
		return "S-1-5-21-2", nil
	}
	return "S-1-5-21-1", nil
}

func TestWindowsLoginSupervisorUsesNativeTranslation(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	created := clock.Trap().NewTicker(loginSessionPollTag)
	defer created.Close()
	observed := make(chan int, 8)
	reported := make(chan error, 1)
	done := make(chan error, 1)
	var calls atomic.Int32
	monitor := &windowsSessionMonitor{enumerate: func() ([]windowsSessionEntry, error) {
		call := int(calls.Add(1))
		defer func() { observed <- call }()
		raw := []windowsSessionSnapshot{{ID: 10, State: wtsIdle}, {ID: 11, State: wtsConnected}, {ID: 3, State: wtsActive}}
		switch call {
		case 1:
			raw = append(raw, windowsSessionSnapshot{ID: 1, State: wtsActive}, windowsSessionSnapshot{ID: 2, State: wtsActive})
		case 2:
			raw = append(raw, windowsSessionSnapshot{ID: 2, State: wtsActive})
		case 3:
			raw = append(raw, windowsSessionSnapshot{ID: 2, State: wtsDisconnected})
		case 4:
			raw = append(raw, windowsSessionSnapshot{ID: 5, State: wtsActive})
		}
		return translateWindowsSessions(raw, scriptedSessionAccounts{})
	}}
	go func() {
		done <- WatchLoginSessions(ctx, monitor, AccountRef{ID: "S-1-5-21-1"}, clock, func(err error) { reported <- err })
	}()
	created.MustWait(ctx).MustRelease(ctx)
	for call := 1; call <= 5; call++ {
		select {
		case got := <-observed:
			require.Equal(t, call, got)
		case <-ctx.Done():
			t.Fatal("session translation did not complete")
		}
		if call < 5 {
			select {
			case err := <-done:
				t.Fatalf("supervision ended before final logout: %v", err)
			default:
			}
			clock.Advance(time.Second).MustWait(ctx)
		}
	}
	select {
	case err := <-reported:
		require.ErrorIs(t, err, assert.AnError)
	case <-ctx.Done():
		t.Fatal("lookup error was lost")
	}
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("final logout did not stop supervision")
	}
}

func TestWindowsSessionMonitorMatchesVerifiedAccount(t *testing.T) {
	monitor := &windowsSessionMonitor{enumerate: func() ([]windowsSessionEntry, error) {
		return []windowsSessionEntry{
			{ID: 1, Kind: SessionInteractive, SID: "S-1-5-21-1"},
			{ID: 2, Kind: SessionDisconnected, SID: "S-1-5-21-1"},
			{ID: 3, Kind: SessionInteractive, SID: "S-1-5-21-2"},
			{ID: 0, Kind: SessionInteractive, SID: "S-1-5-21-1"},
			{ID: 4, Kind: SessionService},
			{ID: 5, Kind: SessionListener},
			{ID: 6, Kind: SessionBatch},
		}, nil
	}}
	sessions, err := monitor.Sessions(t.Context(), AccountRef{ID: "S-1-5-21-1", Name: "DOMAIN\\user"})
	require.NoError(t, err)
	assert.Equal(t, []Session{{ID: "1", Kind: SessionInteractive}, {ID: "2", Kind: SessionDisconnected}}, sessions)
}

func TestWindowsSessionMonitorNeverTreatsIdentityFailureAsLogout(t *testing.T) {
	for _, enumerationErr := range []error{nil, assert.AnError} {
		monitor := &windowsSessionMonitor{enumerate: func() ([]windowsSessionEntry, error) {
			return []windowsSessionEntry{{ID: 1, Kind: SessionInteractive}}, enumerationErr
		}}
		sessions, err := monitor.Sessions(t.Context(), AccountRef{ID: "S-1-5-21-1"})
		require.Error(t, err)
		stop, _ := LastSessionRule(sessions, err)
		assert.False(t, stop)
	}
}

func TestWindowsEnumerationSharesOnePendingCall(t *testing.T) {
	entered := make(chan int32, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	monitor := &windowsSessionMonitor{enumerate: func() ([]windowsSessionEntry, error) {
		entered <- calls.Add(1)
		<-release
		return []windowsSessionEntry{{ID: 1, Kind: SessionInteractive, SID: "S-1-5-21-1"}}, nil
	}}
	first := monitor.enumeration()
	require.EqualValues(t, 1, <-entered)
	for range 32 {
		require.Same(t, first, monitor.enumeration())
	}
	assert.EqualValues(t, 1, calls.Load())
	close(release)
	<-first.done
	sessions, err := monitor.Sessions(t.Context(), AccountRef{ID: "S-1-5-21-1"})
	require.NoError(t, err)
	assert.Equal(t, []Session{{ID: "1", Kind: SessionInteractive}}, sessions)
	assert.EqualValues(t, 2, calls.Load())
}
