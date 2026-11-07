import { isObject } from '~/lib/jsonPick'

/** Read the operation from the current native tagged union. Unknown shapes state no operation. */
export function codexChangeKind(change: Record<string, unknown>): string {
  const kind = change.kind
  if (isObject(kind) && typeof kind.type === 'string')
    return kind.type
  return ''
}
