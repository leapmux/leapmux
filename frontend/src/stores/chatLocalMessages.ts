import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { ContentCompression, MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { localStorageGet, localStorageRemove, localStorageSet, PREFIX_LOCAL_MESSAGES } from '~/lib/browserStorage'

// ---------------------------------------------------------------------------
// Local (optimistic) message persistence via localStorage
//
// Optimistic user messages (seq 0n) are mirrored to localStorage so an
// undelivered or failed bubble survives a page refresh. The chat store owns the
// in-memory copy; this module owns its persisted shadow, keeping all the
// `leapmux:local-messages:` storage access in one place.
// ---------------------------------------------------------------------------

export interface PersistedLocalMessage {
  id: string
  contentText: string
  createdAt: string
  deliveryError: string
  attachments?: Array<{ filename?: string, mime_type?: string, data?: string }>
}

export function getPersistedLocalMessages(agentId: string): PersistedLocalMessage[] {
  return localStorageGet<PersistedLocalMessage[]>(`${PREFIX_LOCAL_MESSAGES}${agentId}`) ?? []
}

export function persistLocalMessage(agentId: string, msg: PersistedLocalMessage) {
  const list = getPersistedLocalMessages(agentId)
  list.push(msg)
  localStorageSet(`${PREFIX_LOCAL_MESSAGES}${agentId}`, list)
}

export function removePersistedLocalMessage(agentId: string, messageId: string) {
  const list = getPersistedLocalMessages(agentId)
  if (list.length === 0)
    return
  const filtered = list.filter(m => m.id !== messageId)
  if (filtered.length === 0) {
    localStorageRemove(`${PREFIX_LOCAL_MESSAGES}${agentId}`)
  }
  else {
    localStorageSet(`${PREFIX_LOCAL_MESSAGES}${agentId}`, filtered)
  }
}

/** Reconstruct an AgentChatMessage from a persisted local message. */
export function hydrateLocalMessage(p: PersistedLocalMessage): AgentChatMessage {
  const contentJson = JSON.stringify({
    content: p.contentText,
    ...(p.attachments && p.attachments.length > 0
      ? {
          attachments: p.attachments.map(att => ({
            ...(att.filename ? { filename: att.filename } : {}),
            ...(att.mime_type ? { mime_type: att.mime_type } : {}),
            ...(att.data ? { data: att.data } : {}),
          })),
        }
      : {}),
  })
  return {
    $typeName: 'leapmux.v1.AgentChatMessage' as const,
    id: p.id,
    source: MessageSource.USER,
    content: new TextEncoder().encode(contentJson),
    contentCompression: ContentCompression.NONE,
    seq: 0n,
    createdAt: p.createdAt,
    deliveryError: p.deliveryError,
    depth: 0,
    parentSpanId: '',
    spanId: '',
    spanLines: '[]',
  } as AgentChatMessage
}
