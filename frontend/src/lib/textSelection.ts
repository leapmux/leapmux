/**
 * What the browser gives a mouse for free, expressed so a finger can have it
 * too: the word around a point, the paragraph around a point, and the test for
 * whether a point sits on the live selection.
 *
 * A double-click and a triple-click are the mouse's own defaults, and no engine
 * offers a touch equivalent — a finger gets long-press-and-drag handles or
 * nothing. ~/lib/tapSelect.ts supplies the gesture; this module supplies the two
 * ranges it needs, and states what "word" and "paragraph" mean here.
 *
 * ## The inline run
 *
 * Both ranges are computed over an INLINE RUN rather than over the caret's own
 * text node, because a text node is an accident of the markup. `**tap**select`
 * renders as one word in three nodes, and a word taken from one node alone would
 * stop at the `<strong>` boundary. The run is the text of the caret's nearest
 * block ancestor, in document order, with the nested blocks left out: exactly
 * the characters a reader sees as one paragraph.
 *
 * A forced line break — a `<br>`, or a newline inside preserved white space —
 * enters the run as a `\n` and is recorded as a BREAK. That is what makes a
 * triple tap inside a code block select one line: a `<pre>` is a single block,
 * and its newlines are the only paragraph boundaries it has. It is also why the
 * breaks are recorded rather than found by scanning the text for `\n`. A newline
 * that HTML collapses to a space (a source line wrap inside a `<p>`) is a
 * `\n` in the DOM and no boundary at all.
 */

/** A position inside a text node. Named apart from the DOM's own `CaretPosition`. */
export interface TextCaret {
  node: Text
  offset: number
}

/** A half-open character span `[start, end)` of an inline run. */
export interface TextBounds {
  start: number
  end: number
}

/** One text node's contribution to an inline run, or the newline a `<br>` stands for. */
interface RunPiece {
  /** The backing text node. `null` for the synthetic newline of a `<br>`. */
  node: Text | null
  /** Where this piece starts in the run's text. */
  start: number
  /** How many characters it contributes. */
  length: number
  /** Whether white space here is preserved, so the trim must leave it alone. */
  preserves: boolean
}

interface InlineRun {
  text: string
  pieces: RunPiece[]
  /** Indices in `text` that hold a forced line break. */
  breaks: number[]
}

/**
 * The white-space values that keep a newline as a newline.
 *
 * Read from the computed style rather than from the tag, because the app styles
 * its own code blocks and a `<div>` with `white-space: pre` must break the same
 * way a `<pre>` does.
 */
const NEWLINE_PRESERVING_WHITE_SPACE = new Set(['pre', 'pre-wrap', 'pre-line', 'break-spaces'])

/**
 * Word segmentation, built once.
 *
 * `Intl.Segmenter` is the only definition of "word" this module has, and it is
 * the right one: it knows that `foo_bar` is one word and `foo-bar` is two, and
 * it finds word boundaries in scripts that write no spaces at all, which a
 * character-class regex cannot do for Korean, Japanese or Chinese. Writing a
 * regex fallback beside it would be a SECOND definition of a word that disagrees
 * with the first on every one of those inputs.
 *
 * So there is no fallback. Where the constructor is absent the double tap finds
 * no word and does nothing, and the triple tap is unaffected because a paragraph
 * needs no segmentation.
 */
let wordSegmenter: Intl.Segmenter | null | undefined

function segmenter(): Intl.Segmenter | null {
  if (wordSegmenter === undefined)
    wordSegmenter = typeof Intl.Segmenter === 'function' ? new Intl.Segmenter(undefined, { granularity: 'word' }) : null
  return wordSegmenter
}

/**
 * Whether `el` lays its children out in the line, so it is not a paragraph edge.
 *
 * `inline-block`, `inline-flex` and `inline-grid` all count as inline here. They
 * sit IN a line of prose — a badge or a chip beside the words — so a paragraph
 * that contains one does not end at it, which is also what a triple-click does.
 *
 * An empty value reads as inline, so the walk continues upwards. A style engine
 * reports one for a property it does not implement and for an element outside
 * the document; treating that as a block edge would cut every run at the first
 * element above the caret.
 */
function isInlineDisplay(el: Element): boolean {
  const display = getComputedStyle(el).display
  return display === '' || display === 'contents' || display.startsWith('inline') || display.startsWith('ruby')
}

function preservesNewlines(el: Element | null): boolean {
  return el ? NEWLINE_PRESERVING_WHITE_SPACE.has(getComputedStyle(el).whiteSpace) : false
}

/**
 * The nearest ancestor of `node` that ends a paragraph, never above `root`.
 *
 * `root` is the fallback rather than an exclusion: a caret in a bare text node
 * directly under the region still belongs to a paragraph, and that paragraph is
 * the region.
 */
function blockAncestor(node: Node, root: Element): Element {
  for (let el = node.parentElement; el && el !== root; el = el.parentElement) {
    if (!isInlineDisplay(el))
      return el
  }
  return root
}

/**
 * Read `block` into one run of text.
 *
 * `FILTER_REJECT` on a nested block is what keeps a list item out of the
 * paragraph of the item above it: it drops the element AND its subtree, where
 * `FILTER_SKIP` would drop the element and keep walking into its children.
 * `display: none` takes the same path, so a hidden subtree contributes nothing.
 */
function buildInlineRun(block: Element): InlineRun {
  const pieces: RunPiece[] = []
  const breaks: number[] = []
  let text = ''

  const walker = document.createTreeWalker(block, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT, {
    acceptNode(node) {
      if (node.nodeType === Node.TEXT_NODE)
        return NodeFilter.FILTER_ACCEPT
      const el = node as Element
      if (el.tagName === 'BR')
        return NodeFilter.FILTER_ACCEPT
      return isInlineDisplay(el) ? NodeFilter.FILTER_SKIP : NodeFilter.FILTER_REJECT
    },
  })

  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    if (node.nodeType !== Node.TEXT_NODE) {
      // A `<br>`. It has no characters of its own, so the run borrows one.
      breaks.push(text.length)
      pieces.push({ node: null, start: text.length, length: 1, preserves: true })
      text += '\n'
      continue
    }
    const textNode = node as Text
    const preserves = preservesNewlines(textNode.parentElement)
    if (preserves) {
      for (let i = textNode.data.indexOf('\n'); i >= 0; i = textNode.data.indexOf('\n', i + 1))
        breaks.push(text.length + i)
    }
    pieces.push({ node: textNode, start: text.length, length: textNode.data.length, preserves })
    text += textNode.data
  }

  return { text, pieces, breaks }
}

/** The run around `caret`, and where the caret sits in it. */
function resolveRun(caret: TextCaret, root: Element): { run: InlineRun, index: number } | null {
  if (!root.contains(caret.node))
    return null
  const run = buildInlineRun(blockAncestor(caret.node, root))
  const piece = run.pieces.find(p => p.node === caret.node)
  if (!piece)
    return null
  // Not clamped here. An engine can report an offset past the end of the node
  // it names, and {@link wordBounds} and `paragraphBounds` each clamp against
  // the run's own length -- which is the length that matters, and the only one
  // either of them can be wrong about.
  return { run, index: piece.start + caret.offset }
}

/**
 * The word around `index` in `text`, or `null` when there is no word to take.
 *
 * A tap lands on a character boundary, so the index just past the last letter of
 * a word is the common case for a finger aimed at that word's right half — and
 * that index belongs to the SPACE after it. The preceding word wins whenever the
 * segment the index falls in is not a word, which is what makes the near miss
 * select what the reader pointed at.
 */
export function wordBounds(text: string, index: number): TextBounds | null {
  const segment = segmenter()
  if (!segment || text === '')
    return null
  const at = Math.max(0, Math.min(index, text.length))
  const segments = [...segment.segment(text)]
  if (segments.length === 0)
    return null

  let hit = segments.find(s => at >= s.index && at < s.index + s.segment.length)
  if (!hit?.isWordLike) {
    const before = segments.find(s => s.index + s.segment.length === at)
    if (before?.isWordLike)
      hit = before
  }
  hit ??= segments[segments.length - 1]
  return { start: hit.index, end: hit.index + hit.segment.length }
}

/**
 * The paragraph around `index`, trimmed of the white space that does not show.
 *
 * A caret exactly ON a break takes the paragraph BEFORE it. That index is the
 * end of the line the finger tapped, not the start of the next one.
 *
 * The trim skips preserved white space, so the indentation of a code line stays
 * in the selection. Trimming it would copy a line that no longer lines up.
 */
function paragraphBounds(run: InlineRun, index: number): TextBounds | null {
  // `index` is used only to compare against the break positions, so an
  // out-of-range one needs no clamp: past the end it lands after every break
  // and takes the last paragraph, and below zero it lands before every break
  // and takes the first. Nothing here indexes `run.text` with it.
  const at = index
  let start = 0
  let end = run.text.length
  for (const position of run.breaks) {
    if (position >= at) {
      end = position
      break
    }
    start = position + 1
  }

  const preservedAt = (i: number) => run.pieces.some(p => p.preserves && i >= p.start && i < p.start + p.length)
  while (start < end && /\s/.test(run.text[start]) && !preservedAt(start))
    start++
  while (end > start && /\s/.test(run.text[end - 1]) && !preservedAt(end - 1))
    end--
  return start < end ? { start, end } : null
}

/** The DOM position for a run index, entering a piece from its left edge. */
function startPosition(run: InlineRun, index: number): TextCaret | null {
  for (const piece of run.pieces) {
    if (!piece.node)
      continue
    if (index <= piece.start)
      return { node: piece.node, offset: 0 }
    if (index < piece.start + piece.length)
      return { node: piece.node, offset: index - piece.start }
  }
  return null
}

/** The DOM position for a run index, closing the last piece that starts before it. */
function endPosition(run: InlineRun, index: number): TextCaret | null {
  let last: TextCaret | null = null
  for (const piece of run.pieces) {
    if (!piece.node || piece.start >= index)
      continue
    last = { node: piece.node, offset: Math.min(index - piece.start, piece.length) }
  }
  return last
}

function rangeFor(run: InlineRun, bounds: TextBounds): Range | null {
  const from = startPosition(run, bounds.start)
  const to = endPosition(run, bounds.end)
  if (!from || !to)
    return null
  const range = document.createRange()
  range.setStart(from.node, from.offset)
  range.setEnd(to.node, to.offset)
  return range.collapsed ? null : range
}

/** The word around `caret`, as a range. `null` when the caret sits on no word. */
export function wordRangeAt(caret: TextCaret, root: Element): Range | null {
  const resolved = resolveRun(caret, root)
  if (!resolved)
    return null
  const bounds = wordBounds(resolved.run.text, resolved.index)
  return bounds ? rangeFor(resolved.run, bounds) : null
}

/**
 * The paragraph around `caret`, as a range — one line inside preserved white
 * space, because a newline there is a paragraph boundary.
 */
export function paragraphRangeAt(caret: TextCaret, root: Element): Range | null {
  const resolved = resolveRun(caret, root)
  if (!resolved)
    return null
  const bounds = paragraphBounds(resolved.run, resolved.index)
  return bounds ? rangeFor(resolved.run, bounds) : null
}

/**
 * The live selection, when it holds text and sits wholly inside `container`.
 *
 * The Selection itself rather than a boolean, because two of the three callers
 * go on to read its ranges and asking the platform twice invites the two answers
 * to disagree.
 *
 * Blank counts as nothing. A range over white space alone is what a stray drag
 * leaves behind: there is no text to copy, no handle worth adjusting, and no
 * reason for any caller to change what it does.
 *
 * The text comes from the RANGES and never from `Selection.toString()`, which is
 * layout-aware and therefore answers differently depending on a CSS property
 * that this app changes at run time. Measured in Chromium, over the same
 * unchanged range: with `user-select: none` on the wrapper the selection
 * serializes as the empty string while `Range.toString()` still reads the word,
 * and the selection is not collapsed either way. ~/lib/tapSelect.ts puts that
 * suppression back the moment a finger lands away from the highlight, so a
 * predicate built on `Selection.toString()` reported "nothing is selected" to
 * every gesture that asked after it -- which is how a swipe over a live
 * selection opened a drawer.
 */
export function selectionInside(container: Node): Selection | null {
  const selection = window.getSelection()
  if (!selection || selection.isCollapsed || selection.rangeCount === 0)
    return null
  const anchorNode = selection.anchorNode
  const focusNode = selection.focusNode
  if (!anchorNode || !focusNode || !container.contains(anchorNode) || !container.contains(focusNode))
    return null
  for (let i = 0; i < selection.rangeCount; i++) {
    if (selection.getRangeAt(i).toString().trim())
      return selection
  }
  return null
}

/**
 * Whether a point sits on live selected text, within `tolerancePx` of it.
 *
 * The test is the POINT against the selection's rects, not the event target
 * against the selected nodes. `Selection.containsNode` answers the wrong
 * question here: a row that holds the selection is not itself contained by it,
 * so a press over a highlighted word inside that row would read as "outside".
 * Rects also give the correct answer for the reverse case — a press on the row's
 * padding, beside the highlight, reads as outside.
 *
 * `~/components/common/contextMenuGesture.ts` asks it of a right-click with no
 * tolerance, to leave the native Copy menu alone; a mouse points at what it
 * means. `~/lib/tapSelect.ts` asks it of a finger with a fingertip's worth,
 * because the platform draws its selection handles at the edges of the highlight
 * and below its last line — so a finger reaching for one lands outside every
 * rect the selection reports.
 */
export function pointIsInsideSelection(clientX: number, clientY: number, tolerancePx = 0): boolean {
  const selection = window.getSelection()
  if (!selection || selection.isCollapsed || selection.rangeCount === 0)
    return false
  for (let i = 0; i < selection.rangeCount; i++) {
    // jsdom does no layout and does not implement `Range.getClientRects` at all,
    // so this stays optional. There, and anywhere else without geometry, the
    // predicate reports "not on the selection" — the same answer a real browser
    // gives for an empty rect list.
    const rects = selection.getRangeAt(i).getClientRects?.()
    if (!rects)
      continue
    for (let r = 0; r < rects.length; r++) {
      const rect = rects[r]
      if (clientX >= rect.left - tolerancePx && clientX <= rect.right + tolerancePx
        && clientY >= rect.top - tolerancePx && clientY <= rect.bottom + tolerancePx) {
        return true
      }
    }
  }
  return false
}
