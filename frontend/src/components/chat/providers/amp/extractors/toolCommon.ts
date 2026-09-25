import type { ParsedMessageContent } from '~/lib/messageParser'
import { AMP_BLOCK_TYPE, AMP_LINE_TYPE } from '~/generated/contracts/amp-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/**
 * The shape of the rows that Amp's lines become.
 *
 * Amp prints each assistant message WHOLE, and the worker cuts it into one row for each
 * content block, with every other field of the line kept:
 *
 *   {"type":"assistant","message":{"role":"assistant","content":[<one block>],
 *    "stop_reason":...,"usage":{...}},"parent_tool_use_id":null,"session_id":"T-..."}
 *
 * A tool call is an assistant row whose block is a `tool_use`, and its result is a
 * `user` row whose block is the `tool_result` that answers it. A row that the worker
 * kept whole -- one it could not cut -- can hold several blocks, so every reader here
 * reads a LIST of blocks and takes the first block of the kind it wants.
 */

/** The content blocks of one assistant or user row, or none for another row. */
export function ampMessageBlocks(payload: Record<string, unknown> | null | undefined): Record<string, unknown>[] {
  const content = pickObject(payload, 'message')?.content
  return Array.isArray(content) ? content.filter(isObject) : []
}

/** The row's own `type`, when it is an assistant or a user row. */
function rowType(payload: Record<string, unknown> | null | undefined): string {
  const type = pickString(payload, 'type')
  return type === AMP_LINE_TYPE.Assistant || type === AMP_LINE_TYPE.User ? type : ''
}

/** The first block of `blockType` in one row of `lineType`, or null. */
export function ampFirstBlock(payload: Record<string, unknown> | null | undefined, lineType: string, blockType: string): Record<string, unknown> | null {
  if (rowType(payload) !== lineType)
    return null
  return ampMessageBlocks(payload).find(block => pickString(block, 'type') === blockType) ?? null
}

/** The text of every block of one type in one row, joined into paragraphs. */
export function ampBlockText(payload: Record<string, unknown> | null | undefined, blockType: string, field: string): string {
  return ampMessageBlocks(payload)
    .filter(block => pickString(block, 'type') === blockType)
    .map(block => pickString(block, field))
    .filter(text => text.trim() !== '')
    .join('\n\n')
}

/** One `tool_use` block: the call's id, its tool and its arguments. */
export interface AmpToolUse {
  id: string
  name: string
  input: Record<string, unknown>
}

/** One `tool_result` block: the call it answers, its text, and Amp's error flag. */
export interface AmpToolResult {
  toolUseId: string
  /**
   * The result as text.
   *
   * Amp states it as a string: the tool's own text, or the tool's JSON result written
   * as a string. Some Amp builds send a list of text blocks, and this reader joins them.
   */
  content: string
  isError: boolean
}

/** The call that one assistant row states, or null for a row that states none. */
export function ampToolUse(payload: Record<string, unknown> | null | undefined): AmpToolUse | null {
  const block = ampFirstBlock(payload, AMP_LINE_TYPE.Assistant, AMP_BLOCK_TYPE.ToolUse)
  if (!block)
    return null
  const id = pickString(block, 'id')
  const name = pickString(block, 'name')
  if (!id || !name)
    return null
  return { id, name, input: pickObject(block, 'input') ?? {} }
}

/** The text of one `tool_result` content value. */
function resultText(content: unknown): string {
  if (typeof content === 'string')
    return content
  if (!Array.isArray(content))
    return ''
  return content
    .filter(isObject)
    .filter(block => pickString(block, 'type') === AMP_BLOCK_TYPE.Text)
    .map(block => pickString(block, 'text'))
    .join('\n')
}

/** The result that one user row states, or null for a row that states none. */
export function ampToolResult(payload: Record<string, unknown> | null | undefined): AmpToolResult | null {
  const block = ampFirstBlock(payload, AMP_LINE_TYPE.User, AMP_BLOCK_TYPE.ToolResult)
  if (!block)
    return null
  const toolUseId = pickString(block, 'tool_use_id')
  if (!toolUseId)
    return null
  return { toolUseId, content: resultText(block.content), isError: block.is_error === true }
}

/** The call id that one span side states, whichever side it is. */
export function ampSideCallId(side: ParsedMessageContent | undefined): string {
  if (!side)
    return ''
  return ampToolUse(side.parentObject)?.id ?? ampToolResult(side.parentObject)?.toolUseId ?? ''
}
