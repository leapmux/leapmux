import type { JSX } from 'solid-js'
import type { FileListEntry, SearchBodyKind, SearchResult } from '../ir/searchResult'
import type { RenderContext } from '../messageRenderers'
import { createMemo, For, Show } from 'solid-js'
import { relativizePath } from '~/lib/paths'
import { pluralize } from '~/lib/plural'
import { searchResultText } from '../ir/searchResult'
import { getToolResultExpanded } from '../messageRenderers'
import {
  toolMessage,
  toolResultCollapsed,
  toolResultContentPre,
  toolResultPrompt,
} from '../toolStyles.css'
import { TRUNCATION_NOTICE } from '../truncationNotice'
import { useCollapsedItems, useCollapsedLines } from './useCollapsedLines'

/** Reusable file paths with optional provider-supplied details. */
export function FileListView(props: {
  entries: FileListEntry[]
  context?: RenderContext
}): JSX.Element {
  return (
    <div class={toolResultContentPre}>
      <For each={props.entries}>
        {(f, i) => (
          <>
            {i() > 0 && '\n'}
            {relativizePath(f.path, props.context?.workingDir, props.context?.homeDir)}
            <Show when={f.detail}>{detail => `\t${detail()}`}</Show>
          </>
        )}
      </For>
    </div>
  )
}

/** One whitespace character, as `String.prototype.trim` defines the set. Never global: `test` would carry a `lastIndex`. */
const WHITESPACE_RE = /\s/

/**
 * Whether the body is the summary again, with nothing but whitespace around it.
 *
 * Scans inward from both ends rather than calling `trim()`, which allocates a second
 * copy of the whole body: a 4,000-match grep holds 400 KB, and the row re-reads this
 * on every streamed frame. Each scan stops at the first character that is not
 * whitespace, so the cost is what the padding costs and never what the body costs.
 *
 * A length window guarded the `trim()` before, and it was NARROWER than what `trim()`
 * strips -- two characters of padding. "No matches found\n\n\n" is 19 characters
 * against a 16-character summary, so the window refused the very case it guards and
 * the row printed the same sentence twice.
 */
function isSummaryRepeated(text: string, summary: string): boolean {
  if (text.length < summary.length)
    return false
  let start = 0
  let end = text.length
  // The bounds keep both indices inside the string; `?? ''` is the type-level
  // guard alone (`''` never matches `\s`).
  while (start < end && WHITESPACE_RE.test(text[start] ?? ''))
    start++
  while (end > start && WHITESPACE_RE.test(text[end - 1] ?? ''))
    end--
  return end - start === summary.length && text.startsWith(summary, start)
}

/**
 * The muted marker for a search that found nothing, or no summary at all.
 *
 * The EXTRACTOR decides, because only it read the provider's bytes. This asked the
 * question itself before, by comparing `fallbackContent` against the marker below --
 * LeapMux's own user-interface prose measured against a provider's output, in the one
 * layer that `results/README.md` keeps free of every provider. The counters cannot
 * answer it: a tool that found nothing and a body the extractor could not classify
 * both report no file, no line and no count.
 */
function emptySummaryFor(source: SearchResult, marker: string): string {
  return source.empty ? marker : ''
}

function summaryFor(source: SearchResult, kind: SearchBodyKind): string {
  if (kind === 'grep') {
    // Count mode: total occurrences across N files, even when zero.
    if (source.mode === 'count' && typeof source.matchCount === 'number')
      return `${pluralize(source.matchCount, 'match', 'matches')} in ${pluralize(source.numFiles, 'file')}`
    if (source.numLines > 0 && source.numFiles > 0)
      return `${pluralize(source.numLines, 'match', 'matches')} in ${pluralize(source.numFiles, 'file')}`
    if (source.numFiles > 0)
      return `Found ${pluralize(source.numFiles, 'file')}`
    // A grep that counted MATCHES and no files states the count on its own. Cursor
    // reports `totalMatches` with no file total, so this row read "No matches found"
    // for a search that found nine.
    if (typeof source.matchCount === 'number' && source.matchCount > 0)
      return `Found ${pluralize(source.matchCount, 'match', 'matches')}`
    return emptySummaryFor(source, 'No matches found')
  }
  if (kind === 'glob') {
    if (source.numFiles > 0)
      return `Found ${pluralize(source.numFiles, 'file')}`
    return emptySummaryFor(source, 'No files found')
  }
  // ACP search
  if (typeof source.matchCount === 'number' && source.matchCount >= 0) {
    if (source.matchCount === 0)
      return 'No matches found'
    return `Found ${pluralize(source.matchCount, 'match', 'matches')}`
  }
  return ''
}

export function SearchResultBody(props: {
  source: SearchResult
  kind: SearchBodyKind
  /** The pattern the search ran, when the caller holds it apart from the source. */
  context?: RenderContext
}): JSX.Element {
  const expanded = () => getToolResultExpanded(props.context)
  const filenames = () => props.source.filenames
  const summary = createMemo(() => summaryFor(props.source, props.kind))
  const content = createMemo(() => {
    // A result the extractor RECOGNIZED as empty says all it has to say in the
    // summary, whatever words the provider chose for it. Without this the row drew
    // the marker and the provider's own sentence under it, one above the other.
    if (props.source.empty)
      return ''
    const text = searchResultText(props.source, props.context)
    // The body says nothing the summary above has not said, so it draws nothing.
    return isSummaryRepeated(text, summary()) ? '' : text
  })
  const filenameCollapse = useCollapsedItems<string>({ items: filenames, expanded })
  const contentCollapse = useCollapsedLines({ text: content, expanded })
  const isCollapsed = () => filenameCollapse.isCollapsed() || contentCollapse.isCollapsed()
  const displayFilenames = filenameCollapse.displayItems
  const displayContent = contentCollapse.display
  // ONE wrapper per path, kept for as long as the row lives. `FileListView` takes
  // entries and a search result holds bare paths, so the two met through a `map` that
  // built fresh objects on every read -- and `<For>` keys by REFERENCE, so a streaming
  // grep threw away the whole DOM list and rebuilt it once per frame.
  const entryCache = new Map<string, FileListEntry>()
  const fileEntries = createMemo(() => displayFilenames().map((path) => {
    let entry = entryCache.get(path)
    if (!entry) {
      entry = { path }
      entryCache.set(path, entry)
    }
    return entry
  }))

  return (
    <div class={`${toolMessage}${isCollapsed() ? ` ${toolResultCollapsed}` : ''}`}>
      <Show when={summary()}>
        <div class={toolResultPrompt}>{summary()}</div>
      </Show>
      <Show when={displayFilenames().length > 0}>
        <FileListView entries={fileEntries()} {...(props.context !== undefined ? { context: props.context } : {})} />
      </Show>
      <Show when={displayContent()}>
        <div class={toolResultContentPre}>{displayContent()}</div>
      </Show>
      <Show when={props.source.notice || props.source.truncated}>
        <div class={toolResultPrompt}>{props.source.notice || TRUNCATION_NOTICE}</div>
      </Show>
    </div>
  )
}
