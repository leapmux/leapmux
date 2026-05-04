import type { Accessor } from 'solid-js'
import { createMemo } from 'solid-js'
import { COLLAPSED_RESULT_ROWS } from '../toolRenderers'

/**
 * Per-line character cap applied to the collapsed slice. The visual fade mask
 * (`toolResultCollapsed`) only clips height — the browser still lays out
 * everything inside the clipped box, so a single 3MB line (e.g., a grep hit
 * on a minified `.js.map`) tanks scroll perf even when the message looks
 * "collapsed." Clipping each retained line keeps DOM/layout cost bounded
 * regardless of input size; expanded mode is unaffected.
 */
export const COLLAPSED_LINE_CHAR_CAP = 240
const TRUNCATION_INDICATOR = '…'

/**
 * Emit `text`'s `\n`-separated lines, replacing any line longer than `cap`
 * with `<first cap chars>…`. Always allocates — callers that already know no
 * line exceeds the cap should return their input directly to avoid the copy.
 */
function emitClippedLines(text: string, cap: number): string {
  const parts: string[] = []
  let start = 0
  while (start <= text.length) {
    const next = text.indexOf('\n', start)
    const end = next === -1 ? text.length : next
    parts.push(end - start > cap ? `${text.slice(start, start + cap)}${TRUNCATION_INDICATOR}` : text.slice(start, end))
    if (next === -1)
      break
    parts.push('\n')
    start = next + 1
  }
  return parts.join('')
}

/**
 * Equivalent to `text.split('\n').length > threshold` but stops scanning as
 * soon as the threshold is exceeded — avoids the full-array allocation for
 * long bash/tool outputs where only the count matters.
 */
export function hasMoreLinesThan(text: string, threshold: number): boolean {
  let needed = threshold
  let idx = 0
  while (needed > 0) {
    const next = text.indexOf('\n', idx)
    if (next === -1)
      return false
    needed--
    idx = next + 1
  }
  return true
}

export interface UseCollapsedLinesOptions {
  /** Full body text to potentially collapse. */
  text: Accessor<string>
  /** Whether the consumer wants the body expanded (no collapse). */
  expanded: Accessor<boolean>
  /** Row threshold above which the body collapses. Defaults to `COLLAPSED_RESULT_ROWS`. */
  threshold?: number
}

export interface UseCollapsedLinesResult {
  /** True when the body is currently collapsed (over threshold and not expanded). */
  isCollapsed: Accessor<boolean>
  /** The display string — collapsed slice or full text. Memoized. */
  display: Accessor<string>
}

/**
 * Compute the standard collapse-N-lines presentation for a tool result body.
 * The same `text + expanded + threshold → {isCollapsed, display}` formula
 * appears in every result body (`commandResult`, `searchResult`,
 * `readFileResult`, `webFetchResult`, `agent`, `taskOutput`, etc.); this
 * hook centralizes the memoization so each consumer doesn't reimplement it.
 *
 * Implemented via count-only scans (`hasMoreLinesThan` + `indexOf`) so large
 * tool outputs never pay the full `split('\n')` cost.
 */
export function useCollapsedLines(opts: UseCollapsedLinesOptions): UseCollapsedLinesResult {
  const threshold = opts.threshold ?? COLLAPSED_RESULT_ROWS
  const isCollapsed = useCollapsedFlag(opts)
  const display = createMemo(() => {
    if (!isCollapsed())
      return opts.text()
    const text = opts.text()
    const cap = COLLAPSED_LINE_CHAR_CAP
    let idx = 0
    let hasLong = false
    for (let i = 0; i < threshold; i++) {
      const next = text.indexOf('\n', idx)
      if (next === -1)
        return emitClippedLines(text, cap)
      if (next - idx > cap)
        hasLong = true
      idx = next + 1
    }
    const slice = text.slice(0, idx - 1)
    return hasLong ? emitClippedLines(slice, cap) : slice
  })
  return { isCollapsed, display }
}

/**
 * Memoized collapse-flag for kinds that always render the full text and only
 * flip a fade class (`'markdown-tool-result'`, `'json'`). Equivalent to the
 * `isCollapsed` half of {@link useCollapsedLines} without computing the
 * unused `display` slice.
 */
export function useCollapsedFlag(opts: UseCollapsedLinesOptions): Accessor<boolean> {
  const threshold = opts.threshold ?? COLLAPSED_RESULT_ROWS
  return createMemo(() => !opts.expanded() && hasMoreLinesThan(opts.text(), threshold))
}

export interface UseCollapsedItemsOptions<T> {
  /** Items to potentially collapse. */
  items: Accessor<T[]>
  /** Whether the consumer wants the body expanded (no collapse). */
  expanded: Accessor<boolean>
  /** Row threshold above which the items collapse. Defaults to `COLLAPSED_RESULT_ROWS`. */
  threshold?: number
}

export interface UseCollapsedItemsResult<T> {
  /** True when the items are currently collapsed (over threshold and not expanded). */
  isCollapsed: Accessor<boolean>
  /** Items to render — sliced when collapsed, full array otherwise. */
  displayItems: Accessor<T[]>
}

/**
 * Generic-array equivalent of `useCollapsedLines` for already-tokenized
 * collections (parsed cat-n lines, file path lists, etc.) that share the
 * same N-row collapse threshold.
 */
export function useCollapsedItems<T>(opts: UseCollapsedItemsOptions<T>): UseCollapsedItemsResult<T> {
  const threshold = opts.threshold ?? COLLAPSED_RESULT_ROWS
  const isCollapsed = createMemo(() => !opts.expanded() && opts.items().length > threshold)
  const displayItems = createMemo(() => isCollapsed() ? opts.items().slice(0, threshold) : opts.items())
  return { isCollapsed, displayItems }
}
