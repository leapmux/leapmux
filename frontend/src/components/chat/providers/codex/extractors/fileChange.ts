import { isObject } from '~/lib/jsonPick'

/**
 * The operation one Codex `fileChange` entry states.
 *
 * Codex spells `kind` two ways: a bare word on an older frame, and `{type}` on a
 * newer one. Both reach this build from a persisted transcript, so the unwrap lives
 * HERE -- `codexItemKind` and `codexFileChanges` both read it, and a second copy
 * would let the row's kind and the row's diff disagree about one entry.
 *
 * A shape this does not know answers the empty string, which every caller treats as
 * an update.
 */
export function codexChangeKind(change: Record<string, unknown>): string {
  const kind = change.kind
  if (typeof kind === 'string')
    return kind
  if (isObject(kind) && typeof kind.type === 'string')
    return kind.type
  return ''
}
