/* eslint-disable solid/no-innerhtml -- HTML is produced from user/assistant text via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { MessageContentRenderer, RenderContext } from './messageRenderers'
import Hand from 'lucide-solid/icons/hand'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import Stamp from 'lucide-solid/icons/stamp'
import TicketsPlane from 'lucide-solid/icons/tickets-plane'
import { Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { inlineFlex } from '~/styles/shared.css'
import { markdownContent } from './markdownContent.css'
import { getAssistantContent, isObject, relativizePath } from './messageUtils'
import { ToolUseLayout } from './toolRenderers'
import {
  toolInputSubDetail,
  toolInputText,
  toolUseHeader,
  toolUseIcon,
} from './toolStyles.css'

/** Render EnterPlanMode tool_use as a simple "Entering Plan Mode" text line. */
export function renderEnterPlanMode(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element {
  void toolUse
  return (
    <ToolUseLayout
      icon={TicketsPlane}
      toolName="EnterPlanMode"
      title="Entering Plan Mode"
      context={context}
    />
  )
}

/** Render ExitPlanMode tool_use with the plan from input.plan as a markdown document. */
export function renderExitPlanMode(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element {
  const input = toolUse.input
  const planText = isObject(input) ? String((input as Record<string, unknown>).plan || '') : ''

  // Derive effective control response: prefer the explicit controlResponse
  // threaded by the backend; fall back to tool_result-based detection for
  // data where the controlResponse was lost to the pre-fix race condition.
  const effectiveCr = (): { action: string, comment: string } | undefined => {
    if (context?.childControlResponse)
      return context.childControlResponse
    const resultContent = context?.childResultContent
    if (!resultContent)
      return undefined
    if (context?.childResultIsError === true)
      return { action: 'rejected', comment: resultContent }
    if (context?.childResultIsError === false || resultContent.toLowerCase().includes('approved your plan'))
      return { action: 'approved', comment: '' }
    return { action: 'rejected', comment: resultContent }
  }

  const summary = context?.childFilePath
    ? <div class={toolInputSubDetail}>{relativizePath(context.childFilePath, context?.workingDir, context?.homeDir)}</div>
    : undefined

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
          <div class={markdownContent} innerHTML={renderMarkdown(planText)} />
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

export const enterPlanModeRenderer: MessageContentRenderer = {
  render(parsed, _role, context) {
    const content = getAssistantContent(parsed)
    if (!content)
      return null
    const toolUse = content.find(c => isObject(c) && c.type === 'tool_use' && c.name === 'EnterPlanMode')
    if (!toolUse)
      return null
    return renderEnterPlanMode(toolUse as Record<string, unknown>, context)
  },
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
