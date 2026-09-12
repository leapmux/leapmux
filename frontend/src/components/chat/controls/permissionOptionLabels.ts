/**
 * The permission-option VOCABULARY: what an agent's option is, and the words LeapMux
 * shows for it.
 *
 * This is data, and it has no presentation dependency. The decision buttons read it
 * through `~/components/chat/controls/permissionOptions`, and a SAVED decision reads
 * it directly, so one answer reads the same before and after the user gives it.
 */

export interface WirePermissionOption {
  optionId: string
  kind: string
  /** Absent on wire payloads that omit it; every reader must tolerate `undefined`. */
  name?: string
}

// The four option kinds the Agent Client Protocol defines. They are the only stable
// discriminator across agents, because each agent spells its own optionId vocabulary.
export const KIND_ALLOW_ONCE = 'allow_once'
export const KIND_ALLOW_ALWAYS = 'allow_always'
export const KIND_REJECT_ONCE = 'reject_once'
export const KIND_REJECT_ALWAYS = 'reject_always'

export const CANONICAL_KINDS = [KIND_ALLOW_ONCE, KIND_ALLOW_ALWAYS, KIND_REJECT_ONCE, KIND_REJECT_ALWAYS]

export function isRejectPermissionKind(kind: string): boolean {
  return kind === KIND_REJECT_ONCE || kind === KIND_REJECT_ALWAYS
}

/** The option family the request's positive action sends: only these apply a permission preset. */
export function isAllowPermissionKind(kind: string): boolean {
  return kind === KIND_ALLOW_ONCE || kind === KIND_ALLOW_ALWAYS
}

/** Goose sets every option's name to its kind (`name === optionId === kind`), which is no label at all. */
const KIND_FALLBACK_LABELS: Record<string, string> = {
  [KIND_ALLOW_ONCE]: 'Allow once',
  [KIND_ALLOW_ALWAYS]: 'Allow always',
  [KIND_REJECT_ONCE]: 'Reject',
  [KIND_REJECT_ALWAYS]: 'Reject always',
}

/** The label an extra option's button shows: the agent's own name, unless the name is just the id. */
export function permissionOptionLabel(option: WirePermissionOption): string {
  if (option.name !== undefined && option.name !== option.optionId)
    return option.name
  return KIND_FALLBACK_LABELS[option.kind] ?? option.name ?? option.optionId
}
