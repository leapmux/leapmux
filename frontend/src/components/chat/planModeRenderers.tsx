/* eslint-disable solid/no-innerhtml -- HTML is produced from user/assistant text via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { MessageContentRenderer, RenderContext } from './messageRenderers'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import { Show } from 'solid-js'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { markdownContent } from './markdownContent.css'
import { getAssistantContent, isObject } from './messageUtils'
import { ToolUseLayout } from './toolRenderers'

/** Render ExitPlanMode tool_use with the plan from input.plan as a markdown document. */
export function renderExitPlanMode(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element {
  const input = toolUse.input
  const planText = isObject(input) ? String((input as Record<string, unknown>).plan || '') : ''

  return (
    <ToolUseLayout
      icon={PlaneTakeoff}
      toolName="ExitPlanMode"
      title="Leaving Plan Mode"
      alwaysVisible={true}
      bordered={false}
      context={context}
    >
      <Show when={planText}>
        <hr />
        <div class={markdownContent} style={{ 'font-size': 'var(--text-regular)' }} innerHTML={renderMarkdown(planText)} />
      </Show>
    </ToolUseLayout>
  )
}

export const exitPlanModeRenderer: MessageContentRenderer = {
  render(parsed, _role, context) {
    const content = getAssistantContent(parsed)
    if (!content)
      return null
    const toolUse = content.find(c => isObject(c) && c.type === 'tool_use' && c.name === 'ExitPlanMode')
    if (!toolUse)
      return null
    return renderExitPlanMode(toolUse as Record<string, unknown>, context)
  },
}
