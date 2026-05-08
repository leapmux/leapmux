/**
 * Returns a function that yields strings of the form `${prefix}-${timestamp}-${counter}`.
 * The counter disambiguates ids minted within the same millisecond (where
 * `Date.now()` returns the same value); the timestamp keeps ids roughly
 * sortable.
 */
export function makeIdGenerator(prefix: string): () => string {
  let counter = 0
  return () => {
    counter++
    return `${prefix}-${Date.now()}-${counter}`
  }
}
