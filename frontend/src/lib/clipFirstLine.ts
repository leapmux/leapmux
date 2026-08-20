import { cutAtCodeUnit, PREVIEW_ELLIPSIS } from './markdownSafeCut'

/**
 * The first line of `text`, capped at `limit` code units and given an ellipsis
 * when it was cut.
 *
 * For a one-line label built from model prose: a tool-card header, a tool-use
 * summary. Not for a preview of markdown -- `truncatePreview` is that, and it
 * keeps paragraph structure and cuts on a grapheme boundary, which is the wrong
 * shape here because the caller wants exactly one line.
 *
 * `cutAtCodeUnit`, not `slice`. A raw slice at a fixed offset can land between
 * the two halves of a surrogate pair, and an emoji or a CJK-extension character
 * in the model's prose then leaves a lone surrogate that the browser renders as
 * a replacement glyph. The backend's own equivalent (bgtask.TruncateRunes)
 * counts runes for the same reason.
 *
 * Returns "" when the text is blank, so a caller tests one value rather than
 * testing for blankness itself.
 */
export function clipFirstLine(text: string, limit: number): string {
  // \r included: a CRLF payload would otherwise leave the carriage return on the
  // end of the line, where it renders as nothing and defeats a length check.
  const line = text.trim().split(/\r?\n/, 1)[0].trim()
  return line.length > limit ? `${cutAtCodeUnit(line, limit)}${PREVIEW_ELLIPSIS}` : line
}
