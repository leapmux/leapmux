import type { AgentMessageSender, ChatRow, UserMessageAttachment } from './model/row'
import { fromJson } from '@bufbuild/protobuf'
import { MESSAGE_METADATA_FIELD } from '~/generated/contracts/worker-vocab'
import { AgentMessageSenderSchema } from '~/generated/proto/leapmux/v1/agent_pb'
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
export function leapmuxUserRow(payload: unknown): ChatRow | null {
  if (!isObject(payload))
    return null
  const text = pickString(payload, 'content')
  const attachments: UserMessageAttachment[] = Array.isArray(payload.attachments)
    ? payload.attachments.filter(isObject).map((item) => {
        const filename = pickString(item, 'filename') || undefined
        const mimeType = pickString(item, 'mime_type') || undefined
        return {
          ...(filename !== undefined
            ? { filename }
            : {}),
          ...(mimeType !== undefined
            ? { mimeType }
            : {}),
        }
      })
    : []
  return text.trim() === '' && attachments.length === 0
    ? { kind: 'hidden' }
    : { kind: 'user', text, attachments }
}

/**
 * The agent that sent a message, from the Worker metadata of its user row, or null
 * for a row that states none.
 *
 * The Worker writes the sender on the row of a message that another agent sent,
 * as the AgentMessageSender message in its protobuf JSON form. A value that does
 * not decode, or that states no tab, reads as no sender: the row still draws, as
 * the user's message, rather than not at all. A provider that this browser does
 * not know reads as UNSPECIFIED, and the row keeps its sender.
 */
export function agentMessageSenderOf(metadata: unknown): AgentMessageSender | null {
  if (!isObject(metadata))
    return null
  const value = metadata[MESSAGE_METADATA_FIELD.AgentMessageSender]
  if (!isObject(value))
    return null
  try {
    // A Worker of a later version may state more of the sender.
    const sender = fromJson(AgentMessageSenderSchema, value as Parameters<typeof fromJson>[1], { ignoreUnknownFields: true })
    return sender.tabId === ''
      ? null
      : { tabId: sender.tabId, provider: sender.provider }
  }
  catch {
    return null
  }
}

/**
 * The row with the sender that its metadata states, when the row is a message.
 *
 * Every provider reads its user rows itself, so the entry into layer 1 adds the
 * sender after the provider's extraction, from the metadata that the Worker
 * wrote. The sender is LeapMux's own statement, so no provider reads it, and a
 * new provider cannot drop it.
 */
export function withAgentMessageSender(row: ChatRow, metadata: unknown): ChatRow {
  if (row.kind !== 'user')
    return row
  const sender = agentMessageSenderOf(metadata)
  return sender === null
    ? row
    : { ...row, sender }
}

/** The notice LeapMux writes when the reader sends a plan into execution. */
export function leapmuxPlanExecutionRow(payload: unknown): ChatRow | null {
  if (!isObject(payload))
    return null
  const text = pickString(payload, 'content')
  return text
    ? { kind: 'plan-execution', text }
    : { kind: 'hidden' }
}
