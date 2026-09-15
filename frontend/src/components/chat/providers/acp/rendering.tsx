// Render Agent Client Protocol messages through the shared tool components.

import type { JSX } from 'solid-js'
import type { MessageCategory } from '../../messageClassification'
import type { RenderContext } from '../../messageRenderers'
import type { ACPToolAdapter } from './toolPresentation'
import { isObject } from '~/lib/jsonPick'
import { ACP_SESSION_UPDATE } from '~/types/toolMessages'
import { PlanExecutionMessage, UserContentMessage } from '../../messageRenderers'
import { acpPlanRenderer, acpToolCallUpdateRenderer } from './renderers'
import { parsedACPToolCall } from './toolPresentation'

/**
 * Render one Agent Client Protocol row.
 *
 * Assistant text and reasoning are absent on purpose. LeapMux assembles a run of
 * text chunks into ONE row that carries the shared assembled-message envelope, and
 * `renderMessageContent` draws that envelope before it reaches any plugin.
 */
export function renderACPMessage(category: MessageCategory, parsed: unknown, context?: RenderContext, adapter?: ACPToolAdapter): JSX.Element | null {
  if (category.kind === 'tool_use') {
    const cat = category as { toolName: string, toolUse: Record<string, unknown> }
    if (cat.toolName === ACP_SESSION_UPDATE.PLAN)
      return acpPlanRenderer(cat.toolUse, context)
    return acpToolCallUpdateRenderer(parsedACPToolCall(parsed) ?? cat.toolUse, context, adapter)
  }
  if (category.kind === 'user_content')
    return <UserContentMessage parsed={parsed} context={context} />
  if (category.kind === 'plan_execution') {
    const obj = isObject(parsed) ? parsed as Record<string, unknown> : null
    const text = obj && typeof obj.content === 'string' ? obj.content as string : ''
    return text ? <PlanExecutionMessage text={text} context={context} /> : null
  }
  return null
}
