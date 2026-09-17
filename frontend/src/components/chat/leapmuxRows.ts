import type { AttachmentIR, ChatRowIR } from './ir/row'
import { isObject, pickString } from '~/lib/jsonPick'

// The rows LeapMux writes ITSELF, read once for every provider.
//
// Each of these carries LeapMux's own envelope rather than a provider frame, so no
// plugin has anything of its own to read in one and every plugin would reach the same
// answer. `notificationEntries.ts` states the same rule for the notification half.

/**
 * The user row LeapMux persists as `{content, attachments?}`.
 *
 * A payload that is not an object at all yields null, which is the caller's signal
 * that nothing here can read the frame. A payload that carries neither text nor an
 * attachment is HIDDEN instead: it is a row LeapMux wrote and there is nothing in it
 * to show, which is a different statement from one nobody could read.
 */
export function leapmuxUserRow(payload: unknown): ChatRowIR | null {
  if (!isObject(payload))
    return null
  const text = pickString(payload, 'content')
  const attachments: AttachmentIR[] = Array.isArray(payload.attachments)
    ? payload.attachments.filter(isObject).map((item) => {
        const filename = pickString(item, 'filename') || undefined
        const mimeType = pickString(item, 'mime_type') || undefined
        return { ...(filename !== undefined ? { filename } : {}), ...(mimeType !== undefined ? { mimeType } : {}) }
      })
    : []
  return text.trim() === '' && attachments.length === 0 ? { kind: 'hidden' } : { kind: 'user', text, attachments }
}

/** The notice LeapMux writes when the reader sends a plan into execution. */
export function leapmuxPlanExecutionRow(payload: unknown): ChatRowIR | null {
  if (!isObject(payload))
    return null
  const text = pickString(payload, 'content')
  return text ? { kind: 'plan-execution', text } : { kind: 'hidden' }
}
