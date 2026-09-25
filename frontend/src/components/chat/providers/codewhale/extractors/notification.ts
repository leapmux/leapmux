import type { NotificationEntry } from '../../../model/notification'
import { CODEWHALE_EVENT, CODEWHALE_ITEM_KIND } from '~/generated/contracts/codewhale-protocol'
import { pickBool, pickNumber, pickString } from '~/lib/jsonPick'
import { formatDuration } from '../../../rendererUtils'
import { codewhaleEnvelope, codewhaleItem } from './toolCommon'

/**
 * The setting that sets how long Codewhale waits for an answer to an approval or a
 * question, and the value that removes the limit.
 */
const CODEWHALE_WAIT_SETTING_HINT = 'Set [tools] user_input_timeout_seconds = 0 in the Codewhale configuration to wait with no limit.'

/** The words of an item: its whole `detail`, or the `summary` when it states no detail. */
function itemText(item: { summary: string, detail: string }): string {
  return (item.detail || item.summary).trim()
}

/** The entries of one finished non-tool item: a status, a compaction, or an error. */
function itemEntries(parsed: Record<string, unknown>, payload: Record<string, unknown>): NotificationEntry[] | null {
  const item = codewhaleItem(parsed)
  if (!item || item.outcome === 'open')
    return null
  switch (item.kind) {
    // The worker persists only the status items a reader acts on: it drops the notes of
    // the runtime's own loop before they reach the transcript (`statusItemIsPlumbing`).
    case CODEWHALE_ITEM_KIND.Status: {
      const text = itemText(item)
      return text ? [{ kind: 'status', text }] : []
    }
    case CODEWHALE_ITEM_KIND.ContextCompaction: {
      if (item.outcome !== 'completed')
        return [{ kind: 'compaction', phase: 'end', error: itemText(item) || 'aborted' }]
      // The event states beside the item whether the runtime started the compaction by
      // itself. Its counts are MESSAGE counts, which the token-shaped detail cannot carry.
      return [{ kind: 'compaction', phase: 'end', detail: { trigger: pickBool(payload, 'auto') ? 'auto' : 'manual' } }]
    }
    case CODEWHALE_ITEM_KIND.Error: {
      const text = itemText(item)
      return [{ kind: 'text', text: text ? `Error: ${text}` : 'Error' }]
    }
    default:
      return null
  }
}

/**
 * Read one Codewhale notification row into the shared notification model.
 *
 * Codewhale's SOLE notification seam, for a standalone row and for one entry of a
 * consolidated thread alike. Returns an empty list for a row this plugin reads and
 * draws nothing for, and for a row it cannot read: a notification already reached the
 * transcript, so nothing is lost by drawing no line for it.
 */
export function codewhaleNotificationEntry(msg: Record<string, unknown>): NotificationEntry[] {
  const envelope = codewhaleEnvelope(msg)
  if (!envelope)
    return []
  const payload = envelope.payload
  switch (envelope.event) {
    // The worker persists a dropped steer only when nothing sends it again: the queue
    // sends a refused steer as a new message, and the worker hands a later drop back
    // to the queue. So the row asks the reader to send it. The runtime's `reason` asks
    // that of every drop, including the ones that LeapMux sends again, so it is not shown.
    case CODEWHALE_EVENT.TurnSteerDropped: {
      const input = pickString(payload, 'input').trim()
      const steer = input ? `the steer "${input}"` : 'a steer'
      return [{ kind: 'text', text: `Codewhale dropped ${steer} before the model read it. Send it again to deliver it.` }]
    }
    // The wait comes from Codewhale's own configuration, which LeapMux cannot change for
    // its sessions and which no event states in advance. So the row names the setting.
    case CODEWHALE_EVENT.ApprovalTimeout: {
      const seconds = pickNumber(payload, 'timeout_secs')
      const wait = seconds !== null && seconds > 0 ? `within ${formatDuration(seconds * 1000)}` : 'in time'
      return [{ kind: 'text', text: `Codewhale denied the call because nobody answered ${wait}. ${CODEWHALE_WAIT_SETTING_HINT}` }]
    }
    case CODEWHALE_EVENT.SandboxDenied: {
      const tool = pickString(payload, 'tool_name') || 'a tool'
      const reason = pickString(payload, 'reason')
      return [{ kind: 'text', text: reason ? `The sandbox denied ${tool}: ${reason}` : `The sandbox denied ${tool}` }]
    }
    case CODEWHALE_EVENT.StoreFailure: {
      const message = pickString(payload, 'message') || pickString(payload, 'error') || pickString(payload, 'reason')
      return [{ kind: 'text', text: message ? `The runtime could not save its state: ${message}` : 'The runtime could not save its state' }]
    }
    default:
      return itemEntries(msg, payload) ?? []
  }
}
