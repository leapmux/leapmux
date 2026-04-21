/**
 * Same-reference early-exit shallow equality for plain objects and primitives.
 *
 * For objects: compares by key count, then by `Object.is` on each value. Arrays
 * are treated as unequal unless they are the same reference — callers that need
 * element-wise array comparison should wrap each element.
 */
export function shallowEqual(a: unknown, b: unknown): boolean {
  if (Object.is(a, b))
    return true
  if (!a || !b || typeof a !== 'object' || typeof b !== 'object')
    return false
  if (Array.isArray(a) || Array.isArray(b))
    return false
  const aKeys = Object.keys(a as object)
  const bKeys = Object.keys(b as object)
  if (aKeys.length !== bKeys.length)
    return false
  for (const k of aKeys) {
    if (!Object.is((a as Record<string, unknown>)[k], (b as Record<string, unknown>)[k]))
      return false
  }
  return true
}
