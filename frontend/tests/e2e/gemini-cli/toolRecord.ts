import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { acpSupplementRawOutput, acpToolSupplement } from '../../../src/components/chat/providers/acp/toolSupplement'
import { geminiStoredToolRecord } from '../../../src/components/chat/providers/gemini/extractors/storedToolCall'
import { GEMINI_SUPPLEMENT } from '../../../src/generated/contracts/gemini-protocol'
import { pickObject, pickString } from '../../../src/lib/jsonPick'
import { parseMessageContent } from '../../../src/lib/messageParser'

/** Read a native Gemini tool record only when its Worker frame identity matches. */
export function readGeminiStoredToolRecord(message: AgentChatMessage): Record<string, unknown> | null {
  const parsed = parseMessageContent(message)
  const original = parsed.parentObject
  if (!original)
    return null
  const supplement = acpToolSupplement(original, parsed.supplementalContent)
  const record = geminiStoredToolRecord(pickObject(acpSupplementRawOutput(supplement), GEMINI_SUPPLEMENT.StoredToolRecord))
  return record && pickString(record, 'id') === pickString(original, 'toolCallId') ? record : null
}
