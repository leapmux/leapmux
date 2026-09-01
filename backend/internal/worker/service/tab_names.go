package service

import (
	"math/rand/v2"

	"github.com/leapmux/leapmux/generated/contracts"
)

// The pool lives in contracts/tab-names.json, and both languages generate
// from it: the worker reads contracts.TabNames here, and the browser reads
// TAB_NAMES to pre-fill the Title field in the New Agent and New Terminal
// dialogs. One table, so the name a dialog offers and the name the worker
// falls back to can never come from two lists that drift.
//
// The worker is the FALLBACK naming authority. It names any tab whose caller
// sent no title -- the CLI, the quick-open buttons, and ChangeBranchDialog --
// which is why the pool cannot live in the frontend alone: that arrangement
// left CLI-created tabs nameless.

// pickTabName returns a uniformly random name from the pool. With hundreds of
// names, collisions are improbable for typical workspaces; we chose
// random-with-collisions over query-the-DB-to-dedup because the spawn hot
// path doesn't need a name uniqueness invariant -- duplicates are cosmetic
// and the user can rename either tab.
func pickTabName() string {
	return contracts.TabNames[rand.IntN(len(contracts.TabNames))]
}

// pickAgentTitle returns "Agent <name>". See pickTabName for the
// collision policy.
func pickAgentTitle() string {
	return contracts.AgentTitlePrefix + " " + pickTabName()
}

// pickTerminalTitle returns "Terminal <name>".
func pickTerminalTitle() string {
	return contracts.TerminalTitlePrefix + " " + pickTabName()
}
