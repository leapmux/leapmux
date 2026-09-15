import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import Check from 'lucide-solid/icons/check'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import ClockFading from 'lucide-solid/icons/clock-fading'
import OctagonX from 'lucide-solid/icons/octagon-x'
import { Show } from 'solid-js'
import { getToolResultExpanded } from '../messageRenderers'
import { COLLAPSED_RESULT_ROWS, hasMoreLinesThan } from './collapse'
import { CollapsibleContent } from './CollapsibleContent'
import { CommandInputBody } from './multiLineCommandBody'
import { ToolStatusHeader } from './ToolStatusHeader'
import { useCollapsedLines } from './useCollapsedLines'

/**
 * A tool result that states an OUTCOME of its own instead of returning data.
 *
 * A background task that stopped, a message that reached a peer, a retrieval that
 * timed out: each one answers with a state and a short note, and neither the command
 * body nor the plain text body states that state. The row draws the state as its own
 * header, so the reader gets the answer before the note.
 *
 * The four outcomes are a closed set, because the icon table below reads them. They
 * are not the tool-row outcome of `toolOutcomeLabel`, which says how the CALL ended: a
 * call that succeeded can report a task that stopped.
 */
export interface StatusResultSource {
  /** The state, in the words of the surface that reports it. */
  title: string
  outcome: 'succeeded' | 'failed' | 'waiting' | 'stopped'
  /** A command the reported operation ran. The row draws it above the note. */
  command?: string
  output: string
}

const OUTCOME_ICON = { succeeded: Check, failed: CircleAlert, waiting: ClockFading, stopped: OctagonX }

/** Whether the note holds more than the collapsed row shows. */
export function statusResultCollapsible(source: StatusResultSource): boolean {
  return hasMoreLinesThan(source.output, COLLAPSED_RESULT_ROWS)
}

/** Draw the reported state, the command it acted on, and the note below both. */
export function StatusResultBody(props: { source: StatusResultSource, context?: RenderContext }): JSX.Element {
  const output = () => props.source.output
  const collapsed = useCollapsedLines({ text: output, expanded: () => getToolResultExpanded(props.context) })
  return (
    <ToolStatusHeader icon={OUTCOME_ICON[props.source.outcome]} title={props.source.title}>
      <Show when={props.source.command}>
        {command => <CommandInputBody command={command()} context={props.context} />}
      </Show>
      <Show when={output()}>
        <CollapsibleContent kind="ansi-or-pre" text={output()} display={collapsed.display()} isCollapsed={collapsed.isCollapsed()} context={props.context} />
      </Show>
    </ToolStatusHeader>
  )
}
