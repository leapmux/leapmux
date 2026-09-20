/**
 * The permission-option VOCABULARY: what an agent's option is, and the words LeapMux
 * shows for it.
 *
 * This is data, and it has no presentation dependency. The decision buttons read it
 * through `~/components/chat/controls/permissionOptions`, and a SAVED decision reads
 * it directly, so one answer reads the same before and after the user gives it.
 */

import type { PermissionOption } from '../model/controlPrompt'
import { KIND_ALLOW_ALWAYS, KIND_ALLOW_ONCE, KIND_REJECT_ALWAYS, KIND_REJECT_ONCE } from '../model/controlPrompt'

// The four option kinds the Agent Client Protocol defines. They are the only stable
// discriminator across agents, because each agent spells its own optionId vocabulary.
/** Goose sets every option's name to its kind (`name === optionId === kind`), which is no label at all. */
const KIND_FALLBACK_LABELS: Record<string, string> = {
  [KIND_ALLOW_ONCE]: 'Allow once',
  [KIND_ALLOW_ALWAYS]: 'Allow always',
  [KIND_REJECT_ONCE]: 'Reject',
  [KIND_REJECT_ALWAYS]: 'Reject always',
}

/** The label an extra option's button shows: the agent's own name, unless the name is just the id. */
export function permissionOptionLabel(option: PermissionOption): string {
  if (option.name !== undefined && option.name !== option.optionId)
    return option.name
  // `Object.hasOwn`, not `??`: `kind` comes straight off the wire, and a value
  // that spells an `Object.prototype` member resolves to that function -- a truthy value,
  // so the two fallbacks below never ran and the function's source text became the
  // decision button's LABEL.
  const fallback = Object.hasOwn(KIND_FALLBACK_LABELS, option.kind) ? KIND_FALLBACK_LABELS[option.kind] : undefined
  return fallback ?? option.name ?? option.optionId
}
