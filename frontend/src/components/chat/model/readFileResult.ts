/**
 * How severe one reminder block claims to be, read from its tag name.
 *
 * The model's own vocabulary, not the `Alert` component's `variant`. The two
 * happen to spell the same four words today, and the renderer still maps one
 * onto the other: the model must not import a component's prop type, or a change
 * to how an alert LOOKS reaches back into what a Read result MEANS.
 *
 * A tag that claims nothing carries no severity, which the renderer draws in
 * the default informational style.
 */
export type ReminderSeverity = 'success' | 'warning' | 'danger' | 'error'

/** A single parsed line from Read tool output. */
export interface NumberedFileLine {
  /**
   * The line number, or null for an elision row: a row that stands for lines the
   * provider left out of the read, such as omp's `…` in a summarized file.
   */
  num: number | null
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
  /** Severity inferred from the tag name; undefined renders the default info style. */
  severity?: ReminderSeverity
}

/** Read content split into its rendered parts: leading/trailing tag alerts + the cat-n body. */
export interface ReadContentParts {
  leading: ReadReminder[]
  /** Parsed cat-n lines, or null when the body doesn't parse as cat-n format. */
  lines: NumberedFileLine[] | null
  trailing: ReadReminder[]
}

/**
 * The file one Read call returned. `lines` is null for raw
 * text that doesn't parse as cat-n format (or non-text Read variants on
 * Claude — image/notebook/pdf/parts/file_unchanged); the body falls back to
 * `fallbackContent` in that case.
 *
 * The path is not here: the call's REQUEST states it, and one call holds
 * both halves.
 */
export interface ReadFileResult {
  /** Pre-parsed cat-n lines, synthesized file lines, or null when unparseable / non-text. */
  lines: NumberedFileLine[] | null
  /** Raw fallback content used when `lines` is null. */
  fallbackContent: string
  /** `<tag>...</tag>` blocks before the body (e.g. a partial-view notice), shown as alerts when expanded. */
  leading?: ReadReminder[]
  /** `<tag>...</tag>` blocks after the body (e.g. usage reminders), shown as alerts when expanded. */
  trailing?: ReadReminder[]
}

/**
 * The lines a read body DRAWS, or null when it draws {@link ReadFileResult.fallbackContent}.
 *
 * ONE predicate for one decision, because three readers must not disagree about an
 * EMPTY `lines` array. {@link readFileResultFromContent} builds `[]` deliberately --
 * an empty file that WAS read has zero lines -- and `[]` is truthy, so a reader that
 * tested the array alone drew an empty body for a read that stated its reason in
 * `fallbackContent`. That refused read then offered no Copy action and gave the
 * scroll rail nothing, for the one read whose reason is the only thing worth copying.
 */
export function readFileLines(source: ReadFileResult): NumberedFileLine[] | null {
  return source.lines && source.lines.length > 0 ? source.lines : null
}

/**
 * The file text a read body DRAWS.
 *
 * The parsed lines win, because a provider that returns its file already numbered
 * (`1\tfirst`) keeps those prefixes in `fallbackContent`, and the body strips them.
 * An extraction sets its copyable text from this, so the Copy action hands over the
 * text on screen rather than the wire form of it.
 */
export function readFileBodyText(source: ReadFileResult): string {
  const lines = readFileLines(source)
  return lines ? lines.map(line => line.text).join('\n') : source.fallbackContent
}

/** Regex to match a single Read output line: optional whitespace, digits, →, content. */
const CAT_N_LINE_RE = /^\s*(\d+)[→\t](.*)$/

/** Metadata suffix appended by Claude Code to tool results, e.g. [result-id: r7]. */
const RESULT_ID_RE = /^\[result-id: [^\]]+\]$/

/**
 * A whole-line single-line tag block: `<tag>inner</tag>`. Anchored at `^<` so a
 * cat-n body line (which always starts with its line number, e.g. `1\t</div>`)
 * can never be mistaken for a tag block. `\1` ties the close to the open tag.
 *
 * The anchor defends a CAT-N body alone. A raw file body carries no line numbers,
 * so `<p>hi</p>` as its last line matches this, and `</html>` as its last line
 * matches {@link CLOSE_TAG_RE} and then finds the file's own `<html>`. The cat-n
 * post-condition in {@link parseReadContent} is what protects that body.
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

/** Infer a severity from words in the tag name; undefined -> the tag claims none. */
function tagSeverity(tag: string): ReminderSeverity | undefined {
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
  const severity = tagSeverity(tag)
  // A tag that claims no severity omits the field, which the renderer draws in its
  // default style -- the same pixels an explicit undefined stated before.
  return { label: tagLabel(tag), text: textLines.join('\n').trim(), ...(severity !== undefined ? { severity } : {}) }
}

/** Match a tag block at the HEAD of [lo, hi]; null when `lines[lo]` doesn't open one. */
function matchLeadingTag(lines: string[], lo: number, hi: number): { reminder: ReadReminder, closeIndex: number } | null {
  const line = lines[lo] ?? ''
  const single = line.match(SINGLE_LINE_TAG_RE)
  if (single && single[1] !== undefined && single[2] !== undefined)
    return { reminder: toReminder(single[1], [single[2]]), closeIndex: lo }
  const open = line.match(OPEN_TAG_RE)
  if (!open || open[1] === undefined)
    return null
  const closeRe = new RegExp(`^</${open[1]}>\\s*$`, 'i')
  for (let i = lo + 1; i <= hi; i++) {
    if (closeRe.test(lines[i] ?? ''))
      return { reminder: toReminder(open[1], lines.slice(lo + 1, i)), closeIndex: i }
  }
  return null // unterminated -> not a clean block
}

/** Match a tag block at the TAIL of [lo, hi]; null when `lines[hi]` doesn't close one. */
function matchTrailingTag(lines: string[], lo: number, hi: number): { reminder: ReadReminder, openIndex: number } | null {
  const line = lines[hi] ?? ''
  const single = line.match(SINGLE_LINE_TAG_RE)
  if (single && single[1] !== undefined && single[2] !== undefined)
    return { reminder: toReminder(single[1], [single[2]]), openIndex: hi }
  const close = line.match(CLOSE_TAG_RE)
  if (!close || close[1] === undefined)
    return null
  const openRe = new RegExp(`^<${close[1]}>\\s*$`, 'i')
  for (let i = hi - 1; i >= lo; i--) {
    if (openRe.test(lines[i] ?? ''))
      return { reminder: toReminder(close[1], lines.slice(i + 1, hi)), openIndex: i }
  }
  return null
}

/** Parse [lo, hi] as cat-n lines; null if any line in range isn't cat-n. */
function parseCatLines(lines: string[], lo: number, hi: number): NumberedFileLine[] | null {
  if (lo > hi)
    return null
  const parsed: NumberedFileLine[] = []
  for (let i = lo; i <= hi; i++) {
    const m = (lines[i] ?? '').match(CAT_N_LINE_RE)
    if (!m || m[1] === undefined || m[2] === undefined)
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
 *
 * The peel APPLIES ONLY to a cat-n body, and the post-condition at the end is what
 * enforces it. A tag block here is the wrapper AROUND a numbered file body, so a
 * peel that left no cat-n body read the FILE rather than a wrapper: a plain
 * `index.html` ends in `</html>`, the tail matcher scans back to the file's own
 * `<html>`, and the whole document became one alert beside a `fallbackContent` that
 * already drew it -- the reader saw the file twice. The Agent Client Protocol
 * providers feed raw file text through here, so this is the common case rather than
 * a rare one.
 *
 * The cost is stated rather than hidden: output that is a tag block and NOTHING else
 * -- a truncation notice with no file body -- now reaches the reader as raw text
 * rather than as an alert. No shape tells that notice apart from a three-line HTML
 * file, so one rule must answer both, and drawing the file twice is the worse
 * failure.
 */
export function parseReadContent(content: string): ReadContentParts {
  if (!content)
    return { leading: [], lines: null, trailing: [] }
  const lines = content.split('\n')
  let lo = 0
  let hi = lines.length - 1
  const leading: ReadReminder[] = []
  const trailing: ReadReminder[] = []

  // Peel tag blocks off the HEAD FIRST. A block that is the WHOLE output -- a
  // truncation notice with no file body -- matches the trailing single-line form
  // too, so peeling the tail first filed the one notice a reader needs above the
  // body underneath it instead.
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

  // Peel tag blocks off the TAIL, skipping trailing blanks + [result-id] (discarded).
  for (;;) {
    while (hi >= lo && (lines[hi] === '' || RESULT_ID_RE.test(lines[hi] ?? '')))
      hi--
    if (hi < lo)
      break
    const block = matchTrailingTag(lines, lo, hi)
    if (!block)
      break
    trailing.unshift(block.reminder) // prepend to keep document order
    hi = block.openIndex - 1
  }

  // The post-condition the doc states: the blocks survive only when a cat-n body
  // survives with them.
  const body = parseCatLines(lines, lo, hi)
  return body ? { leading, lines: body, trailing } : { leading: [], lines: null, trailing: [] }
}

/**
 * Parse Read tool output content into structured cat-n lines, or null when it
 * doesn't match the expected `<num><tab|→><content>` format. Thin wrapper over
 * {@link parseReadContent} for callers that only need the file body (the leading/
 * trailing tag blocks are handled separately as alerts).
 */
export function parseCatNContent(content: string): NumberedFileLine[] | null {
  return parseReadContent(content).lines
}

/**
 * Build a ReadFileResult from raw file content plus a starting line number.
 *
 * Claude's structured Read payloads and Pi's plain-text Read results both carry
 * real file content rather than cat-n output. Normalizing them here lets every
 * provider state the same line-numbered body.
 */
export function readFileResultFromContent(args: {
  content: string
  startLine?: number
  fallbackContent?: string
}): ReadFileResult {
  const startLine = args.startLine ?? 1
  // `[]`, not null: an empty file that WAS read has zero lines, and `null` in this
  // module means "this build could not parse it". The readers below are what make
  // `fallbackContent` reachable for an empty list.
  const lines = args.content
    ? args.content.split('\n').map((text, i) => ({ num: startLine + i, text }))
    : []
  return {
    lines,
    fallbackContent: args.fallbackContent ?? args.content,
  }
}
