import type { ToolSpanSides } from '~/components/chat/rowExtractionTypes'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ContentBlock } from '~/lib/contentBlocks'
import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { TodoItem } from '~/models/todo'
import { splitToolResultContent } from '~/lib/contentBlocks'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { canonicalClaudeToolName } from '../toolKinds'
import { extractToolUseInfo, getMessageContentArray } from './assistantContent'
import { claudeImagesFromToolResult } from './image'

/** Everything one Claude call reads beyond its own bytes. */
export interface ClaudeRowContext {
  /** The paired result's `tool_use_result`, which the three `Task*` rows read. */
  pairedResult?: Record<string, unknown>
  /** The live to-do store, which a status-only `TaskUpdate` patch reads. */
  todoById?: (taskId: string) => TodoItem | undefined
  /** LeapMux's own reading of how the row ended, which a provider frame can contradict. */
  completion?: MessageCompletion
}

/**
 * One side of one Claude tool span, read out of the Anthropic envelope.
 *
 * Claude states a call across two messages of different shapes: an `assistant`
 * row whose `message.content[]` holds a `tool_use` block, and a `user` row whose
 * `message.content[]` holds the matching `tool_result` block plus a structured
 * `tool_use_result` beside it. This is both of them in one shape, so the
 * presentation table below reads fields rather than envelopes.
 */
export interface ClaudeToolRow {
  /** The span id: the `tool_use.id` that both sides carry. */
  id: string
  /** Which of the two messages this row was built from. */
  role: 'request' | 'result'
  /** The tool name, after {@link canonicalClaudeToolName} folded its aliases. */
  toolName: string
  /** The call's arguments, from this row's own `tool_use` block or its paired one. */
  input: Record<string, unknown>
  /** The structured result payload, which only a result row carries. */
  toolUseResult: Record<string, unknown> | undefined
  /** The text of the `tool_result` block, with its image blocks taken out. */
  resultContent: string
  /** The raw `tool_result.content`, which the MCP body reads as blocks. */
  rawResultContent: unknown
  images: ImageResultSource[]
  /** The tool reported a failure. Undefined when the row states nothing. */
  isError: boolean | undefined
}

const TOOL_USE_ERROR_RE = /<tool_use_error>([\s\S]*?)<\/tool_use_error>/

/**
 * The message inside a `<tool_use_error>` wrapper, or the text unchanged.
 *
 * Claude Code wraps a tool failure in that tag pair so the MODEL can see where
 * the error starts. A reader gets the markup and nothing else from it, and the
 * row already states the failure in its own header.
 */
function unwrapToolUseError(text: string): string {
  const match = TOOL_USE_ERROR_RE.exec(text)
  // The group always participates in a match of this regex; `?? ''` is the
  // type-level guard alone.
  return match ? (match[1] ?? '').trim() : text
}

/** The first `tool_use` block of a Claude assistant envelope, if it holds one. */
function toolUseBlock(content: ContentBlock[] | null): Record<string, unknown> | null {
  const block = content?.find(item => isObject(item) && item.type === 'tool_use')
  return block ?? null
}

/** The first `tool_result` block of a Claude user envelope, if it holds one. */
function toolResultBlock(content: ContentBlock[] | null): Record<string, unknown> | null {
  const block = content?.find(item => isObject(item) && item.type === 'tool_result')
  return block ?? null
}

/**
 * Read one Claude message into {@link ClaudeToolRow}, or null when it is no tool
 * row at all.
 *
 * The paired sibling fills only what this row's own bytes cannot state. A RESULT
 * row carries no arguments, so its title, its diff and its read range all come
 * from the request beside it; a REQUEST row carries no output. `spanType` is the
 * worker's own column and states the tool on every span row, which is what lets a
 * result row state its tool with no sibling resolved at all.
 */
export function claudeToolRow(
  parsed: ParsedMessageContent | undefined,
  spanType: string | undefined,
  sides: ToolSpanSides,
): ClaudeToolRow | null {
  const payload = parsed?.parentObject
  if (!payload)
    return null
  const content = getMessageContentArray(payload)
  const useBlock = toolUseBlock(content)
  const resultBlock = useBlock ? null : toolResultBlock(content)
  if (!useBlock && !resultBlock)
    return null

  const toolUseResult = pickObject(payload, 'tool_use_result') ?? undefined
  const id = useBlock ? pickString(useBlock, 'id') : pickString(resultBlock, 'tool_use_id')
  // The paired REQUEST, matched by this row's own tool-use id. An unmatched
  // request belongs to another call in the same turn, and reading its arguments
  // would title this row with that call's file.
  const paired = useBlock ? null : (sides.request ? extractToolUseInfo(sides.request, id || undefined) : null)
  const rawName = useBlock
    ? pickString(useBlock, 'name')
    : (spanType || pickString(toolUseResult, 'tool_name') || paired?.toolName || '')

  if (useBlock) {
    return {
      id,
      role: 'request',
      toolName: canonicalClaudeToolName(rawName),
      input: pickObject(useBlock, 'input', {}),
      toolUseResult: undefined,
      resultContent: '',
      rawResultContent: undefined,
      images: [],
      isError: undefined,
    }
  }

  const rawResultContent = resultBlock!.content
  const split = Array.isArray(rawResultContent)
    ? splitToolResultContent(rawResultContent, { text: 'text' })
    : { text: String(rawResultContent ?? ''), images: [] }
  const input = paired?.input ?? {}
  const resultContent = unwrapToolUseError(split.text)
  return {
    id,
    role: 'result',
    toolName: canonicalClaudeToolName(rawName),
    input,
    toolUseResult,
    resultContent,
    rawResultContent,
    // The blocks give order and payload; `tool_use_result` gives dimensions when
    // the tool has a structured result, and wins for that reason. The record
    // rides only when this row carries one, so the absent key stays absent.
    images: claudeImagesFromToolResult({
      ...(toolUseResult !== undefined ? { toolUseResult } : {}),
      blockImages: split.images,
      toolInput: input,
    }),
    isError: typeof resultBlock!.is_error === 'boolean' ? resultBlock!.is_error : undefined,
  }
}
