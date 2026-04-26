import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import Check from 'lucide-solid/icons/check'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import { createMemo, Show } from 'solid-js'
import { getToolResultExpanded } from '../messageRenderers'
import { stripLeadingBlankLines } from '../toolRenderers'
import { CollapsibleContent } from './CollapsibleContent'
import { ToolStatusHeader } from './ToolStatusHeader'
import { useCollapsedLines } from './useCollapsedLines'

/**
 * Provider-neutral source for a command-execution result (Claude `Bash`,
 * Codex `commandExecution`, ACP `execute`).
 *
 * `output` is the raw stream (may contain ANSI). Claude's structured Bash
 * payload separates `stdout`/`stderr`; the body concatenates them via
 * `output` for now and surfaces `stderr` for future styling.
 */
export interface CommandResultSource {
  output: string
  /** Claude only: stderr separated from stdout. */
  stderr?: string
  exitCode?: number | null
  durationMs?: number | null
  /** True when the command was interrupted by the user (Ctrl-C). */
  interrupted?: boolean
  /** Resolved error state. */
  isError: boolean
}

/**
 * Resolve the canonical "is this command an error?" boolean shared by ACP
 * `execute` and Codex `commandExecution`: failed-status OR known non-zero
 * exit code. Both extractors agreed on this rule independently; centralizing
 * it keeps them from drifting.
 */
export function commandIsError(status: string | undefined, exitCode: number | null | undefined): boolean {
  if (status === 'failed')
    return true
  return typeof exitCode === 'number' && exitCode !== 0
}

/**
 * Canonical status label:
 *  - interrupted → "Interrupted"
 *  - exitCode known and non-zero → "Error (exit N)"
 *  - isError without known exit code → "Error"
 *  - else → "Success"
 */
export function commandStatusLabel(source: CommandResultSource): string {
  if (source.interrupted)
    return 'Interrupted'
  if (typeof source.exitCode === 'number' && source.exitCode !== 0)
    return `Error (exit ${source.exitCode})`
  if (source.isError)
    return 'Error'
  return 'Success'
}

export function CommandResultBody(props: {
  source: CommandResultSource
  context?: RenderContext
}): JSX.Element {
  const normalized = createMemo(() => stripLeadingBlankLines(props.source.output))
  const expanded = () => getToolResultExpanded(props.context)
  const { display, isCollapsed } = useCollapsedLines({ text: normalized, expanded })
  const statusIcon = () => props.source.isError ? CircleAlert : Check

  return (
    <ToolStatusHeader icon={statusIcon()} title={commandStatusLabel(props.source)} dataToolMessage>
      <Show when={normalized()}>
        <CollapsibleContent kind="ansi-or-pre" text={normalized()} display={display()} isCollapsed={isCollapsed()} />
      </Show>
    </ToolStatusHeader>
  )
}
