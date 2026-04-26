/* eslint-disable solid/no-innerhtml -- HTML is produced via remark, not arbitrary user input */
import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import { useCopyButton } from '~/hooks/useCopyButton'
import { isObject } from '~/lib/jsonPick'
import { renderMarkdown } from '~/lib/renderMarkdown'
import { markdownContent } from '../../../markdownEditor/markdownContent.css'
import { TodoListMessage } from '../../../todoListMessage'
import { ToolUseLayout } from '../../../toolRenderers'
import { codexPlanItemMarkdown, codexTurnPlanFromParams } from '../extractors/plan'
import { extractItem } from '../renderHelpers'

function extractPlanParams(parsed: unknown): Record<string, unknown> | null {
  if (!isObject(parsed))
    return null
  if (parsed.method === 'turn/plan/updated' && isObject(parsed.params))
    return parsed.params as Record<string, unknown>
  if (Array.isArray(parsed.plan))
    return parsed
  return null
}

/** Renders Codex plan items using ToolUseLayout without a bubble (same pattern as ExitPlanMode). */
export function codexPlanRenderer(parsed: unknown, _role: MessageRole, context?: RenderContext): JSX.Element | null {
  const item = extractItem(parsed)
  const text = codexPlanItemMarkdown(item)
  if (!text)
    return null
  const { copied, copy } = useCopyButton(() => text)
  const reply = context?.onReply ? () => context.onReply!(text) : undefined
  return (
    <ToolUseLayout
      icon={PlaneTakeoff}
      toolName="Plan"
      title="Proposed Plan"
      alwaysVisible={true}
      bordered={false}
      context={context}
      headerActions={{
        onReply: reply,
        onCopyMarkdown: copy,
        markdownCopied: copied(),
      }}
    >
      <hr />
      <div class={markdownContent} style={{ 'font-size': 'var(--text-regular)' }} innerHTML={renderMarkdown(text)} />
    </ToolUseLayout>
  )
}

/** Renders Codex turn/plan/updated notifications with the same todo-list UI pattern as TodoWrite. */
export function codexTurnPlanRenderer(parsed: unknown, _role: MessageRole, context?: RenderContext): JSX.Element | null {
  const params = extractPlanParams(parsed)
  const source = codexTurnPlanFromParams(params)
  if (!source)
    return null
  return <TodoListMessage source={source} context={context} />
}
