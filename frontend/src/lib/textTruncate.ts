// ---------------------------------------------------------------------------
// General grapheme-aware snippet truncator
//
// Tidies an arbitrary text body into a bounded, single-snippet preview: collapse
// horizontal whitespace, cap blank-line runs, and truncate at a grapheme boundary
// with a trailing ellipsis. Provider- and feature-neutral (no mark-preview / message
// concerns), so any surface that needs a short snippet of a longer body can reuse it.
//
// Extracted from `~/components/chat/markPreviewShared` -- the scroll-rail mark preview
// was its first caller, which is why the exported names keep the `preview` prefix.
// ---------------------------------------------------------------------------

/** Longest preview kept; longer content is truncated with an ellipsis. */
const MAX_PREVIEW_LEN = 200
const PREVIEW_ELLIPSIS = '…'
// Hard cap on graphemes SCANNED (not just appended). The MAX_PREVIEW_LEN break bounds appended
// CONTENT, but whitespace / post-cap blank-line graphemes append nothing while the loop keeps
// segmenting, so a whitespace-dominated input (a huge leading/embedded run in a pasted body or a
// large tool_result) would segment its whole prefix on the main thread. 20x the output cap -- far
// more than any content-dense input needs to reach MAX_PREVIEW_LEN -- so it never truncates normal
// content, only bounds the pathological run.
const MAX_PREVIEW_SCAN = MAX_PREVIEW_LEN * 20

interface GraphemeSegment {
  segment: string
}

interface GraphemeSegmenter {
  segment: (input: string) => Iterable<GraphemeSegment>
}

interface GraphemeSegmenterConstructor {
  new (
    locales?: string | string[],
    options?: { granularity: 'grapheme' },
  ): GraphemeSegmenter
}

// A single grapheme segmenter reused across previews: constructing an Intl.Segmenter is not
// free, and `segment()` is stateless per call, so one instance serves every truncatePreview.
// `undefined` = not yet resolved; `null` = the runtime has no Intl.Segmenter (SSR / older
// engines), where we fall back to code-unit iteration.
let graphemeSegmenter: GraphemeSegmenter | null | undefined

function resolveGraphemeSegmenter(): GraphemeSegmenter | null {
  if (graphemeSegmenter !== undefined)
    return graphemeSegmenter
  const Segmenter = (Intl as typeof Intl & { Segmenter?: GraphemeSegmenterConstructor }).Segmenter
  graphemeSegmenter = Segmenter ? new Segmenter(undefined, { granularity: 'grapheme' }) : null
  return graphemeSegmenter
}

/**
 * Test-only: override the memoized grapheme segmenter. Pass `null` to exercise the
 * code-point fallback path (the runtime has no Intl.Segmenter), or `undefined` to clear the
 * override so the next call re-resolves the real one.
 */
export function __setGraphemeSegmenterForTest(value: GraphemeSegmenter | null | undefined): void {
  graphemeSegmenter = value
}

function* previewSegments(text: string): Iterable<string> {
  const segmenter = resolveGraphemeSegmenter()
  if (segmenter) {
    for (const part of segmenter.segment(text))
      yield part.segment
    return
  }
  // Fallback (no Intl.Segmenter -- SSR / older engines): iterate code points, but emit a
  // '\r\n' pair as ONE segment so a CRLF line ending matches the Segmenter's single grapheme.
  // Without this, the '\r' and the '\n' each hit truncatePreview's newline branch and one CRLF
  // double-counts into a paragraph break, diverging from the Segmenter path. Kept lazy (a
  // one-code-point lookahead) so truncatePreview's early break still bounds the iteration.
  const it = text[Symbol.iterator]()
  let cur = it.next()
  while (!cur.done) {
    const ch = cur.value
    cur = it.next()
    if (ch === '\r' && !cur.done && cur.value === '\n') {
      yield '\r\n'
      cur = it.next()
    }
    else {
      yield ch
    }
  }
}

/**
 * Tidy a message body into a bounded snippet for the tooltip. Horizontal whitespace
 * runs collapse to a single space and blank-line runs cap at one, but NEWLINES ARE
 * PRESERVED so the tooltip's markdown renderer keeps paragraph / blockquote / list
 * structure. The result is capped at MAX_PREVIEW_LEN with a trailing ellipsis. Returns
 * null for empty input so callers fall back to a mark-type label.
 */
export function truncatePreview(text: string | null | undefined): string | null {
  if (!text)
    return null
  const out: string[] = []
  let seenContent = false
  let pendingSpace = false
  let newlineRun = 0
  let truncated = false
  let scanned = 0

  const append = (part: string) => {
    out.push(part)
    if (out.length > MAX_PREVIEW_LEN) {
      truncated = true
      return false
    }
    return true
  }

  for (const ch of previewSegments(text)) {
    // Stop after MAX_PREVIEW_SCAN graphemes even if none appended: content-dense input hits the
    // append cap (out.length > MAX_PREVIEW_LEN) at scanned ~= MAX_PREVIEW_LEN, far below this, so
    // normal previews are unaffected -- this only bounds a whitespace-dominated run that would
    // otherwise segment the whole (possibly huge) input. Both segmenter paths are generators, so
    // breaking stops pulling, structurally bounding the work.
    if (++scanned > MAX_PREVIEW_SCAN) {
      // Stopped scanning before consuming all input, so any content past this whitespace-dominated
      // run is dropped -- mark truncated so the trailing ellipsis signals it (the append cap already
      // sets `truncated` for content-dense input; this covers the scan-cap path, which otherwise
      // returned a silently-cut snippet with no `…`).
      truncated = true
      break
    }
    // A bare '\r' (classic-Mac line ending) is its own grapheme, distinct from the '\r\n'
    // cluster; treat it as a newline too rather than letting it fall through to the
    // horizontal-whitespace branch below (which would collapse the line break to a space).
    if (ch === '\n' || ch === '\r\n' || ch === '\r') {
      pendingSpace = false
      if (!seenContent)
        continue
      if (newlineRun < 2 && !append('\n'))
        break
      newlineRun = Math.min(newlineRun + 1, 2)
      continue
    }

    if (/\s/u.test(ch)) {
      if (seenContent)
        pendingSpace = true
      continue
    }

    if (pendingSpace && newlineRun === 0 && !append(' '))
      break
    pendingSpace = false
    if (!append(ch))
      break
    seenContent = true
    newlineRun = 0
  }

  while (out.length > 0 && /\s/u.test(out[out.length - 1]))
    out.pop()
  if (out.length === 0)
    return null
  return truncated ? `${out.slice(0, MAX_PREVIEW_LEN).join('')}${PREVIEW_ELLIPSIS}` : out.join('')
}
