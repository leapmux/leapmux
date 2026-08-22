import { afterEach, describe, expect, it, vi } from 'vitest'
import { paragraphRangeAt, pointIsInsideSelection, selectionInside, wordBounds, wordRangeAt } from './textSelection'

/**
 * Every fixture states the two properties the code reads -- `display` and
 * `white-space` -- rather than leaning on the environment's own defaults for
 * them. jsdom does supply both (a `<pre>` reports `white-space: pre` there), but
 * a case that reads "this shape, these boundaries" says what it is testing, and
 * it cannot start passing for a reason the fixture does not show.
 */
function mount(html: string): HTMLElement {
  const style = document.createElement('style')
  style.textContent = `
    .block { display: block; }
    .inline { display: inline; }
    .pre { display: block; white-space: pre; }
    .hidden { display: none; }
  `
  document.head.append(style)
  const root = document.createElement('div')
  root.className = 'block'
  root.innerHTML = html
  document.body.append(root)
  return root
}

/** The text node holding `needle`, so a case names its caret by the text it reads. */
function textNodeWith(root: Element, needle: string): Text {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    if ((node as Text).data.includes(needle))
      return node as Text
  }
  throw new Error(`no text node contains ${JSON.stringify(needle)}`)
}

/** A caret at the first character of `needle` inside the node that holds it. */
function caretAt(root: Element, needle: string, within = 0) {
  const node = textNodeWith(root, needle)
  return { node, offset: node.data.indexOf(needle) + within }
}

afterEach(() => {
  // Restore FIRST: a case that mocks `getSelection` leaves an object with no
  // `removeAllRanges`, and calling it here would throw and skip the rest.
  vi.restoreAllMocks()
  document.body.innerHTML = ''
  document.head.innerHTML = ''
  window.getSelection()?.removeAllRanges()
})

describe('wordBounds', () => {
  it.each([
    ['inside a word', 'hello world', 7, 'world'],
    ['at a word start', 'hello world', 6, 'world'],
    ['at index zero', 'hello world', 0, 'hello'],
    // A finger aimed at the right half of the last letter lands on the boundary
    // AFTER it, which belongs to the space. The word before wins.
    ['on the space after a word', 'hello world', 5, 'hello'],
    ['at the very end of the text', 'hello world', 11, 'world'],
    // Intl.Segmenter's word rules, which is the whole reason this uses it:
    // an underscore joins, a hyphen splits, and Korean needs no spaces at all.
    ['across an underscore', 'foo_bar-baz', 1, 'foo_bar'],
    ['after a hyphen', 'foo_bar-baz', 9, 'baz'],
    ['in a script that writes no spaces', '안녕하세요 세계', 1, '안녕하세요'],
    ['on a number', 'exit code 137 now', 11, '137'],
  ])('takes the word %s', (_case, text, index, expected) => {
    const bounds = wordBounds(text, index)
    expect(bounds).not.toBeNull()
    expect(text.slice(bounds!.start, bounds!.end)).toBe(expected)
  })

  it('clamps an index outside the text', () => {
    expect(wordBounds('hello world', -5)).toEqual({ start: 0, end: 5 })
    expect(wordBounds('hello world', 999)).toEqual({ start: 6, end: 11 })
  })

  it('finds no word in empty text', () => {
    expect(wordBounds('', 0)).toBeNull()
  })

  it('takes the run of spaces when a tap lands on nothing else', () => {
    const bounds = wordBounds('   ', 1)
    expect(bounds).not.toBeNull()
    expect('   '.slice(bounds!.start, bounds!.end).trim()).toBe('')
  })

  // The absent-API path. `Intl.Segmenter` is the only definition of a word this
  // module has, and there is deliberately no regex standing behind it -- so the
  // double tap must find nothing rather than find something different.
  it('finds no word where the platform has no segmenter', async () => {
    vi.resetModules()
    const segmenter = Intl.Segmenter
    Reflect.deleteProperty(Intl, 'Segmenter')
    try {
      const fresh = await import('./textSelection')
      expect(fresh.wordBounds('hello world', 7)).toBeNull()
    }
    finally {
      Object.defineProperty(Intl, 'Segmenter', { value: segmenter, configurable: true, writable: true })
      vi.resetModules()
    }
  })
})

describe('wordRangeAt', () => {
  it('takes a word that markup split across three nodes', () => {
    // `tap**Sel**ect` renders as one word. A word taken from the caret's own text
    // node alone would stop at the <strong>.
    const root = mount('<p class="block">tap<strong class="inline">Sel</strong>ect here</p>')
    const range = wordRangeAt(caretAt(root, 'tap', 1), root)
    expect(range?.toString()).toBe('tapSelect')
  })

  it('stops at the edge of the paragraph the caret is in', () => {
    const root = mount('<p class="block">alpha beta</p><p class="block">gamma</p>')
    expect(wordRangeAt(caretAt(root, 'alpha'), root)?.toString()).toBe('alpha')
  })

  it('reads nothing out of a caret outside the region', () => {
    const root = mount('<p class="block">inside</p>')
    const outside = mount('<p class="block">outside</p>')
    expect(wordRangeAt(caretAt(outside, 'outside'), root)).toBeNull()
  })

  it('finds no word in an empty region', () => {
    const root = mount('<p class="block"></p>')
    const empty = document.createTextNode('')
    root.firstElementChild!.append(empty)
    expect(wordRangeAt({ node: empty, offset: 0 }, root)).toBeNull()
  })
})

// An engine can report an offset past the end of the node it names -- a caret
// at the end of a line whose text re-rendered shorter behind it. Both range
// functions clamp against the run, so neither reads off the end.
describe('a caret offset outside its node', () => {
  it('clamps to the last word', () => {
    const root = mount('<p class="block">alpha beta</p>')
    const node = textNodeWith(root, 'alpha')
    expect(wordRangeAt({ node, offset: 999 }, root)?.toString()).toBe('beta')
  })

  it('clamps to the first word', () => {
    const root = mount('<p class="block">alpha beta</p>')
    const node = textNodeWith(root, 'alpha')
    expect(wordRangeAt({ node, offset: -4 }, root)?.toString()).toBe('alpha')
  })

  it('clamps for the paragraph too', () => {
    const root = mount('<pre class="pre">first\nsecond</pre>')
    const node = textNodeWith(root, 'first')
    expect(paragraphRangeAt({ node, offset: 999 }, root)?.toString()).toBe('second')
    expect(paragraphRangeAt({ node, offset: -9 }, root)?.toString()).toBe('first')
  })
})

describe('paragraphRangeAt', () => {
  it('takes the block the caret is in and no sibling block', () => {
    const root = mount('<p class="block">first para</p><p class="block">second para</p>')
    expect(paragraphRangeAt(caretAt(root, 'first'), root)?.toString()).toBe('first para')
  })

  it('leaves a nested block out of the paragraph above it', () => {
    const root = mount('<ul class="block"><li class="block">outer<ul class="block"><li class="block">inner</li></ul></li></ul>')
    expect(paragraphRangeAt(caretAt(root, 'outer'), root)?.toString()).toBe('outer')
  })

  it('ends the paragraph at a <br>', () => {
    const root = mount('<p class="block">one<br>two</p>')
    expect(paragraphRangeAt(caretAt(root, 'one'), root)?.toString()).toBe('one')
    expect(paragraphRangeAt(caretAt(root, 'two'), root)?.toString()).toBe('two')
  })

  it('takes one line inside preserved white space', () => {
    const root = mount('<pre class="pre">line one\nline two\nline three</pre>')
    expect(paragraphRangeAt(caretAt(root, 'line two', 2), root)?.toString()).toBe('line two')
  })

  it('keeps the indentation of a code line', () => {
    // Trimming it would copy a line that no longer lines up with the block it
    // came from, so the trim skips white space the style preserves.
    const root = mount('<pre class="pre">if (x) {\n    return 1\n}</pre>')
    expect(paragraphRangeAt(caretAt(root, 'return'), root)?.toString()).toBe('    return 1')
  })

  it('takes the line before a caret that sits exactly on the break', () => {
    const root = mount('<pre class="pre">first\nsecond</pre>')
    const node = textNodeWith(root, 'first')
    expect(paragraphRangeAt({ node, offset: node.data.indexOf('\n') }, root)?.toString()).toBe('first')
  })

  it('trims the white space that does not show', () => {
    const root = mount('<p class="block">\n   padded text\n  </p>')
    expect(paragraphRangeAt(caretAt(root, 'padded'), root)?.toString()).toBe('padded text')
  })

  it('joins the inline elements of one paragraph', () => {
    const root = mount('<p class="block">before <em class="inline">middle</em> after</p>')
    expect(paragraphRangeAt(caretAt(root, 'middle'), root)?.toString()).toBe('before middle after')
  })

  // Asserted at the END of the paragraph, where the boundary is what the range
  // carries. `Range.toString()` concatenates every text node between its two
  // points and knows nothing about CSS, so a hidden subtree in the MIDDLE of a
  // range still shows up in it -- there it is `Selection.toString()`, which is
  // layout-aware, that leaves the hidden text out.
  it('does not extend the paragraph into a hidden subtree', () => {
    const root = mount('<p class="block">shown<span class="hidden">hidden</span></p>')
    expect(paragraphRangeAt(caretAt(root, 'shown'), root)?.toString()).toBe('shown')
  })

  it('falls back to the region when no block sits between it and the caret', () => {
    const root = mount('bare text<span class="inline"> and more</span>')
    expect(paragraphRangeAt(caretAt(root, 'bare'), root)?.toString()).toBe('bare text and more')
  })

  it('finds no paragraph in a blank one', () => {
    const root = mount('<p class="block">   </p>')
    expect(paragraphRangeAt(caretAt(root, ' '), root)).toBeNull()
  })
})

describe('selectionInside', () => {
  function selectContents(el: Element) {
    const range = document.createRange()
    range.selectNodeContents(el)
    const selection = window.getSelection()!
    selection.removeAllRanges()
    selection.addRange(range)
  }

  it('reports a selection that sits inside the container', () => {
    const root = mount('<p class="block">selected text</p>')
    selectContents(root.firstElementChild!)
    expect(selectionInside(root)?.toString()).toBe('selected text')
    expect(selectionInside(root.firstElementChild!)?.toString()).toBe('selected text')
  })

  it('reports nothing for a selection in another container', () => {
    const root = mount('<p class="block">inside</p>')
    const other = mount('<p class="block">elsewhere</p>')
    selectContents(other.firstElementChild!)
    expect(selectionInside(root)).toBeNull()
  })

  it('reports nothing with no selection at all', () => {
    const root = mount('<p class="block">selected</p>')
    window.getSelection()?.removeAllRanges()
    expect(selectionInside(root)).toBeNull()
  })

  it('reports nothing for a collapsed selection', () => {
    const root = mount('<p class="block">selected</p>')
    const range = document.createRange()
    range.setStart(textNodeWith(root, 'selected'), 3)
    range.collapse(true)
    const selection = window.getSelection()!
    selection.removeAllRanges()
    selection.addRange(range)
    expect(selectionInside(root)).toBeNull()
  })

  // A range over white space alone is what a stray drag leaves behind. There is
  // no text to copy and no handle worth adjusting, so no caller should react.
  it('reports nothing for a selection that holds only white space', () => {
    const root = mount('<p class="block">   </p>')
    selectContents(root.firstElementChild!)
    expect(selectionInside(root)).toBeNull()
  })

  /**
   * The divergence this predicate is built around, which jsdom cannot produce.
   *
   * Measured in Chromium over one unchanged range: with `user-select: none` on
   * the wrapper the SELECTION serializes as the empty string while the RANGE
   * still reads the word. ~/lib/tapSelect.ts restores that suppression as soon
   * as a finger lands away from the highlight, so a predicate that trusted the
   * selection would tell every gesture asking after it that nothing is
   * selected. The mock reproduces exactly that split.
   */
  it('reports a selection whose text only the range still serializes', () => {
    const root = mount('<p class="block">selected text</p>')
    const textNode = textNodeWith(root, 'selected')
    vi.spyOn(window, 'getSelection').mockReturnValue({
      isCollapsed: false,
      rangeCount: 1,
      anchorNode: textNode,
      focusNode: textNode,
      toString: () => '',
      getRangeAt: () => ({ toString: () => 'selected text' }) as unknown as Range,
    } as unknown as Selection)

    expect(selectionInside(root)).not.toBeNull()
  })
})

describe('pointIsInsideSelection', () => {
  /** jsdom does no layout, so the range's rects are supplied. */
  function selectWithRect(el: Element, rect: { left: number, right: number, top: number, bottom: number }) {
    const range = document.createRange()
    range.selectNodeContents(el)
    const selection = window.getSelection()!
    selection.removeAllRanges()
    selection.addRange(range)
    const live = selection.getRangeAt(0) as Range & { getClientRects: () => DOMRectList }
    live.getClientRects = () => [rect] as unknown as DOMRectList
  }

  it('reports a point on the highlight', () => {
    const root = mount('<p class="block">selected</p>')
    selectWithRect(root.firstElementChild!, { left: 10, right: 90, top: 20, bottom: 40 })
    expect(pointIsInsideSelection(50, 30)).toBe(true)
  })

  it('counts the edges of a rect as inside', () => {
    const root = mount('<p class="block">selected</p>')
    selectWithRect(root.firstElementChild!, { left: 10, right: 90, top: 20, bottom: 40 })
    expect(pointIsInsideSelection(10, 20)).toBe(true)
    expect(pointIsInsideSelection(90, 40)).toBe(true)
  })

  it('reports a point beside the highlight', () => {
    const root = mount('<p class="block">selected</p>')
    selectWithRect(root.firstElementChild!, { left: 10, right: 90, top: 20, bottom: 40 })
    expect(pointIsInsideSelection(120, 30)).toBe(false)
    expect(pointIsInsideSelection(50, 80)).toBe(false)
  })

  // The platform draws its selection handles at the edges of the highlight and
  // below its last line, so a finger reaching for one lands outside every rect.
  it('takes a tolerance for a finger reaching past the edge', () => {
    const root = mount('<p class="block">selected</p>')
    selectWithRect(root.firstElementChild!, { left: 10, right: 90, top: 20, bottom: 40 })
    expect(pointIsInsideSelection(100, 30, 24)).toBe(true)
    expect(pointIsInsideSelection(50, 60, 24)).toBe(true)
    // ...and no further than it was told.
    expect(pointIsInsideSelection(115, 30, 24)).toBe(false)
    expect(pointIsInsideSelection(50, 65, 24)).toBe(false)
  })

  it('reports false with no selection at all', () => {
    expect(pointIsInsideSelection(50, 30)).toBe(false)
  })

  it('reports false for a collapsed selection', () => {
    const root = mount('<p class="block">selected</p>')
    const range = document.createRange()
    range.setStart(textNodeWith(root, 'selected'), 2)
    range.collapse(true)
    const selection = window.getSelection()!
    selection.removeAllRanges()
    selection.addRange(range)
    expect(pointIsInsideSelection(50, 30)).toBe(false)
  })

  // The environment this ships beside: a range with no geometry answers
  // "not on the selection", which is what a real browser answers for an
  // empty rect list too.
  it('reports false where the range has no rects', () => {
    const root = mount('<p class="block">selected</p>')
    const range = document.createRange()
    range.selectNodeContents(root.firstElementChild!)
    const selection = window.getSelection()!
    selection.removeAllRanges()
    selection.addRange(range)
    expect(pointIsInsideSelection(50, 30)).toBe(false)
  })
})
