import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { ZCODE_STORED_ATTACHMENT, ZCODE_STORED_PART, ZCODE_STORED_TOOL } from '~/generated/contracts/zcode-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'
import { ZCODE_SERIALIZATION_STRATEGY, ZCODE_STORED_SERIALIZATION } from '../protocol'
import { zcodeToolSupplement } from '../toolSupplement'
import { zcodeEnvelope, zcodeNativeTool, zcodeRow } from './toolCommon'

/** ZCode replaces each non-ASCII UTF-16 code unit and keeps 120 characters. */
function nativeFileSegment(value: string): string {
  return value.replace(/[^\w.-]/g, '_').slice(0, 120) || 'unknown'
}

/** Read a declared native serialization path, separate from its opaque resource URI. */
export function zcodeOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  if (input.span.role !== 'result' || !call.id)
    return []
  const row = zcodeRow(input.resolved.parentObject, input.spanType, input.span.request, input.resolved.supplementalContent)
  const native = zcodeNativeTool(row)
  if (!native)
    return []
  const own = input.resolved.parentObject
  const eventSession = pickString(own, 'sessionId', undefined)
  const workerSession = input.resolved.agentSessionId
  const childSession = pickString(zcodeEnvelope(own)?.payload, 'childSessionId', undefined)
  if (eventSession && workerSession && eventSession !== workerSession)
    return []
  const toolSession = childSession ?? eventSession ?? workerSession
  if (toolSession !== native.sessionId)
    return []
  const serialization = pickObject(pickObject(native.state, ZCODE_STORED_ATTACHMENT.Metadata), ZCODE_STORED_SERIALIZATION.Serialization)
  const path = serialization?.[ZCODE_STORED_SERIALIZATION.OutputFilePath]
  if (serialization?.[ZCODE_STORED_SERIALIZATION.BudgetStrategy] !== ZCODE_SERIALIZATION_STRATEGY.OutputFile || !isFilesystemPath(path))
    return []
  const nativeTool = zcodeToolSupplement(row.supplemental).nativeTool
  const callId = pickString(pickObject(nativeTool, ZCODE_STORED_TOOL.Data), ZCODE_STORED_PART.CallID)
  const parts = path.split(/[\\/]/u)
  const leaf = parts.at(-1)
  const prefix = `${nativeFileSegment(callId)}-`
  return parts.at(-2) === nativeFileSegment(native.sessionId) && leaf?.startsWith(prefix)
    && /^tool-result-[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}\.txt$/iu.test(leaf.slice(prefix.length))
    ? [path]
    : []
}
