import type { SpanLine } from './widgets/SpanLines'

/**
 * Wire-shape validation for a message's `span_lines` payload, extracted from the
 * classified-entry cache so the malformed-payload rejection is testable without
 * standing up the cache. Pure: depends only on the `SpanLine` shape and JSON.
 */

/**
 * A parsed span_lines element is a renderable column iff it's the null sentinel
 * (a blank column) or a non-array object carrying a string `type` -- the field
 * classFor dispatches on. Rejects primitives, arrays, and shapeless objects that
 * would otherwise render as a junk column.
 */
function isSpanLineColumn(el: unknown): el is SpanLine | null {
  if (el === null)
    return true
  return typeof el === 'object' && !Array.isArray(el) && typeof (el as { type?: unknown }).type === 'string'
}

export function parseSpanLines(raw: string | undefined): (SpanLine | null)[] {
  if (!raw || raw === '[]')
    return []
  try {
    // JSON.parse only throws on malformed JSON, not on a well-formed non-array
    // (a worker shipping `5`, `"x"`, or `{}` as span_lines parses cleanly). Those
    // would feed a non-array into `.length`/`<Index>` downstream -- a string
    // value would iterate its characters as bogus span columns. Gate on
    // Array.isArray so only a real array survives; anything else means "none".
    const parsed = JSON.parse(raw) as unknown
    if (!Array.isArray(parsed))
      return []
    // Elements must be a SpanLine object (or the null sentinel for a blank
    // column): a PRIMITIVE element (e.g. `[5, "x"]` from a malformed payload)
    // would reach classFor, which reads `.type`/`.color` off it and renders a
    // bogus colorless column (and a non-empty array flips hasSpanLines, adding a
    // wrong inter-row gap). `typeof === 'object'` alone is NOT enough -- it admits
    // both arrays (`typeof [] === 'object'`) and a bare `{}` with no `type`, each
    // of which classFor would still render as a junk column. Keep only the null
    // sentinel and objects that actually carry a string `type` (the field classFor
    // dispatches on), so a malformed element means "none" rather than a junk column.
    return parsed.filter(isSpanLineColumn)
  }
  catch {
    return []
  }
}
