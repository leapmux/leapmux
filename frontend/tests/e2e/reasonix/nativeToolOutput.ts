import { basename, isAbsolute } from 'node:path'
import { ACP_SUPPLEMENT } from '../../../src/generated/contracts/acp-protocol'
import { REASONIX_TOOL_RECORD } from '../../../src/generated/contracts/reasonix-protocol'
import { MESSAGE_SUPPLEMENT_FIELD } from '../../../src/generated/contracts/worker-vocab'
import { isObject } from '../../../src/lib/jsonPick'
import { acpClosedToolCall, requireAcpToolSupplement } from '../helpers/acpToolFrame'

export interface ReasonixNativeOutput {
  callId: string
  excerpt: string
  text: string
  previewText: string
  path: string
}

/** Validate the complete native spill receipt and the exact stored record before a browser assertion. */
export function reasonixNativeOutput(original: unknown, supplemental: unknown, callId: string): ReasonixNativeOutput {
  if (!callId.trim() || !isObject(original) || !acpClosedToolCall(original, callId, ['completed', 'failed']))
    throw new Error('The native Reasonix output has no exact finished call.')
  const retained = isObject(supplemental) ? supplemental[MESSAGE_SUPPLEMENT_FIELD.Provider] : undefined
  if (!isObject(retained))
    throw new Error('The native Reasonix output has no stored provider record.')
  const provider = requireAcpToolSupplement(original, retained, 'Reasonix output')
  const raw = provider[ACP_SUPPLEMENT.RawOutput]
  const record = isObject(raw) ? raw[REASONIX_TOOL_RECORD.Envelope] : undefined
  if (!isObject(record) || record[REASONIX_TOOL_RECORD.RoleField] !== REASONIX_TOOL_RECORD.ToolRole
    || record[REASONIX_TOOL_RECORD.ToolCallIDField] !== callId || record[REASONIX_TOOL_RECORD.NameField] !== 'bash') {
    throw new Error('The native Reasonix output record has another tool identity.')
  }
  const content = original.content
  const first = Array.isArray(content) && content.length === 1 ? content[0] : undefined
  const block = isObject(first) && first.type === 'content' ? first.content : undefined
  const excerpt = isObject(block) && block.type === 'text' ? block.text : undefined
  const text = record[REASONIX_TOOL_RECORD.ContentField]
  if (typeof excerpt !== 'string' || typeof text !== 'string' || (original.status === 'completed' ? excerpt !== text : !text.includes(`\n\nOpening lines:\nerror: ${excerpt}\n`)))
    throw new Error('The native Reasonix output does not match its stored receipt.')
  const match = /^\[bash output kept out of context: [1-9]\d* lines, [^\]\n]+\]\nFull output: ([^\r\n]+)\nRead a window of it with read_file \(offset\/limit\), or search it with grep\. It is not in this conversation\.\n/u.exec(text)
  const path = match?.[1]
  if (!path || !isAbsolute(path) || basename(path) !== `${callId}.txt`)
    throw new Error('The native Reasonix output has no exact filesystem receipt.')
  return { callId, excerpt, text, previewText: excerpt, path }
}
