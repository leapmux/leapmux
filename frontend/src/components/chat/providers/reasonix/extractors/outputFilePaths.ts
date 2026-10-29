import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { REASONIX_OUTPUT_RECEIPT, REASONIX_TOOL_RECORD } from '~/generated/contracts/reasonix-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { collectAcpToolText } from '../../acp/content'
import { parsedACPToolCall } from '../../acp/extractors/toolCall'
import { acpSupplementRawOutput, acpToolSupplement } from '../../acp/toolSupplement'
import { reasonixAuthenticatedOutputFilePointer, reasonixOutputFilePointer } from './outputReceipt'

/** Expose the saved output beside its original preview, without reading or replacing that preview. */
export function reasonixOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const native = parsedACPToolCall(input.resolved.parentObject)
  if (input.span.role !== 'result' || !native || native.toolCallId !== call.id
    || (native.status !== 'completed' && native.status !== 'failed') || !Array.isArray(native.content)) {
    return []
  }
  const request = parsedACPToolCall(input.span.request?.parentObject)
  const nativeName = pickString(native, 'name') || pickString(native, 'title')
    || (request?.toolCallId === call.id ? pickString(request, 'name') || pickString(request, 'title') : '') || call.name
  const paths = native.content.flatMap((block: unknown) => {
    if (!isObject(block) || block.type !== 'content' || !isObject(block.content) || block.content.type !== 'text')
      return []
    const path = reasonixOutputFilePointer(block.content.text, nativeName, call.id)
    return path === undefined ? [] : [path]
  })
  if (paths.length > 0)
    return paths.length === 1 ? paths : []
  if (native.status !== 'failed')
    return []
  const output = acpSupplementRawOutput(acpToolSupplement(native, input.resolved.supplementalContent))
  const saved = pickObject(output, REASONIX_TOOL_RECORD.Envelope)
  if (saved?.[REASONIX_TOOL_RECORD.RoleField] !== REASONIX_TOOL_RECORD.ToolRole || saved[REASONIX_TOOL_RECORD.ToolCallIDField] !== call.id)
    return []
  const savedName = pickString(saved, REASONIX_TOOL_RECORD.NameField)
  if (savedName && nativeName && savedName !== nativeName)
    return []
  const path = reasonixAuthenticatedOutputFilePointer(saved[REASONIX_TOOL_RECORD.ContentField], savedName || nativeName, call.id, collectAcpToolText(native), output?.[REASONIX_OUTPUT_RECEIPT.Envelope])
  return path === undefined ? [] : [path]
}
