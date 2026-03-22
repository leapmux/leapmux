/* eslint-disable solid/no-innerhtml -- HTML is produced from user/assistant text via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { MessageContentRenderer, RenderContext } from './messageRenderers'
import Hand from 'lucide-solid/icons/hand'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import Stamp from 'lucide-solid/icons/stamp'
import { Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { inlineFlex } from '~/styles/shared.css'
import { markdownContent } from './markdownContent.css'
import { getAssistantContent, isObject } from './messageUtils'
import { ToolUseLayout } from './toolRenderers'
import {
  toolInputText,
  toolUseHeader,
  toolUseIcon,
} from './toolStyles.css'

/** Render ExitPlanMode tool_use with the plan from input.plan as a markdown document. */
export function renderExitPlanMode(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element {
  const input = toolUse.input
  const planText = isObject(input) ? String((input as Record<string, unknown>).plan || '') : ''

  // Control response info is no longer available from thread children.
  // ExitPlanMode tool_use just shows the plan; the control_response
  // and tool_result are now rendered as separate messages in the timeline.
  const effectiveCr = (): { action: string, comment: string } | undefined => undefined

  const summary = undefined

  return (
    <ToolUseLayout
      icon={PlaneTakeoff}
      toolName="ExitPlanMode"
      title="Leaving Plan Mode"
      summary={summary}
      alwaysVisible={true}
      bordered={false}
      context={context}
    >
      <>
        <Show when={planText}>
          <hr />
          <div class={markdownContent} style={{ 'font-size': 'var(--text-regular)' }} innerHTML={renderMarkdown(planText)} />
        </Show>
        <Show when={effectiveCr()}>
          {cr => (
            <>
              <hr />
              <div class={toolUseHeader}>
                <span class={inlineFlex}>
                  {cr().action === 'approved'
                    ? <Icon icon={Stamp} size="md" class={toolUseIcon} />
                    : <Icon icon={Hand} size="md" class={toolUseIcon} />}
                </span>
                <span class={toolInputText}>
                  {cr().action === 'approved' ? 'Approved' : cr().comment ? 'Sent feedback' : 'Rejected'}
                </span>
              </div>
              <Show when={cr().action !== 'approved' && cr().comment}>
                <div class={markdownContent} innerHTML={renderMarkdown(cr().comment)} />
              </Show>
            </>
          )}
        </Show>
      </>
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
