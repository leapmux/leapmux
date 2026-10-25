import { createStore, produce } from 'solid-js/store'
import { queueEditDraftKeyPrefix } from '../attachments'

/**
 * The composer's schedule drafts, keyed by the composer's draft key: the agent
 * id for the ordinary composer, and `queueEditDraftKey(agentId, inputId)`
 * while a queue edit is open. These are the keys of the text draft and the
 * attachment cache, so the three parts of one draft share one identity.
 *
 * The store lives at module level for the reason the attachment cache does.
 * The composer panel unmounts while a non-agent tab has focus, and a schedule
 * that died with the panel would let the surviving text send at once.
 *
 * A key without an entry has no draft of its own. A queue edit then shows the
 * schedule that the edited item holds, and the ordinary composer shows none.
 * An entry of `EMPTY_SCHEDULE` is a draft: the user cleared the schedule.
 */
const [scheduleDrafts, setScheduleDrafts] = createStore<Record<string, string | undefined>>({})

/** The schedule draft of one composer key, or undefined when it holds none. */
export function getScheduleDraft(key: string): string | undefined {
  return scheduleDrafts[key]
}

/** Record the schedule draft of one composer key. */
export function setScheduleDraft(key: string, value: string): void {
  setScheduleDrafts(key, value)
}

/** Drop the schedule draft of one composer key. */
export function clearScheduleDraft(key: string): void {
  setScheduleDrafts(key, undefined)
}

/**
 * Drop every schedule draft of an agent: its ordinary draft and the draft of
 * each queue edit, which share `queueEditDraftKeyPrefix`.
 */
export function forgetAgentScheduleDrafts(agentId: string): void {
  const queuePrefix = queueEditDraftKeyPrefix(agentId)
  setScheduleDrafts(produce((drafts) => {
    for (const key of Object.keys(drafts)) {
      if (key === agentId || key.startsWith(queuePrefix))
        delete drafts[key]
    }
  }))
}
