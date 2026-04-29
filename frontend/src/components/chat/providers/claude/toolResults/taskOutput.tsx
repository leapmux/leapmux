import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import Check from 'lucide-solid/icons/check'
import ClockFading from 'lucide-solid/icons/clock-fading'
import { Show } from 'solid-js'
import { pickNumber, pickString } from '~/lib/jsonPick'
import { getToolResultExpanded } from '../../../messageRenderers'
import { formatTaskStatus, joinMetaParts } from '../../../rendererUtils'
import { CollapsibleContent } from '../../../results/CollapsibleContent'
import { ToolStatusHeader } from '../../../results/ToolStatusHeader'
import { useCollapsedLines } from '../../../results/useCollapsedLines'

/** Structured TaskOutput result view using tool_use_result.task data. */
export function TaskOutputResultView(props: {
  task: Record<string, unknown>
  fallbackContent: string
  context?: RenderContext
}): JSX.Element {
  const expanded = () => getToolResultExpanded(props.context)
  const status = () => pickString(props.task, 'status')
  const statusLabel = () => formatTaskStatus(status() || undefined)
  const description = () => pickString(props.task, 'description')
  const taskId = () => pickString(props.task, 'task_id')
  const exitCode = () => pickNumber(props.task, 'exitCode')
  const output = () => pickString(props.task, 'output', props.fallbackContent)
  const icon = () => status() === 'completed' ? Check : ClockFading

  const meta = () => {
    const inner = joinMetaParts([
      taskId() && `task ID: ${taskId()}`,
      exitCode() !== null && `exit code: ${exitCode()}`,
    ])
    return inner ? ` (${inner})` : ''
  }

  const title = () => {
    const label = statusLabel()
    const desc = description()
    const body = label && desc ? `${label}: ${desc}` : (label || desc || 'TaskOutput')
    return `${body}${meta()}`
  }

  const { display, isCollapsed } = useCollapsedLines({ text: output, expanded })

  return (
    <ToolStatusHeader icon={icon()} title={title()}>
      <Show when={display()}>
        <CollapsibleContent kind="ansi-or-pre" text={output()} display={display()} isCollapsed={isCollapsed()} />
      </Show>
    </ToolStatusHeader>
  )
}
