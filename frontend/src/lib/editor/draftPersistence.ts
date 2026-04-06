import { PREFIX_EDITOR_DRAFT, safeGetJson, safeGetString, safeRemoveItem, safeSetJson } from '~/lib/browserStorage'

export interface Draft {
  content: string
  cursor: number
}

export function loadDraft(agentId: string): Draft {
  const key = `${PREFIX_EDITOR_DRAFT}${agentId}`
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
    safeSetJson(`${PREFIX_EDITOR_DRAFT}${agentId}`, { content, cursor })
  }
  else {
    safeRemoveItem(`${PREFIX_EDITOR_DRAFT}${agentId}`)
  }
}

export function clearDraft(agentId: string): void {
  safeRemoveItem(`${PREFIX_EDITOR_DRAFT}${agentId}`)
}
