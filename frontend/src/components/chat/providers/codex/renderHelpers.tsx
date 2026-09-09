import { isObject } from '~/lib/jsonPick'

/**
 * Codex emits items either wrapped (`{item: {...}, threadId, turnId}`) or
 * unwrapped (the item is the top-level object, for `item/completed`-style
 * messages stored directly). Resolves to the inner item or null.
 */
export function extractItem(parsed: unknown): Record<string, unknown> | null {
  if (!isObject(parsed))
    return null
  const item = parsed.item as Record<string, unknown> | undefined
  if (isObject(item))
    return item
  if (parsed.type && typeof parsed.type === 'string')
    return parsed
  return null
}
