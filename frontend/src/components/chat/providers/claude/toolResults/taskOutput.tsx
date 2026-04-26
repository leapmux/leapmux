/* eslint-disable solid/no-innerhtml -- HTML is produced via renderAnsi, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import Check from 'lucide-solid/icons/check'
import ClockFading from 'lucide-solid/icons/clock-fading'
import { Show } from 'solid-js'
import { pickNumber, pickString } from '~/lib/jsonPick'
import { containsAnsi, renderAnsi } from '~/lib/renderAnsi'
import { formatTaskStatus } from '../../../rendererUtils'
import { ToolStatusHeader } from '../../../results/ToolStatusHeader'
import { COLLAPSED_RESULT_ROWS } from '../../../toolRenderers'
import { toolResultCollapsed, toolResultContentAnsi, toolResultContentPre } from '../../../toolStyles.css'

/** Structured TaskOutput result view using tool_use_result.task data. */
export function TaskOutputResultView(props: {
  task: Record<string, unknown>
  fallbackContent: string
  context?: RenderContext
}): JSX.Element {
  const expanded = () => props.context?.toolResultExpanded?.() ?? false
  const status = () => pickString(props.task, 'status')
  const statusLabel = () => formatTaskStatus(status() || undefined)
  const description = () => pickString(props.task, 'description')
  const taskId = () => pickString(props.task, 'task_id')
  const exitCode = () => pickNumber(props.task, 'exitCode')
  const output = () => pickString(props.task, 'output', props.fallbackContent)
  const icon = () => status() === 'completed' ? Check : ClockFading

  const meta = () => {
    const parts: string[] = []
    if (taskId())
      parts.push(`task ID: ${taskId()}`)
    if (exitCode() !== null)
      parts.push(`exit code: ${exitCode()}`)
    return parts.length > 0 ? ` (${parts.join(' · ')})` : ''
  }

  const title = () => {
    const label = statusLabel()
    const desc = description()
    if (label && desc)
      return `${label}: ${desc}${meta()}`
    if (label)
      return `${label}${meta()}`
    if (desc)
      return `${desc}${meta()}`
    return `TaskOutput${meta()}`
  }

  const outputLines = () => output().split('\n')
  const isCollapsed = () => !expanded() && outputLines().length > COLLAPSED_RESULT_ROWS
  const displayOutput = () => {
    if (!isCollapsed())
      return output()
    return outputLines().slice(0, COLLAPSED_RESULT_ROWS).join('\n')
  }

  return (
    <ToolStatusHeader icon={icon()} title={title()}>
      <Show when={displayOutput()}>
        {containsAnsi(output())
          ? <div class={`${toolResultContentAnsi}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`} innerHTML={renderAnsi(displayOutput())} />
          : <div class={`${toolResultContentPre}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`}>{displayOutput()}</div>}
      </Show>
    </ToolStatusHeader>
  )
}
