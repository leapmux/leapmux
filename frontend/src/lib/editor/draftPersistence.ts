import { localStorageDrop, localStorageLoad, localStorageStore, PREFIX_EDITOR_DRAFT } from '~/lib/browserStorage'

export interface Draft {
  content: string
  cursor: number
}

/**
 * Read an agent's saved draft, or an empty one.
 *
 * ASYNCHRONOUS: a draft is arbitrary user prose, so the family is unbounded and
 * lives on the unmirrored storage tier. Every caller already awaits something
 * around it -- the editor loads a draft inside the `onMount` that builds the
 * editor, and re-loads inside an effect on the key.
 */
export async function loadDraft(agentId: string): Promise<Draft> {
  const parsed = await localStorageLoad<{ content?: string, cursor?: number }>(`${PREFIX_EDITOR_DRAFT}${agentId}`)
  if (parsed) {
    return { content: parsed.content ?? '', cursor: parsed.cursor ?? -1 }
  }
  return { content: '', cursor: -1 }
}

export function saveDraft(agentId: string, content: string, cursor: number): void {
  if (content) {
    localStorageStore(`${PREFIX_EDITOR_DRAFT}${agentId}`, { content, cursor })
  }
  else {
    localStorageDrop(`${PREFIX_EDITOR_DRAFT}${agentId}`)
  }
}

export function clearDraft(agentId: string): void {
  localStorageDrop(`${PREFIX_EDITOR_DRAFT}${agentId}`)
}
