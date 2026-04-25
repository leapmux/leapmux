/**
 * Working context derived from the active tab. Built by
 * `getCurrentTabContext` in AppShell and passed down as a getter to
 * shell hooks (tab/agent/terminal ops, shortcuts) and shell components
 * (sidebars, dialogs, tile renderer).
 *
 * Passed as a plain getter (not a reactive accessor) so callers see the
 * fresh value on each call without subscribing to the source signals.
 */
export interface TabContext {
  workerId: string
  workingDir: string
  homeDir: string
}
