/**
 * Write `value` as the content of a double-quoted CSS attribute value.
 *
 * A tool call ID can hold any character, and a Cursor call ID holds a line break. The function puts a backslash
 * before a quote and a backslash. It writes a line break as a hex escape followed by a space, because a quoted CSS
 * string cannot hold a line break. The result needs no browser, unlike `CSS.escape`.
 */
export function cssAttributeValue(value: string): string {
  return value
    .replace(/["\\]/g, '\\$&')
    .replace(/[\n\r\f]/g, lineBreak => `\\${lineBreak.codePointAt(0)!.toString(16)} `)
}
