// ACP = Agent Control Protocol — the shared message shape used by opencode,
// gemini, kilo, goose, cursor, and copilot. This module routes ACP messages
// to the canonical ACP renderers in `./acpRenderers/` and exposes helpers
// for collecting tool output text.

import type { JSX } from 'solid-js'
import type { MessageCategory } from '../../messageClassification'
import type { RenderContext } from '../../messageRenderers'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import type { ContentBlock } from '~/lib/contentBlocks'
import { joinContentParagraphs } from '~/lib/contentBlocks'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { ACP_SESSION_UPDATE } from '~/types/toolMessages'
import { PlanExecutionMessage, UserContentMessage } from '../../messageRenderers'
import {
  acpAgentMessageRenderer,
  acpPlanRenderer,
  acpResultDividerRenderer,
  acpThoughtRenderer,
  acpToolCallRenderer,
  acpToolCallUpdateRenderer,
} from './renderers'

// ACP rawInput field aliases — agents emit camelCase, snake_case, or the
// short `path` form interchangeably. Extractors fall through these in order.
export const ACP_FILE_PATH_KEYS = ['filePath', 'path', 'file_path'] as const
export const ACP_OLD_TEXT_KEYS = ['oldText', 'oldString', 'old_string'] as const
export const ACP_NEW_TEXT_KEYS = ['newText', 'newString', 'new_string'] as const

export function renderACPMessage(category: MessageCategory, parsed: unknown, role: MessageRole, context?: RenderContext): JSX.Element | null {
  if (category.kind === 'assistant_text')
    return acpAgentMessageRenderer(parsed)
  if (category.kind === 'assistant_thinking')
    return acpThoughtRenderer(parsed, role, context)
  if (category.kind === 'result_divider')
    return acpResultDividerRenderer(parsed)
  if (category.kind === 'tool_use') {
    const cat = category as { toolName: string, toolUse: Record<string, unknown> }
    if (cat.toolName === ACP_SESSION_UPDATE.PLAN)
      return acpPlanRenderer(cat.toolUse, role, context)
    if (cat.toolUse.sessionUpdate === ACP_SESSION_UPDATE.TOOL_CALL_UPDATE)
      return acpToolCallUpdateRenderer(cat.toolUse, role, context)
    return acpToolCallRenderer(cat.toolUse, role, context)
  }
  if (category.kind === 'user_content')
    return <UserContentMessage parsed={parsed} />
  if (category.kind === 'plan_execution') {
    const obj = isObject(parsed) ? parsed as Record<string, unknown> : null
    const text = obj && typeof obj.content === 'string' ? obj.content as string : ''
    return text ? <PlanExecutionMessage text={text} context={context} /> : null
  }
  return null
}

/**
 * Pull `rawOutput.metadata` out of an ACP tool_call_update. Several ACP
 * extractors (execute / search / webFetch) start by digging out this nested
 * shape; centralize it so the wire-format navigation lives in one place.
 */
export function pickAcpRawOutputMetadata(toolUse: Record<string, unknown> | null | undefined): Record<string, unknown> | null {
  return pickObject(pickObject(toolUse, 'rawOutput'), 'metadata')
}

/**
 * Flatten ACP's nested `[{type:'content', content:{text}}, ...]` shape into
 * the canonical Anthropic-style `[{type:'text', text}, ...]` so the shared
 * {@link joinContentParagraphs} helper handles ACP content the same way it
 * handles Claude/Pi/Codex content. Non-text entries (image, diff, etc.)
 * pass through unchanged for the helper's image formatter to handle.
 */
export function flattenAcpContent(content: unknown): ContentBlock[] {
  if (!Array.isArray(content))
    return []
  return content.flatMap((item): ContentBlock[] => {
    if (!isObject(item))
      return []
    const entry = item as Record<string, unknown>
    if (entry.type === 'content' && isObject(entry.content)) {
      const inner = entry.content as Record<string, unknown>
      const text = pickString(inner, 'text')
      return text ? [{ type: 'text', text }] : []
    }
    return [entry]
  })
}

/**
 * Pull joined text out of an ACP tool_call_update's `content[]`. Falls back
 * to `rawOutput.output || rawOutput.error` when the content array yields
 * nothing.
 */
export function collectAcpToolText(toolUse: Record<string, unknown> | null | undefined): string {
  if (!toolUse)
    return ''
  const text = joinContentParagraphs(flattenAcpContent(toolUse.content), { text: 'text' })
  if (text)
    return text
  const rawOutput = pickObject(toolUse, 'rawOutput')
  if (rawOutput)
    return String(rawOutput.output || rawOutput.error || '')
  return ''
}
