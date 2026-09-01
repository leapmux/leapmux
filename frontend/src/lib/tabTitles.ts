import { AGENT_TITLE_PREFIX, TAB_NAMES, TERMINAL_TITLE_PREFIX } from '~/generated/contracts/tab-names'

/**
 * Auto-generated tab titles, drawn from the pool the WORKER names tabs with.
 *
 * Both sides generate from contracts/tab-names.json, so the title the New
 * Agent / New Terminal dialog pre-fills and the title the worker falls back to
 * for a caller that sends none come from one list. The worker still owns the
 * fallback -- the CLI, the quick-open buttons and ChangeBranchDialog all send
 * no title -- so this module adds a second CALLER of the pool, not a second
 * pool.
 *
 * The `<prefix> <name>` shape matters beyond looks: the worker's plan-mode
 * auto-rename only overwrites a title matching `^Agent [A-Z][A-Za-z]+$`, so a
 * pre-filled title the user leaves alone stays overwritable, exactly as a
 * worker-picked one is. A title the user EDITS stops matching and is then
 * preserved, which is the behaviour a user who typed a name expects.
 */
function randomTabName(): string {
  return TAB_NAMES[Math.floor(Math.random() * TAB_NAMES.length)]
}

export function randomAgentTitle(): string {
  return `${AGENT_TITLE_PREFIX} ${randomTabName()}`
}

export function randomTerminalTitle(): string {
  return `${TERMINAL_TITLE_PREFIX} ${randomTabName()}`
}
