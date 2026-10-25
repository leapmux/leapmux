/**
 * The key prefix that every per-queue-item draft and attachment set of one
 * agent shares. Attachment and schedule cleanup use this prefix, and the
 * composer composes each item key from it, so the separator has exactly one
 * definition. A second spelling makes a lookup miss without a message.
 */
export function queueEditDraftKeyPrefix(agentId: string): string {
  return `${agentId}-queue-`
}

/** The draft and attachment key for one queued input of one agent. */
export function queueEditDraftKey(agentId: string, inputId: string): string {
  return `${queueEditDraftKeyPrefix(agentId)}${inputId}`
}
