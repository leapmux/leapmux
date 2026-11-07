package servicelaunch

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func installedTaskAccess(account string) windowsTaskAccess {
	return windowsTaskAccess{Present: true, Protected: true, Entries: []windowsTaskAccessEntry{
		{SID: "S-1-5-18", Mask: windowsTaskFullAccess},
		{SID: "S-1-5-32-544", Mask: windowsTaskFullAccess},
		{SID: account, Mask: windowsTaskFullAccess},
	}}
}

func TestTaskSnapshotRequiresPrivateAccess(t *testing.T) {
	spec, err := LaunchSpecFromService(testSpec())
	require.NoError(t, err)
	spec.AccountID = "S-1-5-21-1001"
	data, err := taskDefinitionXML(spec, strings.Join(spec.RunArguments(), " "))
	require.NoError(t, err)
	var snapshot windowsTaskSnapshot
	require.NoError(t, xml.Unmarshal(data, &snapshot.Definition))
	snapshot.Enabled = true
	snapshot.Identity = windowsTaskIdentity{Arguments: spec.RunArguments(), PrincipalSID: spec.AccountID, TriggerSID: spec.AccountID}
	snapshot.Access = installedTaskAccess(spec.AccountID)
	require.True(t, snapshot.matches(spec))
	snapshot.Access.Entries = append(snapshot.Access.Entries, windowsTaskAccessEntry{SID: "S-1-1-0", Mask: windowsTaskFullAccess})
	assert.False(t, snapshot.matches(spec), "a different account must not gain control of the task")
	snapshot.Access = windowsTaskAccess{}
	assert.False(t, snapshot.matches(spec), "missing access information must refuse startup")
	calls := 0
	run := func() error { calls++; return assert.AnError }
	require.ErrorContains(t, startTaskSnapshot(snapshot, spec, run), "repair")
	assert.Zero(t, calls)
	snapshot.Access = installedTaskAccess(spec.AccountID)
	snapshot.Instances = 1
	require.ErrorContains(t, startTaskSnapshot(snapshot, spec, run), "already running")
	assert.Zero(t, calls)
	snapshot.Instances = 0
	require.ErrorIs(t, startTaskSnapshot(snapshot, spec, run), assert.AnError)
	assert.Equal(t, 1, calls)
	require.NoError(t, startTaskSnapshot(snapshot, spec, func() error { calls++; return nil }))
	assert.Equal(t, 2, calls)
}

func TestTaskSnapshotActivity(t *testing.T) {
	for state := int32(0); state <= 4; state++ {
		assert.Equal(t, state == 2 || state == 4, (windowsTaskSnapshot{State: state}).active())
		assert.True(t, (windowsTaskSnapshot{State: state, Instances: 1}).active())
	}
}

func TestWindowsTaskAccessMatchesInstalledPolicy(t *testing.T) {
	const account = "S-1-5-21-1001"
	for name, mutate := range map[string]func(*windowsTaskAccess){
		"missing":                func(a *windowsTaskAccess) { a.Present = false },
		"unprotected":            func(a *windowsTaskAccess) { a.Protected = false },
		"defaulted":              func(a *windowsTaskAccess) { a.Defaulted = true },
		"empty":                  func(a *windowsTaskAccess) { a.Entries = nil },
		"missing user":           func(a *windowsTaskAccess) { a.Entries = a.Entries[:2] },
		"missing system":         func(a *windowsTaskAccess) { a.Entries = a.Entries[1:] },
		"missing administrators": func(a *windowsTaskAccess) { a.Entries = append(a.Entries[:1], a.Entries[2]) },
		"read only":              func(a *windowsTaskAccess) { a.Entries[2].Mask = windowsTaskFileGenericRead },
		"missing write dacl":     func(a *windowsTaskAccess) { a.Entries[2].Mask &^= 0x40000 },
		"extra full": func(a *windowsTaskAccess) {
			a.Entries = append(a.Entries, windowsTaskAccessEntry{SID: "S-1-1-0", Mask: windowsTaskFullAccess})
		},
		"extra read": func(a *windowsTaskAccess) {
			a.Entries = append(a.Entries, windowsTaskAccessEntry{SID: "S-1-1-0", Mask: windowsTaskFileGenericRead})
		},
		"extra zero":      func(a *windowsTaskAccess) { a.Entries = append(a.Entries, windowsTaskAccessEntry{SID: "S-1-1-0"}) },
		"deny":            func(a *windowsTaskAccess) { a.Entries[2].Kind = 1 },
		"object":          func(a *windowsTaskAccess) { a.Entries[2].Kind = 5 },
		"callback":        func(a *windowsTaskAccess) { a.Entries[2].Kind = 9 },
		"system security": func(a *windowsTaskAccess) { a.Entries[2].Mask |= 0x01000000 },
		"maximum allowed": func(a *windowsTaskAccess) { a.Entries[2].Mask |= 0x02000000 },
	} {
		t.Run(name, func(t *testing.T) { a := installedTaskAccess(account); mutate(&a); assert.False(t, a.matches(account)) })
	}
	for _, flags := range []uint8{1, 2, 4, 8, 16, 32, 64, 128} {
		a := installedTaskAccess(account)
		a.Entries[2].Flags = flags
		assert.False(t, a.matches(account))
	}
	for name, mutate := range map[string]func(*windowsTaskAccess){
		"original":  func(*windowsTaskAccess) {},
		"reordered": func(a *windowsTaskAccess) { a.Entries[0], a.Entries[2] = a.Entries[2], a.Entries[0] },
		"duplicate": func(a *windowsTaskAccess) { a.Entries = append(a.Entries, a.Entries[2]) },
		"split": func(a *windowsTaskAccess) {
			a.Entries[2].Mask = 0x40000
			a.Entries = append(a.Entries, windowsTaskAccessEntry{SID: account, Mask: windowsTaskFullAccess &^ 0x40000})
		},
		"generic all": func(a *windowsTaskAccess) { a.Entries[2].Mask = windowsTaskGenericAll },
		"generic components": func(a *windowsTaskAccess) {
			a.Entries[2].Mask = windowsTaskGenericRead | windowsTaskGenericWrite | windowsTaskGenericExecute | 0x000d0040
		},
	} {
		t.Run(name, func(t *testing.T) { a := installedTaskAccess(account); mutate(&a); assert.True(t, a.matches(account)) })
	}
	assert.False(t, installedTaskAccess(account).matches(""))
	assert.Equal(t, 0x16, windowsTaskRegistrationFlags)
	assert.Equal(t, "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;S-1-5-21-1001)", windowsTaskSecurityDescriptor(account))
}
