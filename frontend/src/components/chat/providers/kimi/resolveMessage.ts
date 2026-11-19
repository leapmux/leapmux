import type { KimiConversationRow } from './conversation'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { KIMI_CONVERSATION_FIELD, KIMI_CONVERSATION_ITEM_KIND, KIMI_CONVERSATION_RECORD_ROLE, KIMI_CONVERSATION_SUPPLEMENT, KIMI_CONVERSATION_MEDIA as MEDIA } from '~/generated/contracts/kimi-protocol'
import { isObject, pickObject } from '~/lib/jsonPick'
import { kimiConversationAcceptsView, kimiConversationRow } from './conversation'
import { kimiInlineImage } from './extractors/media'

function resolveConversationMedia(row: KimiConversationRow, supplement: Record<string, unknown> | undefined): Record<string, unknown> | undefined {
  if (row.kind !== 'item' || row.role !== KIMI_CONVERSATION_RECORD_ROLE.Result
    || row.item[KIMI_CONVERSATION_FIELD.Type] !== KIMI_CONVERSATION_ITEM_KIND.ToolCall) {
    return undefined
  }
  const receipt = pickObject(supplement, MEDIA.Root)
  if (!receipt || receipt[MEDIA.SessionID] !== row.sessionId || receipt[MEDIA.AgentID] !== row.agentId
    || receipt[MEDIA.TurnID] !== row.turnId || receipt[MEDIA.ItemID] !== row.item[KIMI_CONVERSATION_FIELD.ItemID]) {
    return undefined
  }
  const files = pickObject(receipt, MEDIA.Files)
  if (!files)
    return undefined
  let restored = false
  const nativeMedia = row.item[KIMI_CONVERSATION_FIELD.Media]
  if (!Array.isArray(nativeMedia))
    return undefined
  const media = nativeMedia.map((part) => {
    if (!isObject(part) || part[KIMI_CONVERSATION_FIELD.Kind] !== 'image')
      return part
    const fileID = part[KIMI_CONVERSATION_FIELD.FileID]
    if (typeof fileID !== 'string' || !/^f_[0-9a-f]{64}$/u.test(fileID))
      return part
    const file = pickObject(files, fileID)
    const image = kimiInlineImage(file?.[MEDIA.MIMEType], file?.[MEDIA.Data])
    if (!image)
      return part
    restored = true
    return { ...part, [KIMI_CONVERSATION_FIELD.URL]: `data:${image.mimeType};base64,${image.data}`, [KIMI_CONVERSATION_FIELD.MediaType]: image.mimeType }
  })
  return restored
    ? {
        ...row.record,
        [KIMI_CONVERSATION_FIELD.Item]: { ...row.item, [KIMI_CONVERSATION_FIELD.Media]: media },
      }
    : undefined
}

/** Resolve scoped native bytes and collected output only in the display copy. */
export function resolveKimiMessage(parsed: ParsedMessageContent): Record<string, unknown> | undefined {
  const conversation = kimiConversationRow(parsed.parentObject)
  if (!conversation || !parsed.agentSessionId || parsed.agentSessionId !== conversation.sessionId
    || (conversation.kind === 'item' && conversation.role === KIMI_CONVERSATION_RECORD_ROLE.Request)) {
    return undefined
  }
  const supplement = isObject(parsed.supplementalContent)
    ? parsed.supplementalContent
    : undefined
  const latest = kimiConversationRow(supplement?.[KIMI_CONVERSATION_SUPPLEMENT.Root])
  const current = latest && kimiConversationAcceptsView(conversation, latest)
    ? latest
    : conversation
  return resolveConversationMedia(current, supplement) ?? (current === latest
    ? latest.record
    : undefined)
}
