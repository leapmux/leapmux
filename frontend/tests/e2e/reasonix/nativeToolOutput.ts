import { Buffer } from 'node:buffer'
import { ACP_SUPPLEMENT, ACP_SUPPLEMENT_IDENTITY } from '../../../src/generated/contracts/acp-protocol'
import { REASONIX_TOOL_RECORD } from '../../../src/generated/contracts/reasonix-protocol'
import { MESSAGE_SUPPLEMENT_FIELD } from '../../../src/generated/contracts/worker-vocab'
import { isObject } from '../../../src/lib/jsonPick'

export interface ReasonixNativeOutput {
  callId: string
  excerpt: string
  text: string
}

function nativeExcerptMatches(excerpt: string, text: string): boolean {
  if (excerpt === text)
    return true
  const match = /\n…\(([1-9]\d*) more chars truncated\)$/u.exec(excerpt)
  if (!match)
    return false
  const omitted = Number(match[1])
  const prefix = excerpt.slice(0, match.index)
  return Number.isSafeInteger(omitted) && Buffer.byteLength(text) - Buffer.byteLength(prefix) === omitted && text.startsWith(prefix)
}

/** Validate the exact native excerpt and stored native record before a Copy assertion. */
export function reasonixNativeOutput(original: unknown, supplemental: unknown, callId: string): ReasonixNativeOutput {
  if (!callId.trim() || !isObject(original) || original.toolCallId !== callId
    || original.sessionUpdate !== 'tool_call_update' || original.status !== 'completed') {
    throw new Error('The native Reasonix output has no exact completed call.')
  }
  const provider = isObject(supplemental) ? supplemental[MESSAGE_SUPPLEMENT_FIELD.Provider] : undefined
  if (!isObject(provider))
    throw new Error('The native Reasonix output has no stored provider record.')
  for (const key of Object.values(ACP_SUPPLEMENT_IDENTITY)) {
    if (Object.hasOwn(original, key) !== Object.hasOwn(provider, key) || original[key] !== provider[key])
      throw new Error('The native Reasonix output belongs to another result.')
  }
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
  const native = record[REASONIX_TOOL_RECORD.ContentField]
  const complete = record[REASONIX_TOOL_RECORD.RawContentField]
  const text = typeof complete === 'string' && complete !== '' ? complete : native
  if (typeof excerpt !== 'string' || typeof native !== 'string' || typeof text !== 'string'
    || (!nativeExcerptMatches(excerpt, native) && !nativeExcerptMatches(excerpt, text))) {
    throw new Error('The native Reasonix output does not match its actual excerpt.')
  }
  if (Buffer.byteLength(text) <= Buffer.byteLength(excerpt) || !/more chars truncated\)$/u.test(excerpt))
    throw new Error('The native Reasonix result supplied no omitted native output content.')
  return { callId, excerpt, text }
}
