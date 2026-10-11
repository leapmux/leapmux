import { isObject } from '../../../src/lib/jsonPick'

/** Collect every string value of a decoded JSON value in document order. An object key is not a value. */
export function jsonStringValues(value: unknown): string[] {
  if (typeof value === 'string')
    return [value]
  if (Array.isArray(value))
    return value.flatMap(jsonStringValues)
  if (isObject(value))
    return Object.values(value).flatMap(jsonStringValues)
  return []
}
