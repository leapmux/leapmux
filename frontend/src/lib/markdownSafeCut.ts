import type { Definition, FootnoteDefinition, LinkReference, Nodes, Root } from 'mdast'
import { createMarkdownParser } from './markdownParse'

/** Trailing ellipsis appended to truncated previews (U+2026). */
export const PREVIEW_ELLIPSIS = '…'

const parser = createMarkdownParser()

type CutKind = 'inline' | 'block'

interface Candidate {
  offset: number
  kind: CutKind
}

interface PathEntry {
  type: string
  start: number
  end: number
  node: Nodes
}

interface DelimiterMarkers {
  closer: string
  contentStart: number
  contentEnd: number
}

/** A link/image/footnote reference in source order, with its label span. */
interface RefEntry {
  kind: 'linkReference' | 'imageReference' | 'footnoteReference'
  identifier: string
  start: number
  end: number
  /** Label-content span (linkReference only): the source between the brackets. */
  contentStart: number | null
  contentEnd: number | null
}

/**
 * Cut `s` at code-unit offset `at`, never splitting a surrogate pair: a cut
 * landing between a high and low surrogate extends by one unit to keep the
 * astral character whole, so the result is always well-formed UTF-16.
 */
function cutAtCodeUnit(s: string, at: number): string {
  const i = Math.min(Math.max(at, 0), s.length)
  if (i === 0 || i >= s.length)
    return s.slice(0, i)
  const prev = s.charCodeAt(i - 1)
  return prev >= 0xD800 && prev <= 0xDBFF ? s.slice(0, i + 1) : s.slice(0, i)
}

/** The node's children, or `[]` for leaf nodes -- the one home for the narrowing cast. */
function childrenOf(node: Nodes): Nodes[] {
  return 'children' in node && Array.isArray(node.children) ? node.children as Nodes[] : []
}

const ATOMIC_INLINE = new Set([
  'link',
  'image',
  'linkReference',
  'imageReference',
  'footnoteReference',
  'html',
])

const DELIMITER = new Set(['strong', 'emphasis', 'delete', 'inlineCode'])

// `[ \t>]` (not `[>\s]`): `\s` would also match `\n`, letting the prefix class
// walk across lines if an exec slice ever spanned one -- same reasoning as
// FENCE_LANG_RE in markdownProcessor.ts.
const FENCE_OPENER_RE = /^([ \t>]*)(`{3,}|~{3,})/
const FENCE_CLOSER_LINE_RE = /^[ \t>]*(`{3,}|~{3,})[ \t]*$/

// `&name;` / `&#123;` / `&#x1F600;`. The longest named HTML entity is 33 chars
// total (`&CounterClockwiseContourIntegral;`), so a 32-char name cap covers all.
const ENTITY_RE = /^&(?:[a-z][a-z0-9]{1,31}|#\d{1,7}|#x[0-9a-f]{1,6});/i

/**
 * Treat an HTML entity as one character: when `offset` falls strictly inside an
 * `&…;` run, return the offset just past the `;` so the entity is kept whole
 * instead of being split into a literal fragment (`&am` + ellipsis). A false
 * positive (`&notreal;` is not a real entity) merely keeps a literal run whole,
 * which renders identically to the full view -- harmless.
 */
function snapPastEntity(text: string, offset: number): number {
  const amp = text.lastIndexOf('&', offset - 1)
  if (amp === -1 || amp < offset - 34)
    return offset
  const match = ENTITY_RE.exec(text.slice(amp, amp + 35))
  if (!match)
    return offset
  const entityEnd = amp + match[0].length
  return offset < entityEnd ? entityEnd : offset
}

/**
 * Cut `text` at/before `limitOffset` so the prefix re-parses cleanly as markdown,
 * then append an ellipsis. `limitOffset` is a code-unit offset (typically at a
 * grapheme boundary from the caller). An HTML entity straddling the limit is
 * kept whole -- the cut snaps just past its `;`.
 *
 * Output may exceed the limit by a small synthesized suffix (closers / fence /
 * paragraph break before `…`) or a snapped-past entity -- still length-bounded
 * for tooltip rendering.
 *
 * References whose definition falls PAST the cut are rewritten to what the
 * definition-less prefix can actually render: a link reference keeps its label
 * text, image and footnote references are dropped (their fallback rendering is
 * pure bracket noise). A reference with no definition anywhere in the text is
 * left literal -- it renders that way in the full view too. When that rewrite
 * empties the whole prefix (a leading reference-style image gallery), the cut
 * scans FORWARD past the references and their definitions to the first content
 * that renders, rather than dumping the raw markup it just suppressed.
 *
 * Accepted imperfections (cosmetic, none worse than a hard mid-token cut):
 * - Setext headings demoted when the underline is dropped
 */
export function markdownSafeCut(text: string, limitOffset: number): string {
  return new MarkdownCutter(text, limitOffset).cut()
}

/**
 * Single-use cutter: holds the input, the (entity-snapped) limit, and the
 * accumulators the tree walks fill, so the strategy methods don't have to
 * thread `text`/`limitOffset`/out-params through every signature.
 */
class MarkdownCutter {
  private readonly text: string
  private readonly limitOffset: number
  private readonly candidates: Candidate[] = []
  private readonly path: PathEntry[] = []
  /** References in source order (a parent precedes its nested references). */
  private readonly refs: RefEntry[] = []
  /** identifier -> end offset of its earliest definition / footnote definition. */
  private readonly defEnds = new Map<string, number>()
  /** Definition spans in source order, for the forward-scan fallback to elide. */
  private readonly defSpans: { start: number, end: number }[] = []

  constructor(text: string, limitOffset: number) {
    this.text = text
    // NaN would compare false against everything below -- skipping the ≤0
    // guard, voiding FLOOR, and slicing to an empty string, so a broken caller
    // would get a bare ellipsis. Degrade it to the defensive hard-cut path.
    const limit = Number.isNaN(limitOffset) ? 0 : limitOffset
    this.limitOffset = limit > 0 ? snapPastEntity(text, limit) : limit
  }

  cut(): string {
    if (this.limitOffset <= 0)
      return this.hardCut()

    try {
      const tree = parser.parse(this.text) as Root
      this.collectRefs(tree)
      this.collect(tree)
    }
    catch {
      // Parse failures AND walk failures degrade to the bounded hard cut. The
      // recursive walks can genuinely throw: ~4000 nested blockquote markers
      // parse fine (micromark iterates) but overflow the call stack when
      // walked, and the cutter must never leak an exception -- its contract is
      // "always return a bounded preview string".
      return this.hardCut()
    }

    // Reject a clean boundary that throws away more than half the budget: below
    // this floor we prefer a fence/delimiter synth (which keeps ~limitOffset
    // chars) or, if none applies, a bounded hard cut, over a very short clean
    // preview.
    const FLOOR = Math.max(1, Math.floor(this.limitOffset / 2))
    const best = this.pickBest()

    if (best && best.offset >= FLOOR) {
      const prefix = this.prefixUpTo(best.offset).trimEnd()
      if (prefix.length === 0)
        return this.hardCut()
      if (best.kind === 'inline')
        return prefix + PREVIEW_ELLIPSIS
      // Block ellipsis on its own paragraph: appending `…` to a closing fence
      // line (or setext underline / thematic break / table row) would corrupt
      // the construct. A blank line keeps the ellipsis as a following
      // paragraph -- carrying the `> ` markers of every blockquote still open
      // at the cut (on the straddle path and opened before the boundary), so
      // the ellipsis cannot escape the container it was cut from; the fence
      // fallback preserves its prefix the same way. List containers need no
      // marker: an ellipsis paragraph after a list item correctly ends the
      // list.
      const quoteDepth = this.path.filter(e => e.type === 'blockquote' && e.start < best.offset).length
      const quotePrefix = '> '.repeat(quoteDepth)
      return `${prefix}\n${quotePrefix.trimEnd()}\n${quotePrefix}${PREVIEW_ELLIPSIS}`
    }

    const straddlingCode = this.path.find(e => e.type === 'code')
    if (straddlingCode) {
      const fenced = this.finalize(this.tryFenceFallback(straddlingCode))
      if (fenced != null)
        return fenced
    }

    const delimiterCut = this.finalize(this.tryDelimiterFallback())
    if (delimiterCut != null)
      return delimiterCut

    return this.hardCut()
  }

  /**
   * Normalize a fallback strategy's result: `null` means "not applicable" (the
   * caller tries the next strategy); a degenerate result (empty or a bare
   * ellipsis) collapses to a hard cut so no synth path can ever emit a
   * content-free preview.
   */
  private finalize(result: string | null): string | null {
    if (result == null)
      return null
    if (result.length === 0 || result === PREVIEW_ELLIPSIS)
      return this.hardCut()
    return result
  }

  private hardCut(): string {
    const prefix = this.prefixUpTo(Math.max(0, this.limitOffset)).trimEnd()
    if (prefix.length > 0)
      return prefix + PREVIEW_ELLIPSIS

    // The prefix is empty: either the limit is ≤ 0, or the dangling-reference
    // rewrite dropped everything before it (a leading reference-style image
    // gallery whose definitions sit at the end). Raw-slicing here would dump
    // the literal `![…][…]` markup the rewrite exists to suppress, so first
    // look past the noise for content that can actually render.
    if (this.limitOffset > 0) {
      const forward = this.renderableContent().trim()
      if (forward.length > 0)
        return cutAtCodeUnit(forward, this.limitOffset).trimEnd() + PREVIEW_ELLIPSIS
    }
    // Nothing renderable anywhere (an all-references message): the bounded raw
    // slice is the accepted last resort, since a bare ellipsis is forbidden.
    return cutAtCodeUnit(this.text, Math.max(1, this.limitOffset)) + PREVIEW_ELLIPSIS
  }

  /**
   * The whole text as the definition-less preview would render it: dangling
   * references rewritten away and every definition / footnote-definition span
   * elided (they render as nothing, so leaving them in would surface raw
   * `[ref]: url` lines). Used only by the empty-prefix fallback, so the cost of
   * scanning past the limit is paid on that path alone.
   */
  private renderableContent(): string {
    let out = ''
    let cursor = 0
    for (const span of this.defSpans) {
      // Definitions nested inside an earlier elided span are already covered.
      if (span.start < cursor)
        continue
      out += this.rewriteRange(cursor, span.start, this.limitOffset)
      cursor = span.end
    }
    return out + this.rewriteRange(cursor, this.text.length, this.limitOffset)
  }

  /** The source up to `cutOffset` with dangling references rewritten. */
  private prefixUpTo(cutOffset: number): string {
    return this.rewriteRange(0, cutOffset, cutOffset)
  }

  /**
   * A reference dangles when its definition exists in the full text but ends
   * past the cut -- the truncation dropped it, so the kept reference would
   * render as literal `[text][ref]` brackets. A reference with NO definition
   * anywhere renders literally in the full view too and is left alone.
   */
  private isDanglingAt(ref: RefEntry, cutOffset: number): boolean {
    const defEnd = this.defEnds.get(ref.identifier)
    return defEnd != null && defEnd > cutOffset
  }

  /**
   * Slice `[start, end)` of the source with every dangling reference rewritten:
   * a link reference keeps its label text (outer brackets and `[ref]` dropped),
   * image and footnote references vanish. A reference straddling `end` is left
   * to the caller's raw slicing (bounded garble, like any atomic construct).
   * Recurses into a rewritten link label so a nested dangling reference inside
   * it is handled too; a resolved reference's interior is covered by the main
   * scan, since skipping it does not advance the cursor past its span.
   */
  private rewriteRange(start: number, end: number, cutOffset: number): string {
    let out = ''
    let cursor = start
    for (const ref of this.refs) {
      if (ref.start < cursor || ref.end > end)
        continue
      if (!this.isDanglingAt(ref, cutOffset))
        continue
      out += this.text.slice(cursor, ref.start)
      if (ref.kind === 'linkReference' && ref.contentStart != null && ref.contentEnd != null)
        out += this.rewriteRange(ref.contentStart, ref.contentEnd, cutOffset)
      cursor = ref.end
    }
    return out + this.text.slice(cursor, end)
  }

  /**
   * Gather every reference and definition in the FULL tree. Deliberately not
   * folded into `collect()`: that walk prunes past the limit, but definitions
   * conventionally sit at the document end -- past the cut -- and resolving
   * "does this reference's definition survive the cut?" needs them all.
   */
  private collectRefs(node: Nodes): void {
    const start = node.position?.start?.offset
    const end = node.position?.end?.offset
    if (start == null || end == null)
      return

    const type = node.type
    if (type === 'definition' || type === 'footnoteDefinition') {
      const { identifier } = node as Definition | FootnoteDefinition
      const known = this.defEnds.get(identifier)
      // The first definition wins in CommonMark; keep the earliest end.
      if (known == null || end < known)
        this.defEnds.set(identifier, end)
      this.defSpans.push({ start, end })
    }
    else if (type === 'linkReference' || type === 'imageReference' || type === 'footnoteReference') {
      let contentStart: number | null = null
      let contentEnd: number | null = null
      if (type === 'linkReference') {
        const children = (node as LinkReference).children
        if (children.length > 0) {
          contentStart = children[0]!.position?.start?.offset ?? null
          contentEnd = children[children.length - 1]!.position?.end?.offset ?? null
        }
      }
      this.refs.push({
        kind: type,
        identifier: (node as { identifier: string }).identifier,
        start,
        end,
        contentStart,
        contentEnd,
      })
    }

    for (const child of childrenOf(node))
      this.collectRefs(child)
  }

  private pickBest(): Candidate | null {
    let best: Candidate | null = null
    for (const c of this.candidates) {
      if (c.offset > this.limitOffset)
        continue
      if (
        !best
        || c.offset > best.offset
        // At equal offset, inline beats block -- critical for the scan-cap case
        // where text/paragraph/root all end at text.length and the expected
        // output is `hello…`, not `hello\n\n…`.
        || (c.offset === best.offset && c.kind === 'inline' && best.kind === 'block')
      ) {
        best = c
      }
    }
    return best
  }

  private collect(node: Nodes, inInlineContext = false): void {
    const start = node.position?.start?.offset
    const end = node.position?.end?.offset
    // Skip nodes with undefined position.end.offset (types force the narrowing).
    if (start == null || end == null)
      return

    // A node starting at/after the limit yields no usable candidate (`pickBest`
    // discards offset > limitOffset) and never lands on the straddle path
    // (`onPath` needs start < limitOffset), so walking its subtree is wasted
    // work -- children only start later. Prune once the walk crosses the limit.
    if (start >= this.limitOffset)
      return

    const type = node.type
    const onPath = start < this.limitOffset && this.limitOffset < end
    if (onPath)
      this.path.push({ type, start, end, node })

    if (type === 'break')
      return

    if (type === 'html') {
      // mdast has ONE `html` type for both block and inline HTML. A block HTML
      // section continues until a blank line, so an inline `…` appended right
      // after it would be absorbed into the block -- which remark-rehype then
      // drops entirely (no allowDangerousHtml), losing the ellipsis. Only HTML
      // inside a paragraph/heading is inline.
      this.candidates.push({ offset: end, kind: inInlineContext ? 'inline' : 'block' })
      return
    }

    if (ATOMIC_INLINE.has(type)) {
      this.candidates.push({ offset: end, kind: 'inline' })
      return
    }

    if (DELIMITER.has(type)) {
      this.candidates.push({ offset: end, kind: 'inline' })
      // No descent for candidates. Still walk children when straddling so nested
      // delimiters (and any atomic inline that blocks delimiter fallback) land on
      // the path.
      if (onPath) {
        for (const child of childrenOf(node))
          this.collectDelimiterPath(child)
      }
      return
    }

    if (type === 'text') {
      this.candidates.push({ offset: end, kind: 'inline' })
      // Mid-word cut in literal text is safe -- same as the pre-markdown status quo.
      if (start < this.limitOffset && this.limitOffset < end)
        this.candidates.push({ offset: this.limitOffset, kind: 'inline' })
      return
    }

    if (type === 'table') {
      this.candidates.push({ offset: end, kind: 'block' })
      // Body rows only (children[1..]). Never the header row: GFM's `|---|`
      // delimiter row is not an AST node, so a header-row-end cut reparses with
      // no table at all. Never descend into cells.
      const rows = childrenOf(node)
      for (let i = 1; i < rows.length; i++) {
        const rowEnd = rows[i]?.position?.end?.offset
        if (rowEnd != null)
          this.candidates.push({ offset: rowEnd, kind: 'block' })
      }
      return
    }

    if (type === 'code' || type === 'definition' || type === 'thematicBreak') {
      this.candidates.push({ offset: end, kind: 'block' })
      return
    }

    // root, paragraph, heading, blockquote, list, listItem, footnoteDefinition, …
    this.candidates.push({ offset: end, kind: 'block' })
    // Paragraph/heading children are inline content; every other container
    // (root, blockquote, list, listItem, footnoteDefinition) holds blocks.
    const childrenInline = type === 'paragraph' || type === 'heading'
    for (const child of childrenOf(node))
      this.collect(child, childrenInline)
  }

  /**
   * Path-only walk under a delimiter: records nested delimiters and atomic
   * inlines that contain limitOffset, without adding candidates (the parent
   * delimiter owns that).
   */
  private collectDelimiterPath(node: Nodes): void {
    const start = node.position?.start?.offset
    const end = node.position?.end?.offset
    if (start == null || end == null)
      return
    if (!(start < this.limitOffset && this.limitOffset < end))
      return

    const type = node.type
    if (ATOMIC_INLINE.has(type) || DELIMITER.has(type)) {
      this.path.push({ type, start, end, node })
      if (DELIMITER.has(type)) {
        for (const child of childrenOf(node))
          this.collectDelimiterPath(child)
      }
      return
    }

    for (const child of childrenOf(node))
      this.collectDelimiterPath(child)
  }

  private tryFenceFallback(entry: PathEntry): string | null {
    const { start: nodeStart, end: nodeEnd } = entry
    const lineStart = this.text.lastIndexOf('\n', nodeStart - 1) + 1
    const openerMatch = FENCE_OPENER_RE.exec(this.text.slice(lineStart))
    if (!openerMatch)
      return null // indented code (or non-fence) -- no synthesizable closer

    const prefix = openerMatch[1]
    const fenceRun = openerMatch[2]
    const fenceChar = fenceRun[0]!
    const fenceLen = fenceRun.length

    const openerLineNl = this.text.indexOf('\n', nodeStart)
    // Opening fence line with no following content line: nothing to close usefully.
    if (openerLineNl === -1 || openerLineNl >= nodeEnd)
      return null

    // Limit inside the opening fence line → cut at the code node's start instead
    // (fall through to an earlier candidate / hard cut).
    if (this.limitOffset <= openerLineNl)
      return null

    const contentStart = openerLineNl + 1

    // Detect a closing fence as the last line of the node.
    let contentEnd = nodeEnd
    let closingFenceLineStart = -1
    const lastNl = this.text.lastIndexOf('\n', nodeEnd - 1)
    if (lastNl >= contentStart - 1) {
      const closingLineStart = lastNl + 1
      if (closingLineStart >= contentStart && closingLineStart < nodeEnd) {
        const closingLine = this.text.slice(closingLineStart, nodeEnd)
        const closerMatch = FENCE_CLOSER_LINE_RE.exec(closingLine)
        if (
          closerMatch
          && closerMatch[1]![0] === fenceChar
          && closerMatch[1]!.length >= fenceLen
        ) {
          closingFenceLineStart = closingLineStart
          // Exclusive content end: exclude the newline before the closer line.
          contentEnd = closingLineStart - 1
        }
      }
    }

    let cutAt = Math.min(this.limitOffset, contentEnd)
    // Limit inside the closing fence line → clamp cut to content end (no doubled fence).
    if (closingFenceLineStart >= 0 && this.limitOffset >= closingFenceLineStart)
      cutAt = contentEnd
    if (cutAt < contentStart)
      return null

    // Skip trimEnd inside fence content -- trailing spaces in code are significant,
    // and trimming could eat the newline structure before the synthesized closer.
    const sliced = this.prefixUpTo(cutAt)
    // When the cut lands at a content-line boundary (`sliced` ends in a newline)
    // the `…` opens a fresh code line. Inside a container (e.g. a blockquote)
    // that line needs the same prefix the fence opener carried, or the `…`
    // escapes the container and the synthesized closing fence re-parses as a
    // second, empty fence.
    const ellipsisLinePrefix = sliced.endsWith('\n') ? prefix : ''
    const closer = `\n${prefix}${fenceRun}`
    return sliced + ellipsisLinePrefix + PREVIEW_ELLIPSIS + closer
  }

  private tryDelimiterFallback(): string | null {
    // Delimiter chain with no link/image/html below it -- those have no
    // synthesizable closer from source markers alone.
    const delimiters: PathEntry[] = []
    for (const entry of this.path) {
      if (ATOMIC_INLINE.has(entry.type))
        return null
      if (DELIMITER.has(entry.type))
        delimiters.push(entry)
    }
    if (delimiters.length === 0)
      return null

    const innermost = delimiters[delimiters.length - 1]!
    const markers = delimiters.map(d => this.delimiterMarkers(d))
    if (markers.some(m => m == null))
      return null
    const resolved = markers as DelimiterMarkers[]

    const inner = resolved[resolved.length - 1]!
    // Limit inside the innermost construct's opening run (path membership
    // guarantees innermost.start < limitOffset, so the run containing the limit
    // is always the innermost one): drop that construct entirely -- cut at its
    // start and close the still-open outer constructs, innermost-first.
    if (this.limitOffset < inner.contentStart) {
      const prefix = this.prefixUpTo(innermost.start).trimEnd()
      if (prefix.length === 0)
        return null // empty → hard cut at caller
      const outerClosers = resolved.slice(0, -1).map(m => m.closer).reverse().join('')
      return prefix + PREVIEW_ELLIPSIS + outerClosers
    }

    // Clamp to innermost content range (opener/closer runs excluded). Limit in
    // the closing region clamps to content end.
    const cutAt = Math.min(Math.max(this.limitOffset, inner.contentStart), inner.contentEnd)
    // `…` between content and closers keeps closers validly right-flanking and
    // prevents backtick-run merging (e.g. cut content ending in `` ` `` would
    // otherwise merge with a closing `` ` `` run).
    const closers = resolved.map(m => m.closer).reverse().join('')
    return this.prefixUpTo(cutAt) + PREVIEW_ELLIPSIS + closers
  }

  private delimiterMarkers(entry: PathEntry): DelimiterMarkers | null {
    const { start, end, node, type } = entry

    if (type === 'inlineCode') {
      const openMatch = /^(`+)/.exec(this.text.slice(start, end))
      if (!openMatch)
        return null
      const ticks = openMatch[1]!
      return {
        closer: ticks,
        contentStart: start + ticks.length,
        contentEnd: end - ticks.length,
      }
    }

    // strong / emphasis / delete: the closer read verbatim from source at the
    // construct's end (so `**` vs `__`, `*` vs `_`, `~`/`~~` stay accurate).
    const children = childrenOf(node)
    if (children.length > 0) {
      const contentStart = children[0]!.position?.start?.offset
      const contentEnd = children[children.length - 1]!.position?.end?.offset
      if (contentStart == null || contentEnd == null)
        return null
      return {
        closer: this.text.slice(contentEnd, end),
        contentStart,
        contentEnd,
      }
    }

    // Empty delimiter construct: unreachable with the pinned parser (CommonMark
    // requires non-empty emphasis/strong/delete content -- `****` parses as a
    // thematic break, not empty strong), but the mdast types allow it. Bail to
    // the bounded hard cut rather than guess at marker runs.
    return null
  }
}
