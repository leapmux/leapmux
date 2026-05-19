/**
 * Extract a user-facing string from a caught value.
 *
 * - `Error` instances: returns `err.message`.
 * - Anything else: returns `fallback` when supplied, otherwise `String(err)`.
 *
 * Use the fallback form (`formatErrorMessage(err, 'Failed to load workers')`)
 * for dialogs that want a stable copy even when the rejection is a non-Error
 * thrown literal. Use the no-fallback form when the raw stringification is
 * the most useful thing the caller can show (debug logs, dev-only surfaces).
 */
export function formatErrorMessage(err: unknown, fallback?: string): string {
  if (err instanceof Error)
    return err.message
  if (fallback !== undefined)
    return fallback
  return String(err)
}
