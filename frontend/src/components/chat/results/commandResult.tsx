import type { JSX } from 'solid-js'
import type { CommandExit, CommandResult } from '../model/commandResult'
import type { ToolCallStatus } from '../model/toolCallStatus'
import type { ToolResultRenderContext } from '../renderContext'
import Ban from 'lucide-solid/icons/ban'
import Check from 'lucide-solid/icons/check'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import Terminal from 'lucide-solid/icons/terminal'
import { createMemo, For, Show } from 'solid-js'
import { normalizedCommandBody, PROGRESS_MAX_ROWS } from '~/lib/normalizeProgressOutput'
import { getToolResultExpanded } from '../messageRenderers'
import { commandExit, commandIsError } from '../model/commandResult'
import { isFinishedToolCallStatus } from '../model/toolCallStatus'
import { formatDuration, joinMetaParts } from '../rendererUtils'
import { toolInputSummary, toolMessage } from '../toolStyles.css'
import { TRUNCATION_NOTICE } from '../truncationNotice'
import { COLLAPSED_RESULT_ROWS } from './collapse'
import { CollapsibleContent } from './CollapsibleContent'
import { EMPTY_RESULT_NOTICE } from './emptyResultNotice'
import { toolOutcomeLabel } from './toolOutcomeLabel'
import { drawsOwnOutcome, ToolHeaderRow, ToolStatusHeader } from './ToolStatusHeader'
import { textNeedsCollapse, useCollapsedLines } from './useCollapsedLines'

const normalizedByCommand = new WeakMap<CommandResult, ReturnType<typeof normalizedCommandBody>>()
const MAX_CACHED_NORMALIZED_CHARS = 4 * 1024 * 1024

export function commandCollapseThreshold(hadCarriageReturns: boolean): number {
  return hadCarriageReturns ? PROGRESS_MAX_ROWS : COLLAPSED_RESULT_ROWS
}

export function normalizedCommandOutput(command: CommandResult): ReturnType<typeof normalizedCommandBody> {
  const cached = normalizedByCommand.get(command)
  if (cached !== undefined)
    return cached
  const normalized = normalizedCommandBody(command.output)
  if (normalized.text.length <= MAX_CACHED_NORMALIZED_CHARS)
    normalizedByCommand.set(command, normalized)
  return normalized
}

export function commandOutputIsCollapsible(command: CommandResult): boolean {
  const { text, hadCarriageReturns } = normalizedCommandOutput(command)
  return textNeedsCollapse(text, commandCollapseThreshold(hadCarriageReturns))
}

export function commandStatusLabel(status: ToolCallStatus, exit: CommandExit): string {
  if (status === 'declined')
    return toolOutcomeLabel('declined')
  if (status === 'cancelled')
    return toolOutcomeLabel('interrupted')
  if (typeof exit.exitCode === 'number' && exit.exitCode !== 0)
    return toolOutcomeLabel('failed', `exit ${exit.exitCode}`)
  if (exit.signal)
    return toolOutcomeLabel('failed', exit.signal)
  if (exit.failed || status === 'failed')
    return toolOutcomeLabel('failed')
  return toolOutcomeLabel('succeeded')
}

/** Keep separate process output and status when one tool call owns several commands. */
export function CommandResultList(props: { entries: CommandResult[], status: ToolCallStatus, context?: ToolResultRenderContext }): JSX.Element {
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
  status: ToolCallStatus
  context?: ToolResultRenderContext
}): JSX.Element {
  // Normalize carriage-return overwrites before removing leading blank lines.
  // The body and toolbar reuse the same normalized command text.
  const body = createMemo(() => normalizedCommandOutput(props.source))
  const normalized = createMemo(() => body().text)
  const expanded = () => getToolResultExpanded(props.context)
  // Carriage-return normalization already limits process rows.
  // Use that row threshold so ordinary collapse does not remove the retained tail or omission notice.
  const { display, isCollapsed } = useCollapsedLines({
    text: normalized,
    expanded,
    threshold: () => commandCollapseThreshold(body().hadCarriageReturns),
  })
  // A declined call uses the refusal icon.
  // A completed call can still report a failed process exit.
  // The icon and label must read that exit also.
  const exit = () => commandExit(props.source)
  const commandFailed = () => props.status === 'failed' || commandIsError(exit())
  const statusIcon = () => props.status === 'declined' ? Ban : props.status === 'cancelled' || commandFailed() ? CircleAlert : Check
  const statusLabel = () => commandStatusLabel(props.status, exit())
  // Use the shared outcome vocabulary.
  // The status-header condition must use the same success word as the label.
  const showStatusHeader = () => drawsOwnOutcome(props.context) && statusLabel() !== toolOutcomeLabel('succeeded')

  // A finished command with no output still needs a visible empty-output notice.
  // Include its duration, exit code, or signal when the source supplies one.
  const emptyOutputHint = createMemo(() => {
    if (normalized())
      return null
    // An unfinished call can still receive output.
    // It must not claim an empty completed stream.
    if (!isFinishedToolCallStatus(props.status))
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
        <CollapsibleContent outputPreview kind="ansi-or-pre" text={normalized()} display={display()} isCollapsed={isCollapsed()} {...(props.context !== undefined ? { context: props.context } : {})} />
      </Show>
      <Show when={props.source.truncated}>
        <div class={toolInputSummary}>{TRUNCATION_NOTICE}</div>
      </Show>
    </>
  )

  // Keep the status branch reactive when the call status or process exit changes.
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
