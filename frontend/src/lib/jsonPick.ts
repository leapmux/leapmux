/** Generic JSON value pickers — type guards and safe extractors for untyped records. */

/** Type guard for plain objects. */
export function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

/**
 * Read a string-typed property from an untyped record. Returns `fallback`
 * when the value is missing or not a string. The default fallback is the
 * empty string; pass an explicit `undefined` to opt into `string | undefined`
 * typing.
 */
export function pickString(obj: Record<string, unknown> | null | undefined, key: string): string
export function pickString<T>(obj: Record<string, unknown> | null | undefined, key: string, fallback: T): string | T
export function pickString(obj: Record<string, unknown> | null | undefined, key: string, ...rest: [] | [unknown]): unknown {
  const v = obj?.[key]
  if (typeof v === 'string')
    return v
  return rest.length > 0 ? rest[0] : ''
}

/**
 * Read a number-typed property from an untyped record. Returns `fallback`
 * (default `null`) when the value is missing or not a number.
 */
export function pickNumber(obj: Record<string, unknown> | null | undefined, key: string): number | null
export function pickNumber<T>(obj: Record<string, unknown> | null | undefined, key: string, fallback: T): number | T
export function pickNumber(obj: Record<string, unknown> | null | undefined, key: string, ...rest: [] | [unknown]): unknown {
  const v = obj?.[key]
  if (typeof v === 'number')
    return v
  return rest.length > 0 ? rest[0] : null
}

/** Read a strict-boolean property: true iff the value is exactly `true`. */
export function pickBool(obj: Record<string, unknown> | null | undefined, key: string): boolean {
  return obj?.[key] === true
}

/**
 * Return the first string-typed value found across a list of candidate keys.
 * Useful for payloads that disagree on snake/camelCase naming
 * (`filePath`/`file_path`/`path`). Returns undefined when no key matched.
 */
export function pickFirstString(
  obj: Record<string, unknown> | null | undefined,
  keys: readonly string[],
): string | undefined {
  if (!obj)
    return undefined
  for (const key of keys) {
    const value = obj[key]
    if (typeof value === 'string')
      return value
  }
  return undefined
}

/** Number-typed counterpart to {@link pickFirstString}. */
export function pickFirstNumber(
  obj: Record<string, unknown> | null | undefined,
  keys: readonly string[],
): number | undefined {
  if (!obj)
    return undefined
  for (const key of keys) {
    const value = obj[key]
    if (typeof value === 'number')
      return value
  }
  return undefined
}
