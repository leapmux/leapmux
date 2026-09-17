import { isObject } from '~/lib/jsonPick'

/**
 * Codex emits items three ways, and a persisted row can hold any of them:
 *
 *   - wrapped (`{item: {...}, threadId, turnId}`), which is the common case,
 *   - inside the JSON-RPC envelope of `item/started` and `item/completed`
 *     (`{method, params: {item: {...}}}`), which the worker stores verbatim,
 *   - unwrapped, with the item itself as the top-level object.
 *
 * Resolves to the inner item or null. The `params` form used to reach neither
 * reader: `classify` looked at `parent.item` alone, so the row fell through to
 * `unknown` and drew raw JSON.
 */
export function extractItem(parsed: unknown): Record<string, unknown> | null {
  if (!isObject(parsed))
    return null
  const item = parsed.item
  if (isObject(item))
    return item
  const params = parsed.params
  if (isObject(params) && isObject(params.item))
    return params.item
  if (parsed.type && typeof parsed.type === 'string')
    return parsed
  return null
}
