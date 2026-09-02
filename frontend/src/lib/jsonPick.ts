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
 * Read a boolean-typed property, returning `fallback` when the value is missing
 * or not a boolean.
 *
 * Distinct from {@link pickBool}, which folds "absent" and "the wrong type" into
 * `false`. A caller that must tell an explicit `false` apart from an absent key
 * needs this one and an `undefined` fallback -- a wire translator that omits an
 * absent key, for instance.
 */
export function pickBoolean(obj: Record<string, unknown> | null | undefined, key: string): boolean | null
export function pickBoolean<T>(obj: Record<string, unknown> | null | undefined, key: string, fallback: T): boolean | T
export function pickBoolean(obj: Record<string, unknown> | null | undefined, key: string, ...rest: [] | [unknown]): unknown {
  const v = obj?.[key]
  if (typeof v === 'boolean')
    return v
  return rest.length > 0 ? rest[0] : null
}

/**
 * Assign `value` to `target[key]`, and do NOTHING when it is undefined, so the
 * key stays ABSENT rather than present-and-undefined.
 *
 * The distinction is load-bearing wherever a result is later compared by key
 * count. `shallowEqual` compares `Object.keys(a).length` first, and a value that
 * round-trips through `JSON.stringify` loses its undefined-valued keys, so an
 * object built with explicit `undefined`s compares unequal to its own rehydrated
 * copy -- forever, on every comparison. `toEqual` cannot catch that, because it
 * ignores an undefined-valued key.
 *
 * Pair it with a picker whose fallback is `undefined`:
 * `assignDefined(info, 'utilization', pickNumber(raw, 'utilization', undefined))`.
 */
export function assignDefined<T, K extends keyof T>(target: T, key: K, value: T[K] | undefined): void {
  if (value !== undefined)
    target[key] = value
}

/**
 * Narrow an unknown value to its string elements: an array keeps only its string
 * entries, anything else yields []. The canonical home for the
 * `Array.isArray(v) ? v.filter((s): s is string => ...) : []` idiom that the
 * provider height metrics hand-rolled per call site (Codex reasoning summary/content,
 * Claude ToolSearch matches / result-divider errors).
 */
export function stringArray(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((s): s is string => typeof s === 'string') : []
}

/**
 * Read an object-typed property from an untyped record. Returns `fallback`
 * (default `null`) when the value is missing or not a plain object — arrays
 * and primitives are rejected.
 */
export function pickObject(
  obj: Record<string, unknown> | null | undefined,
  key: string,
): Record<string, unknown> | null
export function pickObject<T>(
  obj: Record<string, unknown> | null | undefined,
  key: string,
  fallback: T,
): Record<string, unknown> | T
export function pickObject(
  obj: Record<string, unknown> | null | undefined,
  key: string,
  ...rest: [] | [unknown]
): unknown {
  const v = obj?.[key]
  if (isObject(v))
    return v
  return rest.length > 0 ? rest[0] : null
}

/**
 * Return the first value across `keys` that the type-narrowing predicate accepts,
 * or undefined when no key matched. The shared loop behind {@link pickFirstString} /
 * {@link pickFirstNumber} / {@link pickFirstObject}, which differ only in `narrow`.
 */
function pickFirst<T>(
  obj: Record<string, unknown> | null | undefined,
  keys: readonly string[],
  narrow: (v: unknown) => v is T,
): T | undefined {
  if (!obj)
    return undefined
  for (const key of keys) {
    const value = obj[key]
    if (narrow(value))
      return value
  }
  return undefined
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
  return pickFirst(obj, keys, (v): v is string => typeof v === 'string')
}

/** Number-typed counterpart to {@link pickFirstString}. */
export function pickFirstNumber(
  obj: Record<string, unknown> | null | undefined,
  keys: readonly string[],
): number | undefined {
  return pickFirst(obj, keys, (v): v is number => typeof v === 'number')
}

/** Object-typed counterpart to {@link pickFirstString}. */
export function pickFirstObject(
  obj: Record<string, unknown> | null | undefined,
  keys: readonly string[],
): Record<string, unknown> | undefined {
  return pickFirst(obj, keys, isObject)
}
