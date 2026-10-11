/**
 * Escape `text` so that a regular expression matches it literally, outside a character class.
 *
 * The function puts a backslash before each syntax character, and only before a syntax character, so the result is
 * also valid with the `u` flag, which refuses an escape of any other character. `RegExp.escape` does the same job,
 * but it is an ES2025 API, and the TypeScript `lib` of this project is ES2023.
 */
export function escapeRegExp(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}
