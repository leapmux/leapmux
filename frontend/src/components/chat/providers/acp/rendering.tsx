// ACP = Agent Control Protocol — the shared message shape used by opencode,
// gemini, kilo, goose, cursor, and copilot. This module routes ACP messages
// to the canonical ACP renderers in `./acpRenderers/` and exposes helpers
// for collecting tool output text.

import type { JSX } from 'solid-js'
import type { MessageCategory } from '../../messageClassification'
import type { RenderContext } from '../../messageRenderers'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { isObject, pickObject } from '~/lib/jsonPick'
import { ACP_SESSION_UPDATE } from '~/types/toolMessages'
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
 * Walk an ACP tool_call_update's `content[]` for `{type:'content', content:{text}}`
 * entries and concatenate their text. If the array yields nothing, fall back to
 * `rawOutput.output || rawOutput.error`. Returns an empty string when neither
 * source has text.
 */
export function collectAcpToolText(toolUse: Record<string, unknown> | null | undefined): string {
  if (!toolUse)
    return ''
  const contentArr = toolUse.content as unknown[] | undefined
  let text = ''
  if (Array.isArray(contentArr)) {
    for (const item of contentArr) {
      if (!isObject(item))
        continue
      const entry = item as Record<string, unknown>
      if (entry.type === 'content' && isObject(entry.content)) {
        const ct = entry.content as Record<string, unknown>
        text += String(ct.text || '')
      }
    }
  }
  if (text)
    return text
  const rawOutput = pickObject(toolUse, 'rawOutput')
  if (rawOutput)
    return String(rawOutput.output || rawOutput.error || '')
  return ''
}
