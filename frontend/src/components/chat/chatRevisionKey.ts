import type { MessageRevision } from '~/lib/messageSpan'

// ---------------------------------------------------------------------------
// The ONE row revision key.
//
// A row's caches -- the classified entry, the extracted IR, the normalized
// command body, the measured height -- used to re-key on ten separate freshness
// fields hand-copied between the entry cache and the geometry key, and the two
// lists drifted. Here the freshness IS one string, built from exactly the
// revisions the row depends on, and every cache keys on it.
//
// The string is VERSIONED and LENGTH-PREFIXED, never hashed: a hash could collide
// (silently), and the fields are short enough that the readable key costs less
// than the collision check a digest would demand.
// ---------------------------------------------------------------------------

/**
 * The revisions one row's caches depend on: always its own, plus a sibling's
 * when the row renders from that side.
 *
 * `request` rides only on a RESULT row (the row that draws its request's input),
 * and `result` only on a TOOL-USE row (the row that may render from hidden
 * result data). An absent member states "this row does not depend on that side"
 * -- which is itself part of the key, so a member ARRIVING changes it.
 */
export interface RowRevisionDependencies {
  own: MessageRevision
  request?: MessageRevision
  result?: MessageRevision
}

/** The key format version. Bump when the layout of the string changes. */
const ROW_REVISION_KEY_VERSION = 1

/**
 * One member's contribution: tagged, length-prefixed, and in a fixed field
 * order -- id, sequence, content version, supplemental revision.
 *
 * The LENGTH prefix keeps an id that contains the delimiter from forging a
 * different member's fields: the reader takes the id by count, not by scan.
 */
function member(tag: string, revision: MessageRevision): string {
  return `${tag}=${revision.id.length}:${revision.id}|${revision.seq}|${revision.contentVersion}|${revision.supplementalRevision}`
}

/**
 * The exact dependency key of one row revision.
 *
 * Members appear in a FIXED order (own, request, result), each tagged, so two
 * different dependency sets never spell the same string and the same set always
 * spells the same one. Members are never sorted and the key is never hashed.
 */
export function rowRevisionKey(
  dependencies: RowRevisionDependencies,
): string {
  const parts = [`v${ROW_REVISION_KEY_VERSION}`, member('own', dependencies.own)]
  if (dependencies.request !== undefined)
    parts.push(member('request', dependencies.request))
  if (dependencies.result !== undefined)
    parts.push(member('result', dependencies.result))
  return parts.join('~')
}
