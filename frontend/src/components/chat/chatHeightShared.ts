import type { HeightInput } from './chatHeightEstimator'
import type { ContentBlock } from '~/lib/contentBlocks'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { asContentArray, getMessageContent, joinContentParagraphs } from '~/lib/contentBlocks'
import { isObject } from '~/lib/jsonPick'

/**
 * Pure text/block height-extraction primitives shared by the orchestrator
 * (`chatHeightInput.ts`) and the per-provider `heightMetrics` hooks. Kept in its
 * own module so the provider hooks can reuse the text measurements WITHOUT
 * importing the orchestrator (which pulls in the provider registry). This file
 * imports no registry, no provider, and no SolidJS, so it stays a leaf with no
 * runtime cycle. Diff geometry lives in its sibling leaf `chatDiffGeometry.ts`
 * (which reuses `countLines` from here).
 */

/**
 * True when a row carries diff geometry (unified or split rows) and must be sized
 * as a diff block, regardless of which side produced it (a Codex/ACP tool_use or a
 * Claude/Pi tool_result). The single home for the diff-precedence rule that the
 * height-input builder (buildHeightInput) and the estimator (estimateRowHeight)
 * both gate on, so a future third diff field can't be added to one check but
 * forgotten in the other -- which would size a diff row as plain text, the
 * under-estimate / drift direction the estimator guards against.
 */
export function rowCarriesDiff(x: { diffUnifiedRows?: number, diffSplitRows?: number }): boolean {
  return x.diffUnifiedRows != null || x.diffSplitRows != null
}

// ---------------------------------------------------------------------------
// Text measurements
// ---------------------------------------------------------------------------

/** Hard-line count of a text body (number of '\n'-separated lines, >= 0). */
export function countLines(text: string): number {
  if (!text)
    return 0
  let n = 1
  for (let i = 0; i < text.length; i++) {
    if (text.charCodeAt(i) === 10)
      n++
  }
  return n
}

/**
 * Char count of each hard line (blank lines -> 0), for the per-line wrap model.
 * Capped at MAX_LINE_SAMPLES so a pasted multi-thousand-line log can't allocate
 * an unbounded array per visible row; beyond the cap the remaining lines fold
 * into one trailing virtual line so their wrap rows are still counted.
 *
 * Scans the string once for '\n' rather than `split('\n')` so a multi-MB body with
 * millions of newlines can't materialize a millions-entry transient array (a GC
 * spike on every cache-miss estimate) just to slice the first MAX_LINE_SAMPLES of
 * it -- only the bounded `head` array (<= MAX_LINE_SAMPLES + 1 entries) is ever
 * allocated. Result is identical to the old split-based form: the first
 * MAX_LINE_SAMPLES line lengths verbatim, then the remaining lines folded into one
 * trailing entry summing each line's length plus its '\n'.
 */
const MAX_LINE_SAMPLES = 2000
export function toLineLengths(text: string): number[] {
  if (!text)
    return []
  const head: number[] = []
  let rest = 0
  let folded = false
  let lineStart = 0
  // Iterate to text.length INCLUSIVE so the trailing segment (text after the last
  // '\n', or the whole string when there is none) is emitted just like split's
  // last element.
  for (let i = 0; i <= text.length; i++) {
    if (i < text.length && text.charCodeAt(i) !== 10)
      continue
    const lineLen = i - lineStart
    if (head.length < MAX_LINE_SAMPLES) {
      head.push(lineLen)
    }
    else {
      // +1 mirrors the old fold, which added the '\n' that separated each folded line.
      rest += lineLen + 1
      folded = true
    }
    lineStart = i + 1
  }
  if (folded)
    head.push(rest)
  return head
}

/** True when [start,end) is a CommonMark fence line: <=3 spaces then >=3 ` or ~. */
function isFenceLine(text: string, start: number, end: number): boolean {
  let i = start
  let spaces = 0
  while (i < end && text.charCodeAt(i) === 32 && spaces < 4) {
    i++
    spaces++
  }
  // 4+ leading spaces is an indented code block, not a fence
  if (spaces >= 4)
    return false
  // Fence char: ` (backtick, 96) or ~ (tilde, 126)
  const c = text.charCodeAt(i)
  if (c !== 96 && c !== 126)
    return false
  let run = 0
  while (i < end && text.charCodeAt(i) === c) {
    i++
    run++
  }
  return run >= 3
}

/**
 * Like {@link toLineLengths}, but markdown-aware: a line inside a fenced code block
 * (the ``` / ~~~ delimiters AND the body between them) is encoded as a NEGATIVE
 * length `-(len + 1)`, so the wrap model sizes it as ONE non-wrapping row -- a
 * rendered code block scrolls horizontally (overflow-x), it never wraps -- instead
 * of char-wrapping a long code line into dozens of phantom prose rows (the 7x
 * over-estimate this fixes). Prose lines keep their positive length (blank = 0); one
 * entry per line, so it stays aligned with logicalLineCount and folds its tail
 * exactly like toLineLengths.
 *
 * The two fence delimiter rows are counted as code rows even though they render as
 * the <pre> box edges (0 text rows): the two extra rows closely approximate the
 * block's vertical padding (--space-4 x2 ~= 1.4 code rows), so no separate chrome
 * term is needed. A folded tail (>MAX_LINE_SAMPLES) sums only its PROSE chars into the
 * one trailing entry; folded CODE lines are deliberately omitted from that sum (they
 * never wrap), and proseRowMetrics' `logicalLineCount - gaps` floor charges each of
 * them ~1 row instead. Summing folded code chars as prose char-wrapped a giant
 * code-block tail into thousands of phantom rows -- a large over-estimate for a
 * >MAX_LINE_SAMPLES fenced block (the bug this omission fixes).
 */
export function toMarkdownLineLengths(text: string): number[] {
  if (!text)
    return []
  const head: number[] = []
  let rest = 0
  let folded = false
  let lineStart = 0
  let inFence = false
  for (let i = 0; i <= text.length; i++) {
    if (i < text.length && text.charCodeAt(i) !== 10)
      continue
    const lineLen = i - lineStart
    const fence = isFenceLine(text, lineStart, i)
    const code = inFence || fence // fence delimiters AND body are code rows
    if (fence)
      inFence = !inFence
    if (head.length < MAX_LINE_SAMPLES) {
      head.push(code ? -(lineLen + 1) : lineLen)
    }
    else {
      // Code lines never wrap (one row each); folding their chars into the prose sum
      // would char-wrap them into phantom rows. Omit them -- the logicalLineCount floor
      // in proseRowMetrics already charges each dropped hard line >= 1 row.
      if (!code)
        rest += lineLen + 1
      folded = true
    }
    lineStart = i + 1
  }
  if (folded)
    head.push(rest)
  return head
}

/**
 * The displayed-body text metrics a text-bearing row charges: total char count,
 * logical line count, and per-line content lengths for the wrap model.
 */
export interface BodyTextMetrics {
  textLength: number
  logicalLineCount: number
  lineLengths: number[]
}

/**
 * Body text metrics for a raw string, by render mode. `'mono'` uses
 * {@link toLineLengths} (a 12px <pre> body, every hard line its own wrap row);
 * `'markdown'` uses the fence-aware {@link toMarkdownLineLengths} (a 14px markdown
 * body). The SINGLE builder of the `{textLength, logicalLineCount, lineLengths}`
 * shape shared by the orchestrator (`chatHeightInput.textMetrics`) and the
 * provider `heightMetrics` hooks (`monoBody`/`markdownBody`), so a body the
 * estimate sizes one way and a hook sizes another can't drift on the field set.
 */
export function bodyTextMetrics(text: string, mode: 'mono' | 'markdown'): BodyTextMetrics {
  return {
    textLength: text.length,
    logicalLineCount: countLines(text),
    lineLengths: mode === 'markdown' ? toMarkdownLineLengths(text) : toLineLengths(text),
  }
}

/**
 * Body text metrics for a 12px-mono pre body (per-line lengths via toLineLengths).
 * Carries `bodyMarkdown: false` so a caller spreading this can't forget to mark the
 * body mono -- the estimator reads the flag by truthiness, so the explicit false keeps
 * a later `...markdownBody`/`bodyMarkdown` spread from leaking in. Paired with
 * {@link markdownBody} so the `bodyTextMetrics(text, mode)` + matching `bodyMarkdown`
 * flag travel together; the per-provider height hooks (claude/codex/acp) all spread
 * these rather than re-pairing the metrics and flag inline.
 */
export function monoBody(text: string): Partial<HeightInput> {
  return { ...bodyTextMetrics(text, 'mono'), bodyMarkdown: false }
}

/** Body text metrics for a 14px-markdown body (fence-aware per-line lengths). */
export function markdownBody(text: string): Partial<HeightInput> {
  return { ...bodyTextMetrics(text, 'markdown'), bodyMarkdown: true }
}

/**
 * The collapsed cat-n Read body sized mono: the parsed lines (reminder/tag alerts
 * stripped -- they render only when expanded) joined and measured as a monospace
 * block. Shared by the Claude and ACP read height hooks so the "body = parsed lines
 * joined, sized mono" rule has one home and can't drift between them; the ACP hook
 * then wraps the result in acpCollapsibleBody.
 */
export function monoReadBody(lines: ReadonlyArray<{ text: string }>): Partial<HeightInput> {
  return monoBody(lines.map(l => l.text).join('\n'))
}

/**
 * Join a single `tool_result` content block's inner content into displayed text:
 * an array of content parts -> joined paragraphs, a bare string -> itself, else ''
 * (not a tool_result, or an unrecognized inner shape). The shared per-block unit
 * behind `extractText`'s tool_result branch (chatHeightInput) and the Claude hook's
 * `resultContentText` (heightMetrics), which both hand-walked the same block shape;
 * centralizing it keeps the two from drifting when a new inner content shape lands.
 */
export function toolResultBlockText(block: unknown): string {
  if (!isObject(block) || block.type !== 'tool_result')
    return ''
  const inner = block.content
  if (Array.isArray(inner))
    return joinContentParagraphs(asContentArray(inner) ?? [], { text: 'text' })
  if (typeof inner === 'string')
    return inner
  return ''
}

/**
 * The displayed content blocks of a parsed message: the parent object's content
 * if present, else the top-level object's. The single home for the
 * `parentObject ?? topLevel` precedence that `extractText`, `countImages`
 * (chatHeightInput), and the Claude hook's `resultContentText` (heightMetrics) all
 * read. extractText and countImages MUST read the SAME source -- a tool_result that
 * parsed with `parentObject` undefined but `topLevel` carrying the blocks would
 * otherwise have its text sized while its images are dropped -- and resultContentText
 * must match extractText's tool_result branch. Centralizing the read keeps the three
 * aligned instead of relying on a comment to keep three hand-spelled reads in lockstep.
 */
export function messageContentBlocks(parsed: ParsedMessageContent): ContentBlock[] | null {
  return getMessageContent(parsed.parentObject ?? parsed.topLevel ?? undefined)
}

/**
 * The joined text of the FIRST `tool_result` block whose inner is a recognized
 * content shape (an array of parts or a bare string), or '' when none. The
 * single-block counterpart to `extractText`'s all-blocks join, shared so the Claude
 * hook's structured-extractor raw-text fallback (`resultContentText`) can't drift
 * from how the orchestrator walks tool_result blocks.
 */
export function firstToolResultBlockText(blocks: readonly ContentBlock[]): string {
  for (const b of blocks) {
    if (isObject(b) && b.type === 'tool_result' && (Array.isArray(b.content) || typeof b.content === 'string'))
      return toolResultBlockText(b)
  }
  return ''
}

/**
 * True when the parsed message carries a `tool_result` content block flagged
 * `is_error`. The block shape is the Anthropic/Claude content-array format, so
 * this returns false for providers that don't use it -- shared here because the
 * orchestrator reads it for the generic `isError` field AND Claude's
 * `heightMetrics` reads it to gate diff rendering (a failed edit renders error
 * text, not a diff).
 */
export function isToolResultError(parsed: ParsedMessageContent): boolean {
  const blocks = getMessageContent(parsed.parentObject ?? undefined)
  if (!blocks)
    return false
  for (const b of blocks) {
    if (isObject(b) && b.type === 'tool_result' && b.is_error === true)
      return true
  }
  return false
}
