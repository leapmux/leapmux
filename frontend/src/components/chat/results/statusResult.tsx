import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import type { TaskResult, TaskStatus } from '../model/tools/task'
import type { ToolResultRenderContext } from '../renderContext'
import ClockFading from 'lucide-solid/icons/clock-fading'
import { Show } from 'solid-js'
import { getToolResultExpanded } from '../messageRenderers'
import { toolMessage } from '../toolStyles.css'
import { CollapsibleContent } from './CollapsibleContent'
import { ENDED_OUTCOME_ICON } from './endedOutcomeIcon'
import { CommandInputBody } from './multiLineCommandBody'
import { ToolStatusHeader } from './ToolStatusHeader'
import { textNeedsCollapse, useCollapsedLines } from './useCollapsedLines'

/**
 * Specify one icon for each task outcome.
 * The Record type rejects a new outcome without its icon.
 * Ended outcomes share the agent card icon table.
 */
const OUTCOME_ICON: Record<TaskStatus, LucideIcon> = {
  ...ENDED_OUTCOME_ICON,
  // Task cards show an icon while they run.
  // Agent cards use an absent ended icon to identify that same unfinished state.
  running: ClockFading,
}

/** Whether a task note exceeds the collapsed display. */
export function taskResultCollapsible(result: TaskResult): boolean {
  return textNeedsCollapse(result.output)
}

export function StatusResultBody(props: { source: TaskResult, context?: ToolResultRenderContext }): JSX.Element {
  const output = () => props.source.output
  const collapsed = useCollapsedLines({ text: output, expanded: () => getToolResultExpanded(props.context) })
  const body = () => (
    <>
      <Show when={props.source.command}>
        {command => <CommandInputBody command={command()} {...(props.context !== undefined ? { context: props.context } : {})} />}
      </Show>
      <Show when={output()}>
        <CollapsibleContent outputPreview kind="ansi-or-pre" text={output()} display={collapsed.display()} isCollapsed={collapsed.isCollapsed()} {...(props.context !== undefined ? { context: props.context } : {})} />
      </Show>
    </>
  )
  // Draw a status header only when the native result supplies its title.
  // Otherwise, keep the output wrapper and let the row state the call's outcome.
  return (
    <Show when={props.source.title} fallback={<div class={toolMessage}>{body()}</div>}>
      {title => (
        <ToolStatusHeader icon={OUTCOME_ICON[props.source.outcome]} title={title()}>
          {body()}
        </ToolStatusHeader>
      )}
    </Show>
  )
}
