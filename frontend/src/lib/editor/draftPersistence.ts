import { localStorageGet, localStorageRemove, localStorageSet, PREFIX_EDITOR_DRAFT } from '~/lib/browserStorage'

export interface Draft {
  content: string
  cursor: number
}

export function loadDraft(agentId: string): Draft {
  const key = `${PREFIX_EDITOR_DRAFT}${agentId}`
  const parsed = localStorageGet<{ content?: string, cursor?: number }>(key)
  if (parsed) {
    return { content: parsed.content ?? '', cursor: parsed.cursor ?? -1 }
  }
  return { content: '', cursor: -1 }
}

export function saveDraft(agentId: string, content: string, cursor: number): void {
  if (content) {
    localStorageSet(`${PREFIX_EDITOR_DRAFT}${agentId}`, { content, cursor })
  }
  else {
    localStorageRemove(`${PREFIX_EDITOR_DRAFT}${agentId}`)
  }
}

export function clearDraft(agentId: string): void {
  localStorageRemove(`${PREFIX_EDITOR_DRAFT}${agentId}`)
}
