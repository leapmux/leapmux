import type { SearchMode } from './searchMode'
import { relativizePath } from '~/lib/paths'
import { COLLAPSED_RESULT_ROWS, hasMoreLinesThan } from './collapse'

export interface SearchResultLine {
  filePath: string
  lineNumber?: number
  text: string
}

/**
 * Which search-shaped kind a row draws as: it words the summary and picks the title.
 *
 * It is the CALL's kind rather than the result's, so a provider that decides the kind
 * from the result -- Cursor reads its title and its match counters -- answers with one
 * of these. One name keeps the provider's answer and the renderer's switch in step.
 */
export type SearchBodyKind = 'glob' | 'grep' | 'search'

/**
 * What one search found: Grep, Glob and the ACP search kind share this result.
 * The KIND the row draws -- glob, grep or search -- travels on the CALL rather
 * than here, because the kind also picks the title and the icon that the summary
 * must agree with.
 */
export interface SearchResult {
  /**
   * The files the search matched, or empty for a search whose corpus holds no files.
   *
   * A `search` call is a query against a corpus the SESSION holds, and only some of
   * those corpora answer with files. A tool-registry probe and a language-server
   * lookup both state their matches in `content`, which the body prints unchanged.
   * Leave this empty rather than inventing a path for a match that has none.
   */
  filenames: string[]
  /** Grep-style content blob (line:text or file:line:text). Empty otherwise. */
  content: string
  /** Structured matches keep file paths separate from text that can contain path punctuation. */
  lines?: SearchResultLine[]
  numFiles: number
  numLines: number
  /**
   * How many matches the tool itself counted, when it counted them.
   *
   * ONE field for one fact. Grep's count mode and the ACP search kind each report a
   * total, and the row words it differently for each -- but a source that carried the
   * two under different names let a grep built with the search field fall through to
   * the line-and-file sentence, which states a different number.
   */
  matchCount?: number
  /**
   * Result truncated by the tool's own cap (Glob explicit `truncated`,
   * Grep `appliedLimit != null`).
   */
  truncated: boolean
  /** A provider's explanation of an output limit. */
  notice?: string
  /** Glob: tool_use_result.durationMs. */
  durationMs?: number
  /** How the grep reported what it found. */
  mode?: SearchMode
  /** Raw fallback text shown when there's no structured output. */
  fallbackContent: string
  /**
   * The extractor RECOGNIZED an empty result: the tool ran and found nothing.
   *
   * A body the extractor could NOT classify is not empty. The two are
   * indistinguishable from the counters alone -- `numFiles`, `numLines` and an
   * absent `matchCount` describe both -- so only the extractor that read the
   * provider's own bytes can tell them apart, and it states the answer here.
   *
   * Without it the renderer compared the provider's bytes against LeapMux's own
   * summary wording to guess, which put a provider's output format in the layer
   * that `results/README.md` keeps free of every provider.
   */
  empty: boolean
}

/** One path in a file list, with whatever detail the provider stated beside it. */
export interface FileListEntry {
  path: string
  detail?: string
}

/**
 * The structured matches the body draws, or null when the body draws TEXT instead.
 *
 * ONE predicate for one decision, because two readers must not disagree about an
 * EMPTY `lines` array. A provider that found the field but recovered no match from it
 * still sets it -- `cursorSearchSource` always does -- and the body then falls through
 * to `fallbackContent`. A reader that took the empty array for the authoritative
 * answer reported "0 rows, nothing to expand" over text the body had already clipped,
 * so the reader saw four lines of a long output and no Expand control.
 */
function structuredLines(source: SearchResult): SearchResultLine[] | null {
  return source.lines?.length ? source.lines : null
}

/**
 * The text a search body shows, and the text its Copy action writes.
 *
 * Takes the two PATH fields rather than the render context they sit in: the
 * function relativizes file paths and reads nothing else, so a narrower parameter
 * keeps this module clear of the renderer.
 */
export function searchResultText(source: SearchResult, context?: { workingDir?: string | undefined, homeDir?: string | undefined }): string {
  const lines = structuredLines(source)
  if (lines)
    return lines.map(line => `${relativizePath(line.filePath, context?.workingDir, context?.homeDir)}${line.lineNumber !== undefined ? `:${line.lineNumber}` : ''}:${line.text}`).join('\n')
  return source.content || (source.filenames.length === 0 ? source.fallbackContent : '')
}

/**
 * The text a search row's Copy action writes.
 *
 * The FILE LIST is the answer for a search that returned one, so it stands in when
 * there is no match text: a grep in `files_with_matches` mode, and an ACP search that
 * reported only paths, both set `filenames` and leave `content` empty. Without this
 * the toolbar hid the Copy button over a body that visibly listed files, and only the
 * glob renderer patched it -- `toolCallMeta` reads `hasCopyable` from this same
 * getter, so the button and the text agree by construction.
 *
 * Separate from {@link searchResultText}, which the BODY draws: the body renders the
 * file list itself, so folding the fallback in there would print it twice.
 */
export function searchResultCopyable(source: SearchResult, context?: { workingDir?: string | undefined, homeDir?: string | undefined }): string {
  return searchResultText(source, context) || source.filenames.join('\n')
}

export function searchResultCollapsible(source: SearchResult): boolean {
  if (source.filenames.length > COLLAPSED_RESULT_ROWS)
    return true
  // From the COUNT, not the joined text. `searchResultText` maps and relativizes
  // every match to build one string that `hasMoreLinesThan` then stops reading after
  // four newlines -- and `toolCallMeta` calls this on every streamed frame, so a grep
  // over a large file paid for the whole join per frame to answer a boolean. One
  // structured line is one row, so the length answers it directly.
  const lines = structuredLines(source)
  if (lines)
    return lines.length > COLLAPSED_RESULT_ROWS
  return hasMoreLinesThan(searchResultText(source), COLLAPSED_RESULT_ROWS)
}
