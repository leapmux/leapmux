package providerkit

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The zero value is usable, so an embedder needs no constructor -- and every
// current embedder relies on that (Base, codex.Agent and pi.Agent are all
// constructed as struct literals, some of them in tests).
func TestPendingPrompts_ZeroValueIsUsable(t *testing.T) {
	t.Parallel()

	var p PendingPrompts
	assert.Zero(t, p.CountForTest())
	assert.Empty(t, p.Take("absent"), "taking from an empty store is not a panic")
	p.Forget("absent")
	p.Clear()

	p.Remember("k", "prompt")
	assert.Equal(t, "prompt", p.PeekForTest("k"))
}

// First write wins: a provider can re-announce a spawn (a history replay, a
// progress update repeating the payload), and the FIRST prompt is the real one.
func TestPendingPrompts_FirstWriteWins(t *testing.T) {
	t.Parallel()

	var p PendingPrompts
	p.Remember("k", "first")
	p.Remember("k", "second")

	assert.Equal(t, "first", p.PeekForTest("k"))
}

// Taking spends the entry, so a replayed observation cannot persist the prompt
// into the transcript twice.
func TestPendingPrompts_TakeSpendsTheEntry(t *testing.T) {
	t.Parallel()

	var p PendingPrompts
	p.Remember("k", "prompt")

	assert.Equal(t, "prompt", p.Take("k"))
	assert.Empty(t, p.Take("k"), "the second take finds nothing")
	assert.Zero(t, p.CountForTest())
}

// A blank key or prompt cannot be spent, so storing one would only retain it.
func TestPendingPrompts_IgnoresBlanks(t *testing.T) {
	t.Parallel()

	var p PendingPrompts
	p.Remember("", "prompt")
	p.Remember("k", "")

	assert.Zero(t, p.CountForTest())
}

// forget drops a spawn whose child transcript never appears; clear drops the
// whole session's. Without them an unspent prompt is retained for the life of
// the process, and a reused key would open the next transcript on the previous
// session's instruction.
func TestPendingPrompts_ForgetAndClearRelease(t *testing.T) {
	t.Parallel()

	var p PendingPrompts
	p.Remember("a", "one")
	p.Remember("b", "two")

	p.Forget("a")
	assert.Empty(t, p.PeekForTest("a"))
	assert.Equal(t, 1, p.CountForTest())

	p.Clear()
	assert.Zero(t, p.CountForTest())
	assert.Empty(t, p.PeekForTest("b"))
}

// Every provider reaches this from its own reader goroutine while the sink can
// be spending an entry, which is why the type carries its own mutex rather than
// borrowing the agent's.
func TestPendingPrompts_ConcurrentUse(t *testing.T) {
	t.Parallel()

	var p PendingPrompts
	var wg sync.WaitGroup
	const keys = 50
	for i := range keys {
		wg.Add(2)
		key := fmt.Sprintf("k-%d", i)
		go func() { defer wg.Done(); p.Remember(key, "prompt") }()
		go func() { defer wg.Done(); p.Take(key) }()
	}
	wg.Wait()

	// Whichever order each pair ran in, no entry may be left half-written: a
	// key is either spent or still held with its prompt intact.
	for i := range keys {
		if got := p.PeekForTest(fmt.Sprintf("k-%d", i)); got != "" {
			require.Equal(t, "prompt", got)
		}
	}
}
