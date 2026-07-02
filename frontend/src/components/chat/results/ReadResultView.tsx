import type { JSX } from 'solid-js'
import type { AlertVariant } from '~/components/common/Alert'
import { createMemo, For, Show } from 'solid-js'
import { ansiSyncTokenize } from '~/lib/ansiTokenize'
import { guessLanguage } from '~/lib/languageMap'
import {
  codeViewContainer,
  codeViewContent,
  codeViewLine,
  codeViewLineNumber,
} from '../markdownEditor/codeViewStyles.css'
import { useAsyncCodeTokens } from '../useAsyncCodeTokens'
import { HIGHLIGHT_LINE_LIMIT } from './collapse'

/** A single parsed line from Read tool output. */
export interface ParsedCatLine {
  num: number
  text: string
}

/**
 * A `<tag>...</tag>` block Claude Code wraps around tool output (e.g. the leading
 * `<system-reminder>[Truncated: PARTIAL view ...]</system-reminder>` on a partial
 * read, or trailing usage reminders). Rendered as an oat alert rather than mixed
 * into the file body.
 */
export interface ReadReminder {
  /** Title-cased tag name (`system-reminder` -> "System Reminder"). */
  label: string
  /** Inner text of the block (HTML-escaped at render time). */
  text: string
  /** Alert variant inferred from the tag name; undefined renders the default info style. */
  variant?: AlertVariant
}

/** Read content split into its rendered parts: leading/trailing tag alerts + the cat-n body. */
export interface ParsedReadContent {
  leading: ReadReminder[]
  /** Parsed cat-n lines, or null when the body doesn't parse as cat-n format. */
  lines: ParsedCatLine[] | null
  trailing: ReadReminder[]
}

/** Regex to match a single Read output line: optional whitespace, digits, →, content. */
const CAT_N_LINE_RE = /^\s*(\d+)[→\t](.*)$/

/** Metadata suffix appended by Claude Code to tool results, e.g. [result-id: r7]. */
const RESULT_ID_RE = /^\[result-id: [^\]]+\]$/

/**
 * A whole-line single-line tag block: `<tag>inner</tag>`. Anchored at `^<` so a
 * cat-n body line (which always starts with its line number, e.g. `1\t</div>`)
 * can never be mistaken for a tag block. `\1` ties the close to the open tag.
 */
const SINGLE_LINE_TAG_RE = /^<([a-z][\w-]*)>(.*)<\/\1>\s*$/i
/** A bare opening tag on its own line: `<tag>` (multi-line block). */
const OPEN_TAG_RE = /^<([a-z][\w-]*)>\s*$/i
/** A bare closing tag on its own line: `</tag>` (multi-line block). */
const CLOSE_TAG_RE = /^<\/([a-z][\w-]*)>\s*$/i

/** Title-case a tag name: `system-reminder`/`other_tag` -> "System Reminder", `otherTag` -> "Other Tag". */
function tagLabel(tag: string): string {
  return tag
    .replace(/[-_]+/g, ' ') // kebab / snake -> spaces
    .replace(/([a-z\d])([A-Z])/g, '$1 $2') // camelCase -> spaced
    .trim()
    .split(/\s+/)
    .map(w => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ')
}

/** Infer an oat alert variant from words in the tag name; undefined -> default info style. */
function tagVariant(tag: string): AlertVariant | undefined {
  const t = tag.toLowerCase()
  if (t.includes('success'))
    return 'success'
  if (t.includes('warn'))
    return 'warning'
  if (t.includes('danger'))
    return 'danger'
  if (t.includes('error') || t.includes('fail'))
    return 'error'
  return undefined
}

function toReminder(tag: string, textLines: string[]): ReadReminder {
  return { label: tagLabel(tag), text: textLines.join('\n').trim(), variant: tagVariant(tag) }
}

/** Match a tag block at the HEAD of [lo, hi]; null when `lines[lo]` doesn't open one. */
function matchLeadingTag(lines: string[], lo: number, hi: number): { reminder: ReadReminder, closeIndex: number } | null {
  const single = lines[lo].match(SINGLE_LINE_TAG_RE)
  if (single)
    return { reminder: toReminder(single[1], [single[2]]), closeIndex: lo }
  const open = lines[lo].match(OPEN_TAG_RE)
  if (!open)
    return null
  const closeRe = new RegExp(`^</${open[1]}>\\s*$`, 'i')
  for (let i = lo + 1; i <= hi; i++) {
    if (closeRe.test(lines[i]))
      return { reminder: toReminder(open[1], lines.slice(lo + 1, i)), closeIndex: i }
  }
  return null // unterminated -> not a clean block
}

/** Match a tag block at the TAIL of [lo, hi]; null when `lines[hi]` doesn't close one. */
function matchTrailingTag(lines: string[], lo: number, hi: number): { reminder: ReadReminder, openIndex: number } | null {
  const single = lines[hi].match(SINGLE_LINE_TAG_RE)
  if (single)
    return { reminder: toReminder(single[1], [single[2]]), openIndex: hi }
  const close = lines[hi].match(CLOSE_TAG_RE)
  if (!close)
    return null
  const openRe = new RegExp(`^<${close[1]}>\\s*$`, 'i')
  for (let i = hi - 1; i >= lo; i--) {
    if (openRe.test(lines[i]))
      return { reminder: toReminder(close[1], lines.slice(i + 1, hi)), openIndex: i }
  }
  return null
}

/** Parse [lo, hi] as cat-n lines; null if any line in range isn't cat-n. */
function parseCatLines(lines: string[], lo: number, hi: number): ParsedCatLine[] | null {
  if (lo > hi)
    return null
  const parsed: ParsedCatLine[] = []
  for (let i = lo; i <= hi; i++) {
    const m = lines[i].match(CAT_N_LINE_RE)
    if (!m)
      return null
    parsed.push({ num: Number.parseInt(m[1], 10), text: m[2] })
  }
  return parsed
}

/**
 * Split Read tool output into the leading/trailing `<tag>...</tag>` blocks Claude
 * Code wraps around it and the cat-n file body between them. Tag blocks are peeled
 * off both ends (multiple, single- or multi-line, with interleaved blank lines and
 * trailing `[result-id: ...]` metadata which is discarded); the middle is parsed as
 * cat-n. Whole-line, `^<`-anchored matching keeps a code line like `5\t</div>` from
 * being mistaken for a tag. `lines` is null when the middle isn't cat-n.
 */
export function parseReadContent(content: string): ParsedReadContent {
  if (!content)
    return { leading: [], lines: null, trailing: [] }
  const lines = content.split('\n')
  let lo = 0
  let hi = lines.length - 1
  const leading: ReadReminder[] = []
  const trailing: ReadReminder[] = []

  // Peel tag blocks off the TAIL, skipping trailing blanks + [result-id] (discarded).
  for (;;) {
    while (hi >= lo && (lines[hi] === '' || RESULT_ID_RE.test(lines[hi])))
      hi--
    if (hi < lo)
      break
    const block = matchTrailingTag(lines, lo, hi)
    if (!block)
      break
    trailing.unshift(block.reminder) // prepend to keep document order
    hi = block.openIndex - 1
  }

  // Peel tag blocks off the HEAD, skipping leading blanks (discarded).
  for (;;) {
    while (lo <= hi && lines[lo] === '')
      lo++
    if (lo > hi)
      break
    const block = matchLeadingTag(lines, lo, hi)
    if (!block)
      break
    leading.push(block.reminder)
    lo = block.closeIndex + 1
  }

  return { leading, lines: parseCatLines(lines, lo, hi), trailing }
}

/**
 * Parse Read tool output content into structured cat-n lines, or null when it
 * doesn't match the expected `<num><tab|→><content>` format. Thin wrapper over
 * {@link parseReadContent} for callers that only need the file body (the leading/
 * trailing tag blocks are handled separately as alerts).
 */
export function parseCatNContent(content: string): ParsedCatLine[] | null {
  return parseReadContent(content).lines
}

/**
 * Syntax-highlighted code view for Read tool results.
 * Renders line numbers alongside Shiki-highlighted code content.
 * Tokenization runs in a Web Worker to avoid blocking the main thread.
 */
export function ReadResultView(props: {
  lines: ParsedCatLine[]
  filePath?: string
  /** Hidden DOM premeasurement keeps line geometry but skips Shiki/token workers. */
  premeasureMode?: boolean
  /** Visible scrolling keeps line geometry but skips Shiki/token workers until idle. */
  syntaxHighlightingPaused?: boolean
  /** Active browser selection: keep existing tokens but avoid replacing text nodes. */
  textSelectionActive?: () => boolean
  /** Row outside the near-viewport band: tokenize at low worker priority. */
  rowOffscreen?: () => boolean
}): JSX.Element {
  // Memoized (like useDiffTokens' `lang`) so the hook's 2-4 reads per reactive pass --
  // both effects' currentKey(), the seed, and syncTokenize -- don't each re-run extname()
  // + the EXT_TO_LANG lookup.
  const lang = createMemo(() => props.filePath ? guessLanguage(props.filePath) : undefined)
  // Memoized so the O(lines) join isn't rebuilt on every read -- the hook reads `code`
  // from both effects' currentKey() plus syncTokenize (2-3x per reactive pass).
  const code = createMemo(() => props.lines.map(line => line.text).join('\n'))

  const tokenizedLines = useAsyncCodeTokens({
    lang,
    code,
    // Line-count gate (very large files skip highlighting); lang presence is checked
    // by the hook before this runs.
    eligible: () => props.lines.length > 0 && props.lines.length <= HIGHLIGHT_LINE_LIMIT,
    gate: () => ({
      premeasure: props.premeasureMode === true,
      hold: props.syntaxHighlightingPaused === true || props.textSelectionActive?.() === true,
    }),
    // ANSI is a special Shiki built-in -- tokenize synchronously on the main thread
    // (the worker's Oniguruma core has no `ansi` grammar). null falls through to the
    // worker, which renders it plain. Shared with the diff sides / gap-context lines so
    // a `.log` file highlights identically wherever it appears.
    syncTokenize: ansiSyncTokenize,
    rowOffscreen: () => props.rowOffscreen?.() === true,
  })

  // Dynamic line number column width based on the largest line number
  const lineNumWidth = createMemo(() => {
    const maxNum = props.lines.length > 0
      ? props.lines.at(-1)!.num
      : 0
    return `${Math.max(String(maxNum).length, 1)}ch`
  })

  return (
    <div class={codeViewContainer}>
      <For each={props.lines}>
        {(line, index) => {
          const tokens = () => {
            const t = tokenizedLines()
            return t?.[index()] ?? null
          }
          return (
            <div class={codeViewLine} data-line-num={line.num}>
              <span
                class={codeViewLineNumber}
                style={{ width: lineNumWidth() }}
              >
                {line.num}
              </span>
              <span class={codeViewContent}>
                <Show
                  when={tokens()}
                  fallback={line.text}
                >
                  <For each={tokens()!}>
                    {token => (
                      // `data-shiki-token` marks THIS as a syntax token so the dual-theme
                      // color rule targets it precisely -- not the line-number span, which
                      // also carries an inline `style` (its width) and would otherwise match
                      // a bare `span[style]` selector and lose its faint color. Mirrors the
                      // Bash/JSON token markup (TokenizedCode).
                      <span data-shiki-token style={token.htmlStyle as JSX.CSSProperties}>{token.content}</span>
                    )}
                  </For>
                </Show>
              </span>
            </div>
          )
        }}
      </For>
    </div>
  )
}
