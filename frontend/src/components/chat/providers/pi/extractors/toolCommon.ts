import { asContentArray, joinContentParagraphs } from '~/lib/contentBlocks'
import { pickObject, pickString } from '~/lib/jsonPick'

/**
 * Pull the canonical fields off a Pi `tool_execution_*` event payload. The
 * three event types carry different fields:
 *
 *   start  → { toolCallId, toolName, args }
 *   update → { toolCallId, toolName, args, partialResult }
 *   end    → { toolCallId, toolName, result, isError }   // no args
 *
 * Callers that need both args and result on the end side reach back into
 * the matching start payload via `RenderContext.toolUseParsed`, which the
 * chat store wires up by `spanId`.
 */
export interface PiToolExecution {
  toolCallId: string
  toolName: string
  args: Record<string, unknown>
  result?: PiToolResult
  partialResult?: PiToolResult
  isError: boolean
}

export interface PiToolResult {
  text: string
  details: Record<string, unknown>
}

/**
 * Join the textual content blocks from a Pi tool result body into a
 * paragraph-separated string. Pi tool results are shaped as
 * `{content: (TextContent | ImageContent)[], details?}`. Image blocks
 * (e.g. `read` on a binary image file) are embedded as Markdown via the
 * helper's default formatter so they survive in any rendering context.
 */
export function piToolResultText(result: Record<string, unknown> | null | undefined): string {
  return joinContentParagraphs(asContentArray(result?.content), { text: 'text' })
}

export function piToolResult(result: Record<string, unknown> | null | undefined): PiToolResult {
  return {
    text: piToolResultText(result),
    details: pickObject(result ?? undefined, 'details') ?? {},
  }
}

// Memoize by payload identity. The same parsed payload is consumed by
// `piToolResultMeta`, the result-body renderer, and per-tool extractors
// (each calls `piExtractTool` again), so without a cache the content
// blocks are walked multiple times per render. WeakMap-keyed so entries
// are collected when the payload object is dropped from the chat store.
const toolCache = new WeakMap<Record<string, unknown>, PiToolExecution | null>()

/** Unwrap a tool_execution event payload into a normalized shape. */
export function piExtractTool(payload: Record<string, unknown> | null | undefined): PiToolExecution | null {
  if (!payload)
    return null
  const cached = toolCache.get(payload)
  if (cached !== undefined)
    return cached
  const toolCallId = pickString(payload, 'toolCallId')
  const toolName = pickString(payload, 'toolName')
  if (!toolCallId || !toolName) {
    toolCache.set(payload, null)
    return null
  }
  const args = pickObject(payload, 'args') ?? {}
  const result = pickObject(payload, 'result')
  const partial = pickObject(payload, 'partialResult')
  const tool: PiToolExecution = {
    toolCallId,
    toolName,
    args,
    result: result ? piToolResult(result) : undefined,
    partialResult: partial ? piToolResult(partial) : undefined,
    isError: payload.isError === true,
  }
  toolCache.set(payload, tool)
  return tool
}
