import type { JSX } from 'solid-js'
import type { CommandResult } from '../ir/commandResult'
import type { ToolRowStatus } from '../ir/toolRowStatus'
import type { RenderContext } from '../messageRenderers'
import Ban from 'lucide-solid/icons/ban'
import Check from 'lucide-solid/icons/check'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import Terminal from 'lucide-solid/icons/terminal'
import { createMemo, For, Show } from 'solid-js'
import { commandCollapseThreshold, commandExit, commandIsError, commandStatusLabel, normalizedCommandOutput } from '../ir/commandResult'
import { toolOutcomeLabel } from '../ir/toolOutcomeLabel'
import { isFinishedToolStatus } from '../ir/toolRowStatus'
import { getToolResultExpanded } from '../messageRenderers'
import { formatDuration, joinMetaParts } from '../rendererUtils'
import { toolInputSummary, toolMessage } from '../toolStyles.css'
import { TRUNCATION_NOTICE } from '../truncationNotice'
import { CollapsibleContent } from './CollapsibleContent'
import { EMPTY_RESULT_NOTICE } from './emptyResultNotice'
import { drawsOwnOutcome, ToolHeaderRow, ToolStatusHeader } from './ToolStatusHeader'
import { useCollapsedLines } from './useCollapsedLines'

/** Keep separate process output and status when one tool call owns several commands. */
export function CommandResultList(props: { entries: CommandResult[], status: ToolRowStatus, context?: RenderContext }): JSX.Element {
  return (
    <For each={props.entries}>
      {entry => (
        <>
          <Show when={entry.label}>{label => <ToolHeaderRow icon={Terminal} title={label()} />}</Show>
          <CommandResultBody source={entry} status={props.status} {...(props.context !== undefined ? { context: props.context } : {})} />
        </>
      )}
    </For>
  )
}

export function CommandResultBody(props: {
  source: CommandResult
  status: ToolRowStatus
  context?: RenderContext
}): JSX.Element {
  // The shared normalize-then-strip transform (order matters: normalize CR
  // overwrites first so a leading bare `\r` becomes a `\n` that strip can then
  // trim). `normalizedCommandOutput` is the single memoized source every
  // command-result reader uses -- the body here and the toolbar's collapsibility
  // check alike -- so the two cannot drift.
  const body = createMemo(() => normalizedCommandOutput(props.source))
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
  // A refusal is not a failure, so it does not take the alert glyph: nothing went
  // wrong, and the reader is the one who stopped the call. A command that ended
  // with no error status can still report a non-zero exit code, and that word
  // belongs in the label -- so the glyph reads the exit code too.
  const exit = () => commandExit(props.source)
  const commandFailed = () => props.status === 'failed' || commandIsError(exit())
  const statusIcon = () => props.status === 'declined' ? Ban : props.status === 'cancelled' || commandFailed() ? CircleAlert : Check
  const statusLabel = () => commandStatusLabel(props.status, exit())
  // Compared against the shared vocabulary, not a literal: a word that changed in one
  // place and not the other would hide the header for every failed command.
  const showStatusHeader = () => drawsOwnOutcome(props.context) && statusLabel() !== toolOutcomeLabel('succeeded')

  // When the command produced no output, surface a "[no output]" placeholder
  // alongside whatever metadata we have (duration, exit code). Without this
  // the bubble is a visually-empty <div> for any successful command that
  // wrote nothing to stdout/stderr.
  const emptyOutputHint = createMemo(() => {
    if (normalized())
      return null
    // A call that has not returned yet has no empty output to state: the tail
    // may still arrive.
    if (!isFinishedToolStatus(props.status))
      return null
    const dur = props.source.durationMs
    const code = props.source.exitCode
    return joinMetaParts([
      props.source.outputUnavailable ? '[output unavailable]' : EMPTY_RESULT_NOTICE,
      typeof dur === 'number' && formatDuration(dur),
      typeof code === 'number' ? `exit ${code}` : props.source.signal,
    ])
  })

  const content = () => (
    <>
      <Show
        when={normalized()}
        fallback={<Show when={emptyOutputHint()}>{hint => <div class={toolInputSummary}>{hint()}</div>}</Show>}
      >
        <CollapsibleContent kind="ansi-or-pre" text={normalized()} display={display()} isCollapsed={isCollapsed()} {...(props.context !== undefined ? { context: props.context } : {})} />
      </Show>
      <Show when={props.source.truncated}>
        <div class={toolInputSummary}>{TRUNCATION_NOTICE}</div>
      </Show>
    </>
  )

  // Keep the status branch under <Show> so it re-runs when the status or
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
