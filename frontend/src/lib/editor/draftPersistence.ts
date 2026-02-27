import { safeGetJson, safeGetString, safeRemoveItem, safeSetJson } from '~/lib/safeStorage'

export const DRAFT_KEY_PREFIX = 'leapmux-editor-draft-'

export interface Draft {
  content: string
  cursor: number
}

export function loadDraft(agentId: string): Draft {
  const key = `${DRAFT_KEY_PREFIX}${agentId}`
  // Try JSON format first (new), fall back to plain string (legacy)
  const parsed = safeGetJson<{ content?: string, cursor?: number }>(key)
  if (parsed) {
    return { content: parsed.content ?? '', cursor: parsed.cursor ?? -1 }
  }
  const raw = safeGetString(key)
  if (raw) {
    return { content: raw, cursor: -1 }
  }
  return { content: '', cursor: -1 }
}

export function saveDraft(agentId: string, content: string, cursor: number): void {
  if (content) {
    safeSetJson(`${DRAFT_KEY_PREFIX}${agentId}`, { content, cursor })
  }
  else {
    safeRemoveItem(`${DRAFT_KEY_PREFIX}${agentId}`)
  }
}

export function clearDraft(agentId: string): void {
  safeRemoveItem(`${DRAFT_KEY_PREFIX}${agentId}`)
}
