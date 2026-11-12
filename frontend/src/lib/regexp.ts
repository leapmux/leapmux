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

/**
 * The source of a multiline pattern that matches the indentation at the start of a
 * line: `^`, then each character that `\s` matches, except a line terminator.
 */
const LINE_INDENT_SOURCE = String.raw`^[^\S\n\r\u2028\u2029]*`

/**
 * Build a multiline pattern that matches `body` at the start of a line, after its
 * indentation. For each text, `test` gives the same result as `/^\s*(?:body)/m`
 * does, in linear time.
 *
 * `^\s*` takes time quadratic in a run of blank lines: `\s` also matches the line
 * terminators, so each line start where the body fails rescans every blank line below
 * it. This pattern ends the indentation at the end of its line. A match that `\s*`
 * found across blank lines also has a match at the line start of the body, and this
 * pattern finds that one. So a match can start on a later line than the match of
 * `^\s*` does, at the same body.
 */
export function afterLineIndent(body: RegExp): RegExp {
  const flags = body.flags.includes('m')
    ? body.flags
    : `${body.flags}m`
  return new RegExp(`${LINE_INDENT_SOURCE}(?:${body.source})`, flags)
}
