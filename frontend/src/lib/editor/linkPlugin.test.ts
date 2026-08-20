import { Schema } from '@milkdown/prose/model'
import { EditorState, TextSelection } from '@milkdown/prose/state'
import { describe, expect, it, vi } from 'vitest'
import { clickAction, createArmedPress, linkRangeAt, linkShortcutTarget, pressClosesPopover } from './linkPlugin'

/**
 * A real schema, mirroring Milkdown's commonmark declarations for the two marks
 * that matter here: `link` carries an href, and `strong` is what splits a marked
 * run into several text nodes.
 */
const schema = new Schema({
  nodes: {
    doc: { content: 'block+' },
    paragraph: { group: 'block', content: 'inline*' },
    text: { group: 'inline', inline: true },
  },
  marks: {
    link: { attrs: { href: {}, title: { default: null } } },
    strong: {},
  },
})

const link = (href: string, title: string | null = null) => schema.marks.link!.create({ href, title })
const strong = schema.marks.strong!.create()

function stateOf(...content: ReturnType<typeof schema.text>[]) {
  return EditorState.create({
    doc: schema.nodes.doc!.create(null, schema.nodes.paragraph!.create(null, content)),
  })
}

/** A state whose doc holds two paragraphs, for the cross-block selection case. */
function twoParagraphState(a: string, b: string) {
  return EditorState.create({
    doc: schema.nodes.doc!.create(null, [
      schema.nodes.paragraph!.create(null, schema.text(a)),
      schema.nodes.paragraph!.create(null, schema.text(b)),
    ]),
  })
}

/** `state` with the selection set to [from, to). */
function withSelection(state: EditorState, from: number, to: number) {
  return state.apply(state.tr.setSelection(TextSelection.create(state.doc, from, to)))
}

describe('linkRangeAt', () => {
  it('returns null outside a link', () => {
    const state = stateOf(schema.text('plain text'))
    expect(linkRangeAt(state, 3)).toBeNull()
  })

  it('finds the run and its href from a click inside it', () => {
    // doc(1) > paragraph(1) so the text starts at position 1.
    const state = stateOf(schema.text('docs', [link('https://x.test')]))
    const range = linkRangeAt(state, 3)

    expect(range).toEqual({ from: 1, to: 5, href: 'https://x.test', attrs: { href: 'https://x.test', title: null } })
  })

  it('covers the whole link when another mark splits it into several text nodes', () => {
    // `[**bold** rest](url)` is TWO text nodes under one link mark. Editing or
    // removing only the clicked node would leave half the link behind, still
    // carrying the old href.
    const href = 'https://x.test'
    const state = stateOf(
      schema.text('bold', [link(href), strong]),
      schema.text(' rest', [link(href)]),
    )

    const fromBoldHalf = linkRangeAt(state, 3)
    const fromPlainHalf = linkRangeAt(state, 7)

    expect(fromBoldHalf).toEqual({ from: 1, to: 10, href, attrs: { href, title: null } })
    expect(fromPlainHalf).toEqual(fromBoldHalf)
  })

  it('stops at a neighbouring link with a different href', () => {
    const state = stateOf(
      schema.text('one', [link('https://one.test')]),
      schema.text('two', [link('https://two.test')]),
    )

    expect(linkRangeAt(state, 2)).toEqual({ from: 1, to: 4, href: 'https://one.test', attrs: { href: 'https://one.test', title: null } })
    expect(linkRangeAt(state, 5)).toEqual({ from: 4, to: 7, href: 'https://two.test', attrs: { href: 'https://two.test', title: null } })
  })

  // `$pos.marks()` reports the marks of the node BEFORE the position whenever
  // the position sits on a text-node boundary. Resolving from that made the
  // feature work or not depending on whether text preceded the link, and made a
  // click in the empty space past a trailing link open its popover.
  it('finds a link at its leading edge when plain text precedes it', () => {
    const state = stateOf(
      schema.text('see '),
      schema.text('docs', [link('https://x.test')]),
    )
    // Position 5 is the boundary: the end of 'see ' and the first character of
    // the link. Clicking the left half of the 'd' resolves here.
    expect(linkRangeAt(state, 5)).toEqual({
      from: 5,
      to: 9,
      href: 'https://x.test',
      attrs: { href: 'https://x.test', title: null },
    })
  })

  it('returns null past the end of a trailing link', () => {
    const state = stateOf(
      schema.text('read the '),
      schema.text('docs', [link('https://x.test')]),
    )
    // The paragraph end, which is also the link run's `to`. A click in the blank
    // space to the right of the text lands here.
    expect(linkRangeAt(state, 14)).toBeNull()
  })

  it('picks the RIGHT link at the join between two different links', () => {
    // The boundary belongs to the link the caret would enter, not the one it
    // leaves. Reading the preceding node answered with the left link.
    const state = stateOf(
      schema.text('one', [link('https://one.test')]),
      schema.text('two', [link('https://two.test')]),
    )
    expect(linkRangeAt(state, 4)?.href).toBe('https://two.test')
  })

  it('carries the whole attribute set, so an edit cannot drop the title', () => {
    // Milkdown's link schema declares `title` beside `href` and serializes it,
    // so `[docs](url "The docs")` round-trips one. Rebuilding the mark from the
    // href alone lost it with nothing said.
    const state = stateOf(schema.text('docs', [link('https://x.test', 'The docs')]))

    expect(linkRangeAt(state, 3)?.attrs).toEqual({ href: 'https://x.test', title: 'The docs' })
  })

  it('returns null for a schema with no link mark', () => {
    const bare = new Schema({
      nodes: {
        doc: { content: 'block+' },
        paragraph: { group: 'block', content: 'inline*' },
        text: { group: 'inline', inline: true },
      },
    })
    const state = EditorState.create({
      doc: bare.nodes.doc!.create(null, bare.nodes.paragraph!.create(null, bare.text('hi'))),
    })

    expect(linkRangeAt(state, 2)).toBeNull()
  })
})

// The click plugin's decision, which had NO coverage and shipped a bug that only
// a browser could show: the reopen ran from ProseMirror's `handleClick`, which
// ProseMirror does not dispatch for every click. After a save rewrote the
// document and refocused the editor, the next click on the link reached the raw
// `pointerdown` handler and then nothing -- so the popover stayed shut about one
// time in three, with no error anywhere. Both halves are raw DOM handlers now,
// and the decision between them is these two pure functions.
describe('pressClosesPopover', () => {
  const run = { from: 1, to: 5, href: 'https://a.test', attrs: {} }

  it('closes when the press lands on the run the popover is already open for', () => {
    expect(pressClosesPopover(run, run, true)).toBe(true)
  })

  it('opens when the popover is shut, however the press lands', () => {
    // The state at POINTERDOWN is the only one that can tell these apart: a
    // `popover="auto"` light-dismisses before the click is dispatched, so by then
    // every press looks like it landed with the popover closed.
    expect(pressClosesPopover(run, run, false)).toBe(false)
  })

  it('opens when the press lands on a DIFFERENT run', () => {
    // Clicking straight from one link to another swaps the popover to the second
    // rather than toggling the first shut.
    expect(pressClosesPopover({ ...run, from: 7, to: 11 }, run, true)).toBe(false)
  })

  it('opens when nothing was open for any run yet', () => {
    expect(pressClosesPopover(run, null, true)).toBe(false)
  })

  it('does not close on a press outside every link', () => {
    expect(pressClosesPopover(null, run, true)).toBe(false)
  })
})

describe('clickAction', () => {
  const run = { from: 1, to: 5, href: 'https://a.test', attrs: {} }

  it('opens a press that landed on a link with the popover shut', () => {
    expect(clickAction(run, false, true)).toBe('open')
  })

  it('closes a press the pointerdown marked as a toggle-off', () => {
    expect(clickAction(run, true, true)).toBe('close')
  })

  it('closes a press that landed outside every link', () => {
    expect(clickAction(null, false, true)).toBe('close')
  })

  it('closes rather than opening when both say so', () => {
    expect(clickAction(null, true, true)).toBe('close')
  })

  it('reopens after a save, which is the sequence that used to fail', () => {
    // Save leaves the popover shut and the stored range untouched, so the next
    // press on the SAME run must read as a fresh open -- not as a toggle-off
    // against a range that is still lying around.
    const afterSave = pressClosesPopover(run, run, false)
    expect(afterSave).toBe(false)
    expect(clickAction(run, afterSave, true)).toBe('open')
  })

  // A SELECTION gesture is not a request to edit the link.
  //
  // ProseMirror's own `handleClick` never fired for either of these -- it sets
  // `allowDefault` on a shift-click and on a drag past 4 px, and skips the
  // single-click path when it is set. Reading the raw `click` event gave up that
  // suppression, so the rule is stated here: drag-selecting part of a link's
  // text, or shift-clicking to extend a selection onto one, popped the URL
  // editor over the text the user was selecting.
  it('closes when the gesture left a selection rather than a caret', () => {
    expect(clickAction(run, false, false)).toBe('close')
  })

  it('closes a selecting gesture whatever the pointerdown decided', () => {
    expect(clickAction(run, true, false)).toBe('close')
    expect(clickAction(null, false, false)).toBe('close')
  })
})

describe('linkShortcutTarget', () => {
  // Mod-K over a SELECTION means "link this text", so the popover opens empty.
  it('offers an empty href for a selection, so the user types a new URL', () => {
    const state = withSelection(stateOf(schema.text('the design doc')), 5, 11)

    expect(linkShortcutTarget(state)).toEqual({ from: 5, to: 11, href: '', attrs: {} })
  })

  // The range is the SELECTION, not the link inside it. applyLinkHref clears the
  // range before it marks it, so applying overrides the old link rather than
  // leaving the selection carrying two.
  it('takes the selection, not the link inside it, so applying overrides', () => {
    const state = withSelection(
      stateOf(schema.text('see '), schema.text('docs', [link('https://old.test')])),
      1,
      9,
    )

    expect(linkShortcutTarget(state)).toEqual({ from: 1, to: 9, href: '', attrs: {} })
  })

  // Mod-K with a bare CARET on a link means "edit this link", so the popover
  // opens on its current URL and the range covers the whole run.
  it('offers the link under a caret, with its current href', () => {
    const state = withSelection(stateOf(schema.text('docs', [link('https://x.test', 'The docs')])), 3, 3)

    expect(linkShortcutTarget(state)).toEqual({
      from: 1,
      to: 5,
      href: 'https://x.test',
      attrs: { href: 'https://x.test', title: 'The docs' },
    })
  })

  it('covers the whole run when another mark splits the link', () => {
    const href = 'https://x.test'
    const state = withSelection(
      stateOf(schema.text('bold', [link(href), strong]), schema.text(' rest', [link(href)])),
      3,
      3,
    )

    expect(linkShortcutTarget(state)).toMatchObject({ from: 1, to: 10, href })
  })

  it('does nothing for a caret outside a link', () => {
    expect(linkShortcutTarget(withSelection(stateOf(schema.text('plain text')), 4, 4))).toBeNull()
  })

  // One mark cannot span a block boundary, so linking part of the selection
  // would be a silent half-answer.
  it('does nothing for a selection that crosses a block boundary', () => {
    const state = twoParagraphState('first', 'second')
    expect(linkShortcutTarget(withSelection(state, 2, 9))).toBeNull()
  })
})

describe('createArmedPress', () => {
  // The ordinary press: pointerdown captures the answer, and the click that
  // completes it reads exactly that -- never the live popover state, which the
  // browser's light-dismiss pass has already changed by then.
  it('gives the click the answer its own pointerdown captured', () => {
    const press = createArmedPress()
    const live = vi.fn(() => false)

    press.arm(true)

    expect(press.take(live)).toBe(true)
    expect(live).not.toHaveBeenCalled()
  })

  // The press is spent by the click that consumes it. A second click with no
  // pointerdown between them -- a synthetic `click()`, an assistive technology
  // activating the link -- inherited the first press's intent and shut the
  // popover on a click that asked to open it.
  it('spends the press on one click, so the next one asks again', () => {
    const press = createArmedPress()

    press.arm(true)
    press.take(() => false)

    expect(press.take(() => false)).toBe(false)
  })

  // A press the browser takes over for a scroll ends in pointercancel and
  // produces no click at all, so nothing else disarms it.
  it('drops the answer of a press that ends with no click', () => {
    const press = createArmedPress()

    press.arm(true)
    press.disarm()

    expect(press.take(() => false)).toBe(false)
  })

  // The fallback is the CORRECT answer for an unarmed click, not a safe guess:
  // no pointerdown ran, so no light-dismiss pass ran, so the popover state the
  // click reads is still truthful. It must be able to say "close" too.
  it('reads the live state for a click no press armed', () => {
    expect(createArmedPress().take(() => true)).toBe(true)
    expect(createArmedPress().take(() => false)).toBe(false)
  })
})
