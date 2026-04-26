/* eslint-disable solid/no-innerhtml -- HTML is produced via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import { Show } from 'solid-js'
import { useCopyButton } from '~/hooks/useCopyButton'
import { isObject } from '~/lib/jsonPick'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { markdownContent } from '../../../markdownEditor/markdownContent.css'
import { ToolUseLayout } from '../../../toolRenderers'

/** Render ExitPlanMode tool_use with the plan from input.plan as a markdown document. */
export function renderExitPlanMode(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element {
  const input = toolUse.input
  const planText = isObject(input) ? String((input as Record<string, unknown>).plan || '') : ''
  const { copied, copy } = useCopyButton(() => planText || undefined)
  const reply = planText && context?.onReply ? () => context.onReply!(planText) : undefined

  return (
    <ToolUseLayout
      icon={PlaneTakeoff}
      toolName="ExitPlanMode"
      title="Leaving Plan Mode"
      alwaysVisible={true}
      bordered={false}
      context={context}
      headerActions={{
        onReply: reply,
        onCopyMarkdown: planText ? copy : undefined,
        markdownCopied: copied(),
      }}
    >
      <Show when={planText}>
        <hr />
        <div class={markdownContent} style={{ 'font-size': 'var(--text-regular)' }} innerHTML={renderMarkdown(planText)} />
      </Show>
    </ToolUseLayout>
  )
}
