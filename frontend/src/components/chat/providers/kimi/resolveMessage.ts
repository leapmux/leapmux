import type { KimiTranscriptToolRow } from './transcript'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { KIMI_TRANSCRIPT_SUPPLEMENT as FIELD, KIMI_MEDIA_SOURCE, KIMI_OUTPUT_PART, KIMI_TRANSCRIPT_TOOL } from '~/generated/contracts/kimi-protocol'
import { base64ToUint8Array, uint8ArrayToBase64 } from '~/lib/base64'
import { isObject, pickObject } from '~/lib/jsonPick'
import { retainedRowIsFinal } from '../registry'
import { kimiTranscriptTool } from './transcript'

function scopedReceipt(parsed: ParsedMessageContent, row: KimiTranscriptToolRow, kind: string): Record<string, unknown> | undefined {
  const receipt = pickObject(isObject(parsed.supplementalContent)
    ? parsed.supplementalContent
    : undefined, kind)
  if (!receipt || !parsed.agentSessionId || parsed.agentSessionId !== row.sessionId
    || receipt[FIELD.SessionID] !== row.sessionId || receipt[FIELD.AgentID] !== row.agentId
    || receipt[FIELD.TurnID] !== row.turnId || receipt[FIELD.StepID] !== row.stepId
    || receipt[FIELD.FrameID] !== row.frameId || receipt[FIELD.ToolCallID] !== row.toolCallId) {
    return undefined
  }
  return receipt
}

/** Resolve scoped native bytes and collected output only in the display copy. */
export function resolveKimiMessage(parsed: ParsedMessageContent): Record<string, unknown> | undefined {
  const row = kimiTranscriptTool(parsed.parentObject)
  if (!row || !parsed.parentObject)
    return undefined
  let frame = row.frame
  const retained = scopedReceipt(parsed, row, FIELD.ToolOutput)
  const text = retained?.[FIELD.Text]
  const truncated = retained?.[FIELD.Truncated]
  if (!Object.hasOwn(frame, 'output') && (row.state !== KIMI_TRANSCRIPT_TOOL.Running || retainedRowIsFinal(parsed.completion))
    && typeof text === 'string' && text && typeof truncated === 'boolean') {
    frame = { ...frame, output: text, truncated }
  }
  const receipt = scopedReceipt(parsed, row, FIELD.Media)
  const files = pickObject(receipt, FIELD.Files)
  if (files && Array.isArray(row.frame.output)) {
    let restored = false
    const output = row.frame.output.map((part: unknown) => {
      if (!isObject(part) || part.type !== KIMI_OUTPUT_PART.Image)
        return part
      const source = pickObject(part, 'source')
      const id = source?.file_id
      if (source?.kind !== KIMI_MEDIA_SOURCE.SessionMedia || typeof id !== 'string' || !/^f_[0-9a-f]{64}$/u.test(id) || !Object.hasOwn(files, id))
        return part
      const file = pickObject(files, id)
      const mimeType = file?.[FIELD.MIMEType]
      const data = file?.[FIELD.Data]
      if (typeof mimeType !== 'string' || !/^image\/[a-z0-9!#$%&'*+.^_`|~-]+$/u.test(mimeType)
        || typeof data !== 'string' || !data || !/^(?:[A-Z0-9+/]{4})*(?:[A-Z0-9+/]{2}==|[A-Z0-9+/]{3}=)?$/iu.test(data)) {
        return part
      }
      if (uint8ArrayToBase64(base64ToUint8Array(data)) !== data)
        return part
      restored = true
      return { ...part, mimeType, data }
    })
    if (restored)
      frame = { ...frame, output }
  }
  if (frame === row.frame)
    return undefined
  const payload = pickObject(parsed.parentObject, 'payload')!
  return {
    ...parsed.parentObject,
    payload: {
      ...payload,
      ops: [
        { ...row.operation, frame },
      ],
    },
  }
}
