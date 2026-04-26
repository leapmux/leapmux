/* eslint-disable solid/no-innerhtml -- HTML is produced via renderAnsi, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import Check from 'lucide-solid/icons/check'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import { createMemo, Show } from 'solid-js'
import { containsAnsi, renderAnsi } from '~/lib/renderAnsi'
import { COLLAPSED_RESULT_ROWS, stripLeadingBlankLines } from '../toolRenderers'
import { toolResultCollapsed, toolResultContentAnsi, toolResultContentPre } from '../toolStyles.css'
import { ToolStatusHeader } from './ToolStatusHeader'

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
  /** Claude only: bytes path when output was truncated to disk. */
  persistedOutputPath?: string
  persistedOutputSize?: number
  /** Claude only: tool was invoked with `output_format: "no-output"`. */
  noOutputExpected?: boolean
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
  const lines = createMemo(() => normalized().split('\n'))
  const isCollapsed = createMemo(() =>
    !(props.context?.toolResultExpanded?.() ?? false) && lines().length > COLLAPSED_RESULT_ROWS,
  )
  const display = createMemo(() => isCollapsed()
    ? lines().slice(0, COLLAPSED_RESULT_ROWS).join('\n')
    : normalized())
  const statusIcon = () => props.source.isError ? CircleAlert : Check

  return (
    <ToolStatusHeader icon={statusIcon()} title={commandStatusLabel(props.source)} dataToolMessage>
      <Show when={normalized()}>
        {containsAnsi(normalized())
          ? <div class={`${toolResultContentAnsi}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`} innerHTML={renderAnsi(display())} />
          : <div class={`${toolResultContentPre}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`}>{display()}</div>}
      </Show>
    </ToolStatusHeader>
  )
}
