// ACP = Agent Client Protocol — the shared message shape used by opencode,
// kilo, goose, cursor, copilot, and reasonix. This module routes ACP messages
// to the canonical ACP renderers in `./acpRenderers/` and exposes helpers
// for collecting tool output text.

import type { JSX } from 'solid-js'
import type { MessageCategory } from '../../messageClassification'
import type { RenderContext } from '../../messageRenderers'
import type { ContentBlock } from '~/lib/contentBlocks'
import { splitToolResultContent } from '~/lib/contentBlocks'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { ACP_SESSION_UPDATE } from '~/types/toolMessages'
import { PlanExecutionMessage, UserContentMessage } from '../../messageRenderers'
import {
  acpAgentMessageRenderer,
  acpPlanRenderer,
  acpSubagentToolRequestRenderer,
  acpThoughtRenderer,
  acpToolCallRenderer,
  acpToolCallUpdateRenderer,
} from './renderers'

// ACP rawInput field aliases — agents emit camelCase, snake_case, or the
// short `path` form interchangeably. Extractors fall through these in order.
export const ACP_FILE_PATH_KEYS = ['filePath', 'path', 'file_path'] as const
export const ACP_OLD_TEXT_KEYS = ['oldText', 'oldString', 'old_string'] as const
export const ACP_NEW_TEXT_KEYS = ['newText', 'newString', 'new_string'] as const

export function renderACPMessage(category: MessageCategory, parsed: unknown, context?: RenderContext): JSX.Element | null {
  if (category.kind === 'assistant_text')
    return acpAgentMessageRenderer(parsed, context)
  if (category.kind === 'assistant_thinking')
    return acpThoughtRenderer(parsed, context)
  if (category.kind === 'tool_use') {
    const cat = category as { toolName: string, toolUse: Record<string, unknown> }
    if (cat.toolName === ACP_SESSION_UPDATE.PLAN)
      return acpPlanRenderer(cat.toolUse, context)
    // Goose subagent tool-request (classifier sets toolName explicitly).
    if (cat.toolName === 'subagent_tool_request')
      return acpSubagentToolRequestRenderer(cat.toolUse, context)
    if (cat.toolUse.sessionUpdate === ACP_SESSION_UPDATE.TOOL_CALL_UPDATE)
      return acpToolCallUpdateRenderer(cat.toolUse, context)
    return acpToolCallRenderer(cat.toolUse, context)
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

/**
 * Pull `rawOutput.metadata` out of an ACP tool_call_update. Several ACP
 * extractors (execute / search / webFetch) start by digging out this nested
 * shape; centralize it so the wire-format navigation lives in one place.
 */
export function pickAcpRawOutputMetadata(toolUse: Record<string, unknown> | null | undefined): Record<string, unknown> | null {
  return pickObject(pickObject(toolUse, 'rawOutput'), 'metadata')
}

/**
 * Flatten ACP's nested `[{type:'content', content:{...}}, ...]` shape into the
 * canonical Anthropic-style `[{type:'text', text}, ...]` so the shared
 * {@link splitToolResultContent} and `joinContentParagraphs` helpers handle ACP
 * content the same way they handle Claude/Pi/Codex content.
 *
 * The INNER block is unwrapped whatever its kind. Unwrapping only text used to
 * mean an ACP `ImageContent` -- what OpenCode's read-on-image, Kilo's and
 * Goose's screenshots all send, as
 * `{type:'content', content:{type:'image', mimeType, data}}` -- was dropped
 * here and never reached any renderer. Top-level entries that are not a
 * `content` wrapper (`diff`, `terminal`) pass through for their own extractors.
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
      // Keyed on the `text` FIELD, not on `type: 'text'`: agents omit the
      // discriminant on a text block often enough that requiring it drops
      // ordinary command output.
      const text = pickString(inner, 'text')
      return text ? [{ type: 'text', text }] : [inner]
    }
    return [entry]
  })
}

/**
 * Pull joined text out of an ACP tool_call_update's `content[]`. Falls back
 * to `rawOutput.output || rawOutput.error` when the content array yields
 * nothing.
 *
 * Images are excluded: this text renders into a `<pre>`, where a data URL is a
 * megabyte of literal base64. `acpImagesFromToolCall` renders them as images.
 */
export function collectAcpToolText(toolUse: Record<string, unknown> | null | undefined): string {
  if (!toolUse)
    return ''
  const text = splitToolResultContent(flattenAcpContent(toolUse.content), { text: 'text' }).text
  if (text)
    return text
  const rawOutput = pickObject(toolUse, 'rawOutput')
  if (rawOutput)
    return String(rawOutput.output || rawOutput.error || '')
  return ''
}
