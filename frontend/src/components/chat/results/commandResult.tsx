import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import Check from 'lucide-solid/icons/check'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import Terminal from 'lucide-solid/icons/terminal'
import { createMemo, For, Show } from 'solid-js'
import { normalizedCommandBody, normalizeProgressOutput, PROGRESS_MAX_ROWS } from '~/lib/normalizeProgressOutput'
import { messageCompletionFromProto } from '../assembledMessage'
import { cachedRenderValueForString } from '../messageRenderCache'
import { getToolResultExpanded } from '../messageRenderers'
import { formatDuration, joinMetaParts } from '../rendererUtils'
import { toolInputSummary, toolMessage } from '../toolStyles.css'
import { COLLAPSED_RESULT_ROWS, hasMoreLinesThan } from './collapse'
import { CollapsibleContent } from './CollapsibleContent'
import { ToolHeaderRow, ToolStatusHeader } from './ToolStatusHeader'
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
  /** A referenced output stream could not be recovered. This is different from an empty stream. */
  outputUnavailable?: boolean
  /** Claude only: stderr separated from stdout. */
  stderr?: string
  exitCode?: number | null
  durationMs?: number | null
  /** The provider retained only a suffix of the command output. */
  truncated?: boolean
  /** True when the command was interrupted by the user (Ctrl-C). */
  interrupted?: boolean
  /** Resolved error state. */
  isError: boolean
}

export interface CommandResultEntry {
  label?: string
  source: CommandResultSource
}

/** Keep separate process output and status when one tool call owns several commands. */
export function CommandResultList(props: { entries: CommandResultEntry[], context?: RenderContext }): JSX.Element {
  return (
    <For each={props.entries}>
      {entry => (
        <>
          <Show when={entry.label}>{label => <ToolHeaderRow icon={Terminal} title={label()} />}</Show>
          <CommandResultBody source={entry.source} context={props.context} />
        </>
      )}
    </For>
  )
}

/**
 * The row count below which {@link CommandResultBody} stops collapsing: widened to
 * {@link PROGRESS_MAX_ROWS} when the output carried `\r`-overwrites (so the head/`…`/
 * tail rows the normalize step just produced aren't sliced back off), else the plain
 * {@link COLLAPSED_RESULT_ROWS}. The body and the toolbar's `collapsible` check
 * (`commandOutputIsCollapsible`) must agree on this threshold or the expand button
 * hides over output the body actually clips -- so both read it from here.
 */
export function commandCollapseThreshold(hadCarriageReturns: boolean): number {
  return hadCarriageReturns ? PROGRESS_MAX_ROWS : COLLAPSED_RESULT_ROWS
}

/**
 * Mirror of {@link CommandResultBody}'s collapse decision for tool-meta
 * `collapsible` checks. `hasMoreLinesThan` against raw `\n`s under-counts
 * when output contains `\r`-overwrites (progress bars, `git rebase`, etc.)
 * because the body normalizes those into separate lines at render time —
 * leaving the toolbar's expand button hidden over output the body actually
 * clips. Use this helper for any provider feeding `CommandResultBody` so
 * the meta and the body agree.
 */
export function commandOutputIsCollapsible(text: string): boolean {
  const { text: normalized, hadCarriageReturns } = normalizeProgressOutput(text)
  return hasMoreLinesThan(normalized, commandCollapseThreshold(hadCarriageReturns))
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
  // The shared normalize-then-strip transform (order matters: normalize CR
  // overwrites first so a leading bare `\r` becomes a `\n` that strip can then
  // trim). normalizedCommandBody is the single source every command-result
  // renderer path uses, so the body can't drift.
  const body = createMemo(() => {
    const context = props.context
    const output = props.source.output
    return cachedRenderValueForString(
      context,
      'commandResult.normalizedBody',
      output,
      () => normalizedCommandBody(output),
    )
  })
  const normalized = createMemo(() => body().text)
  const expanded = () => getToolResultExpanded(props.context)
  // After CR normalization the output has at most PROGRESS_MAX_ROWS rows
  // (head + `…` + tail). Widen the row threshold so the default 3-row
  // collapse doesn't slice the tail/ellipsis we just produced back off.
  const { display, isCollapsed } = useCollapsedLines({
    text: normalized,
    expanded,
    threshold: () => commandCollapseThreshold(body().hadCarriageReturns),
  })
  const source = () => {
    const completion = messageCompletionFromProto(props.context?.sources?.current()?.completion)
    return {
      ...props.source,
      interrupted: props.source.interrupted || completion === 'interrupted',
      isError: props.source.isError || completion === 'error',
    }
  }
  const statusIcon = () => source().isError || source().interrupted ? CircleAlert : Check
  const statusLabel = () => commandStatusLabel(source())
  const showStatusHeader = () => !props.context?.completionHeader && statusLabel() !== 'Success'

  // When the command produced no output, surface a "[no output]" placeholder
  // alongside whatever metadata we have (duration, exit code). Without this
  // the bubble is a visually-empty <div> for any successful command that
  // wrote nothing to stdout/stderr.
  const emptyOutputHint = createMemo(() => {
    if (normalized())
      return null
    const dur = props.source.durationMs
    const exit = props.source.exitCode
    return joinMetaParts([
      props.source.outputUnavailable ? '[output unavailable]' : '[no output]',
      typeof dur === 'number' && formatDuration(dur),
      typeof exit === 'number' && `exit ${exit}`,
    ])
  })

  const content = () => (
    <>
      <Show
        when={normalized()}
        fallback={<Show when={emptyOutputHint()}>{hint => <div class={toolInputSummary}>{hint()}</div>}</Show>}
      >
        <CollapsibleContent kind="ansi-or-pre" text={normalized()} display={display()} isCollapsed={isCollapsed()} context={props.context} />
      </Show>
      <Show when={props.source.truncated}>
        <div class={toolInputSummary}>[output truncated]</div>
      </Show>
    </>
  )

  // Keep the status branch under <Show> so it re-runs when isError or
  // exitCode changes.
  return (
    <Show
      when={showStatusHeader()}
      fallback={(
        <div class={toolMessage} data-tool-message>
          {content()}
        </div>
      )}
    >
      <ToolStatusHeader icon={statusIcon()} title={statusLabel()} dataToolMessage>
        {content()}
      </ToolStatusHeader>
    </Show>
  )
}
