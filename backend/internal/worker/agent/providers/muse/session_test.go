package muse

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func museSessionPage(t *testing.T, sessions []map[string]any, cursor any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"sessions": sessions, "nextCursor": cursor})
	require.NoError(t, err)
	return raw
}

func museStoredSession(id, workspace, stamp string) map[string]any {
	return map[string]any{"sessionId": id, "kind": "root", "workspaceRoot": workspace, "createdAt": "2026-10-08T00:00:00Z", "updatedAt": stamp, "modelId": nil, "activeTurnId": nil}
}

func museRoleSessionQuery(opts agent.Options, registration agent.Registration, env []string, limit int) agent.StoredSessionQuery {
	return agent.StoredSessionQuery{WorkingDir: opts.WorkingDir, Shell: opts.Shell, LoginShell: opts.LoginShell, EnvEntries: env, RuntimeLocator: &registration.Locator, Limit: limit}
}

func TestMuseStoredSessionsReadsEveryNativePageAndSortsRootSessions(t *testing.T) {
	opts, registration, env, path := museRoleLaunch(t, museRoleHostSpec{BuildPages: func(workspace string) []json.RawMessage {
		older := museStoredSession("older", workspace, "2026-10-08T00:00:01Z")
		older["firstUserPrompt"] = "First native prompt"
		newer := museStoredSession("newer", workspace, "2026-10-08T00:00:02Z")
		newer["name"] = "Native served name"
		newer["lastActivityAt"] = "2026-10-08T00:00:04Z"
		child := museStoredSession("child", workspace, "2026-10-08T00:00:05Z")
		child["kind"] = "subagent"
		tied := museStoredSession("tie", workspace, "2026-10-08T00:00:04Z")
		tied["title"] = "Native title"
		tied["name"] = "Ignored served name"
		return []json.RawMessage{museSessionPage(t, []map[string]any{older, child}, "opaque-page"), museSessionPage(t, []map[string]any{tied, newer}, nil)}
	}})

	got, err := storedSessions(museRoleContext(t), museRoleSessionQuery(opts, registration, env, 10), nil)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []string{"newer", "tie", "older"}, []string{got[0].Handle, got[1].Handle, got[2].Handle})
	assert.Equal(t, []string{"Native served name", "Native title", "First native prompt"}, []string{got[0].Title, got[1].Title, got[2].Title})
	assert.Equal(t, time.Date(2026, 10, 8, 0, 0, 4, 0, time.UTC), got[0].UpdatedAt)
	records := museRoleRecords(t, path)
	var queries []map[string]any
	for _, record := range records {
		assert.NotEqual(t, methodSessionStart, record.Method)
		assert.NotEqual(t, methodSessionResume, record.Method)
		if record.Method != methodSessionList {
			continue
		}
		var params map[string]any
		require.NoError(t, json.Unmarshal(record.Params, &params))
		assert.Equal(t, opts.WorkingDir, params["workspaceRoot"])
		queries = append(queries, params)
	}
	require.Len(t, queries, 2)
	assert.NotContains(t, queries[0], "cursor")
	assert.Equal(t, "opaque-page", queries[1]["cursor"])
	assert.Equal(t, "$closed", records[len(records)-1].Method)
	assert.True(t, museRoleHostIdentity(t, path).IsZero())
}

func TestMuseStoredSessionsRejectsMalformedPages(t *testing.T) {
	for _, raw := range []string{
		`null`, `{}`, `{"sessions":null,"nextCursor":null}`, `{"sessions":false,"nextCursor":null}`,
		`{"sessions":[]}`, `{"sessions":[],"nextCursor":false}`, `{"sessions":[],"nextCursor":""}`,
		`{"sessions":[],"nextCursor":"more"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			opts, registration, env, path := museRoleLaunch(t, museRoleHostSpec{Pages: []json.RawMessage{json.RawMessage(raw)}})
			got, err := storedSessions(museRoleContext(t), museRoleSessionQuery(opts, registration, env, 10), nil)
			require.Error(t, err)
			assert.Nil(t, got)
			records := museRoleRecords(t, path)
			assert.Equal(t, "$closed", records[len(records)-1].Method)
		})
	}
}

func TestMuseStoredSessionsRejectsInvalidDatesBeforeSortingAndCapping(t *testing.T) {
	for _, field := range []string{"createdAt", "updatedAt", "lastActivityAt"} {
		for _, value := range []any{"invalid", nil, 0, "2026-10-07T23:59:59Z"} {
			t.Run(field+"/"+fmt.Sprint(value), func(t *testing.T) {
				opts, registration, env, path := museRoleLaunch(t, museRoleHostSpec{BuildPages: func(workspace string) []json.RawMessage {
					session := museStoredSession("invalid-date", workspace, "2026-10-08T00:00:01Z")
					session[field] = value
					if field == "createdAt" && value == "2026-10-07T23:59:59Z" {
						session["updatedAt"] = "2026-10-07T23:59:58Z"
					}
					return []json.RawMessage{museSessionPage(t, []map[string]any{session}, nil)}
				}})
				got, err := storedSessions(museRoleContext(t), museRoleSessionQuery(opts, registration, env, 1), nil)
				require.Error(t, err)
				assert.Nil(t, got)
				records := museRoleRecords(t, path)
				assert.Equal(t, "$closed", records[len(records)-1].Method)
			})
		}
	}
}

func TestMuseStoredSessionsPreservesAnAbsentOptionalActivityTimestamp(t *testing.T) {
	opts, registration, env, _ := museRoleLaunch(t, museRoleHostSpec{BuildPages: func(workspace string) []json.RawMessage {
		session := museStoredSession("native-session", workspace, "2026-10-08T00:00:01Z")
		return []json.RawMessage{museSessionPage(t, []map[string]any{session}, nil)}
	}})
	got, err := storedSessions(museRoleContext(t), museRoleSessionQuery(opts, registration, env, 1), nil)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, time.Date(2026, 10, 8, 0, 0, 1, 0, time.UTC), got[0].UpdatedAt)
}

func TestMuseStoredSessionsRejectsRepeatedCursorsAndKeepsTheHostOwned(t *testing.T) {
	p := museSessionPage(t, []map[string]any{}, "same-cursor")
	opts, registration, env, path := museRoleLaunch(t, museRoleHostSpec{Pages: []json.RawMessage{p, p}})
	got, err := storedSessions(museRoleContext(t), museRoleSessionQuery(opts, registration, env, 10), nil)
	require.ErrorContains(t, err, "cursor")
	assert.Nil(t, got)
	records := museRoleRecords(t, path)
	assert.Equal(t, "$closed", records[len(records)-1].Method)
	assert.True(t, museRoleHostIdentity(t, path).IsZero())
}

func TestMuseStoredSessionsPreservesNativeRefusalAndClosesTheQueryHost(t *testing.T) {
	opts, registration, env, path := museRoleLaunch(t, museRoleHostSpec{PageError: json.RawMessage(`{"code":-32000,"message":"The native index is unavailable"}`)})
	got, err := storedSessions(museRoleContext(t), museRoleSessionQuery(opts, registration, env, 10), nil)
	require.ErrorContains(t, err, "The native index is unavailable")
	assert.Nil(t, got)
	records := museRoleRecords(t, path)
	assert.Equal(t, "$closed", records[len(records)-1].Method)
	assert.True(t, museRoleHostIdentity(t, path).IsZero())
}

func TestMuseStoredSessionsCancelsAnActiveQueryAndClosesItsExactHost(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	opts, registration, env, path := museRoleLaunch(t, museRoleHostSpec{ReadyAddress: listener.Addr().String()})
	ctx, cancel := context.WithCancel(museRoleContext(t))
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() {
		_, err := storedSessions(ctx, museRoleSessionQuery(opts, registration, env, 10), nil)
		result <- err
	}()
	accepted := make(chan net.Conn, 1)
	acceptErrors := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			acceptErrors <- err
			return
		}
		accepted <- connection
	}()
	var connection net.Conn
	select {
	case connection = <-accepted:
	case err := <-acceptErrors:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal("the native query did not reach its controlled stop point")
	}
	t.Cleanup(func() { _ = connection.Close() })
	require.NoError(t, connection.SetReadDeadline(time.Now().Add(30*time.Second)))
	var ready [1]byte
	_, err = connection.Read(ready[:])
	require.NoError(t, err)
	assert.Equal(t, byte(1), ready[0])
	identity := museRoleHostIdentity(t, path)
	require.False(t, identity.IsZero())
	cancel()
	select {
	case err = <-result:
		require.Error(t, err)
	case <-museRoleContext(t).Done():
		t.Fatal("the canceled native query did not return")
	}
	assert.False(t, identity.Runs())
}

func TestMuseStoredSessionsCapsNativePagesAndReturnedRows(t *testing.T) {
	for _, limit := range []int{0, -1, 1, 201} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			count := limit
			if count <= 0 {
				count = agent.DefaultStoredSessionLimit
			}
			opts, registration, env, path := museRoleLaunch(t, museRoleHostSpec{BuildPages: func(workspace string) []json.RawMessage {
				var pages []json.RawMessage
				for first := 0; first < count; first += 200 {
					var sessions []map[string]any
					for index := first; index < min(first+200, count); index++ {
						sessions = append(sessions, museStoredSession(fmt.Sprintf("session-%04d", index), workspace, "2026-10-08T00:00:01Z"))
					}
					var cursor any
					if first+200 < count {
						cursor = "next-page"
					}
					pages = append(pages, museSessionPage(t, sessions, cursor))
				}
				return pages
			}})
			got, err := storedSessions(museRoleContext(t), museRoleSessionQuery(opts, registration, env, limit), nil)
			require.NoError(t, err)
			assert.Len(t, got, count)
			page := 0
			for _, record := range museRoleRecords(t, path) {
				if record.Method != methodSessionList {
					continue
				}
				var params struct {
					Limit int `json:"limit"`
				}
				require.NoError(t, json.Unmarshal(record.Params, &params))
				assert.Equal(t, min(200, count-page*200), params.Limit)
				page++
			}
			assert.Equal(t, (count+199)/200, page)
		})
	}
}

func TestMuseStoredSessionsPreservesQueryAndCleanupFailures(t *testing.T) {
	for _, queryFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "successful query", true: "native query error"}[queryFails], func(t *testing.T) {
			spec := museRoleHostSpec{CloseExitCode: 7, Pages: []json.RawMessage{museSessionPage(t, []map[string]any{}, nil)}}
			if queryFails {
				spec.PageError = json.RawMessage(`{"code":-32000,"message":"The native index is unavailable"}`)
			}
			opts, registration, env, path := museRoleLaunch(t, spec)
			got, err := storedSessions(museRoleContext(t), museRoleSessionQuery(opts, registration, env, 10), nil)
			if queryFails {
				require.ErrorContains(t, err, "The native index is unavailable")
			}
			var nativeExit *exec.ExitError
			require.ErrorAs(t, err, &nativeExit)
			assert.Equal(t, 7, nativeExit.ExitCode())
			assert.Nil(t, got)
			records := museRoleRecords(t, path)
			assert.Equal(t, "$closed", records[len(records)-1].Method)
		})
	}
}
