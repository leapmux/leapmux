import { AGENT_TITLE_PREFIX, TAB_NAMES, TERMINAL_TITLE_PREFIX } from '~/generated/contracts/tab-names'

/**
 * Auto-generated tab titles, drawn from the pool the WORKER uses for a tab.
 *
 * Both sides generate from contracts/tab-names.json, so the title the New
 * Agent / New Terminal dialog pre-fills and the title the worker falls back to
 * for a caller that sends none come from one list. The worker still owns the
 * fallback -- the CLI and the quick-open buttons send no title -- so this
 * module adds a second CALLER of the pool, not a second pool.
 *
 * The `<prefix> <name>` shape is READABILITY only. Nothing reads a title back
 * to decide what it means: a dialog reports whether the user kept this
 * suggestion (`createTitleState.isPristine`, sent as
 * `OpenAgentRequest.title_auto_generated`), and the worker records that answer
 * on the row. Plan-mode auto-rename reads the record, so a title the user
 * typed survives whatever shape it happens to have -- including `Agent Bob`,
 * which an earlier rule matched against this same pattern and overwrote.
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
