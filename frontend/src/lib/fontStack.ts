/**
 * The built-in monospace stack.
 *
 * Declared once, because three consumers fall back to it and they must
 * agree: the terminal instance (`~/lib/terminal`), the resolved
 * preference (`~/context/PreferencesContext`), and the `--font-mono`
 * custom property in the global stylesheet. Three copies of the literal
 * meant that changing the bundled font left two of them stale, silently.
 */
export const DEFAULT_MONO_FONT_FAMILY = '"Hack NF", Hack, "SF Mono", Consolas, monospace'

/**
 * Render an ordered list of family names as a CSS `font-family` value.
 *
 * Every name is quoted, and TWO classes of character inside it are
 * escaped. This escape is the ONLY guard on the quote and the backslash:
 * the account write path (`usersettings.validateFontFamily`) refuses a
 * control character but stores a quote, and a hand-edited localStorage
 * document never reaches that validator at all. The escape holds for
 * whatever the store holds, which is the property worth having on a
 * string interpolated into a stylesheet — a character ban at the store
 * only duplicates it, in two languages that drift apart.
 *
 * - A quote or a backslash would END the declaration it sits in, and the
 *   rest of the rule would be read as live CSS. This string reaches a
 *   stylesheet that the terminal builds by concatenation.
 * - A control character makes the whole declaration INVALID, and a CSS
 *   parser drops an invalid declaration entirely. The two consumers then
 *   differ: the `--mono-font-family` custom property is read through
 *   `var(--mono-font-family, …)`, so it still falls back to the built-in
 *   stack, while `xterm` takes the PLATFORM font and `document.fonts.load`
 *   rejects the same spec with a `SyntaxError`. A control character
 *   therefore becomes `\XX ` — a CSS hex escape, whose trailing space ends
 *   the hex digits. The space is ALWAYS emitted: a name is arbitrary text,
 *   so the character after the escape can itself be a hex digit.
 *
 * The two passes run in this ORDER on purpose. The control-character pass
 * emits backslashes, so running it first would leave the quote pass to
 * escape those backslashes again and turn each escape into literal text.
 */
export function buildFontFamily(fonts: string[]): string {
  return fonts
    .map(f => `"${f
      .replace(/["\\]/g, '\\$&')
      // eslint-disable-next-line no-control-regex
      .replace(/[\u0000-\u001F\u007F]/g, c => `\\${c.codePointAt(0)!.toString(16)} `)}"`)
    .join(', ')
}
