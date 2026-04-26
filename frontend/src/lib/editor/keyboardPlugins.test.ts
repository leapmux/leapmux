import type { Editor } from '@milkdown/core'
import { defaultValueCtx, Editor as EditorImpl, editorViewCtx, rootCtx, serializerCtx } from '@milkdown/core'
import { commonmark } from '@milkdown/preset-commonmark'
import { gfm } from '@milkdown/preset-gfm'
import { TextSelection } from '@milkdown/prose/state'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createTabKeyPlugin } from './keyboardPlugins'

/**
 * Unit tests for `createTabKeyPlugin` exercising every branch of the keyboard
 * handler against a real Milkdown editor mounted in jsdom. Replaces the
 * Tab/Shift+Tab portions of `frontend/tests/e2e/20-markdown-editor-input.spec.ts`.
 */

let editor: Editor | undefined
let host: HTMLDivElement | undefined

afterEach(async () => {
  await editor?.destroy()
  editor = undefined
  host?.remove()
  host = undefined
})

async function makeEditor(initialMarkdown: string, onShiftTabInParagraph: () => void = () => {}) {
  host = document.createElement('div')
  document.body.appendChild(host)

  editor = await EditorImpl.make()
    .config((ctx) => {
      ctx.set(rootCtx, host!)
      ctx.set(defaultValueCtx, initialMarkdown)
    })
    .use(commonmark)
    .use(gfm)
    .use(createTabKeyPlugin({ onShiftTabInParagraph }))
    .create()
  return editor
}

function setSelectionAt(pos: number) {
  editor?.action((ctx) => {
    const view = ctx.get(editorViewCtx)
    const $pos = view.state.doc.resolve(Math.min(pos, view.state.doc.content.size))
    view.dispatch(view.state.tr.setSelection(TextSelection.near($pos)))
  })
}

function dispatchTab(shift = false) {
  editor?.action((ctx) => {
    const view = ctx.get(editorViewCtx)
    const event = new KeyboardEvent('keydown', { key: 'Tab', shiftKey: shift, bubbles: true, cancelable: true })
    view.someProp('handleKeyDown', f => f(view, event))
  })
}

function getMarkdown(): string {
  let md = ''
  editor?.action((ctx) => {
    const view = ctx.get(editorViewCtx)
    const serializer = ctx.get(serializerCtx)
    md = serializer(view.state.doc)
  })
  return md
}

function getDoc(): { topNodeName: string, level?: number, html: string } {
  let topNodeName = ''
  let level: number | undefined
  let html = ''
  editor?.action((ctx) => {
    const view = ctx.get(editorViewCtx)
    const first = view.state.doc.firstChild
    topNodeName = first?.type.name ?? ''
    level = first?.attrs.level as number | undefined
    html = view.dom.innerHTML
  })
  return { topNodeName, level, html }
}

describe('tab key plugin — paragraph', () => {
  it('tab on a plain paragraph promotes it to a level-1 heading', async () => {
    await makeEditor('hello')
    setSelectionAt(1)
    dispatchTab()

    const doc = getDoc()
    expect(doc.topNodeName).toBe('heading')
    expect(doc.level).toBe(1)
  })

  it('shift+Tab on a plain paragraph fires onShiftTabInParagraph', async () => {
    const onShift = vi.fn()
    await makeEditor('hello', onShift)
    setSelectionAt(1)
    dispatchTab(true)
    expect(onShift).toHaveBeenCalledOnce()
  })
})

describe('tab key plugin — heading', () => {
  it('tab on H1 promotes to H2', async () => {
    await makeEditor('# title')
    setSelectionAt(1)
    dispatchTab()
    expect(getDoc().level).toBe(2)
  })

  it('tab on H6 stays at H6 (clamped)', async () => {
    await makeEditor('###### deepest')
    setSelectionAt(1)
    dispatchTab()
    expect(getDoc().level).toBe(6)
  })

  it('shift+Tab on H2 demotes to H1', async () => {
    await makeEditor('## title')
    setSelectionAt(1)
    dispatchTab(true)
    expect(getDoc().level).toBe(1)
  })

  it('shift+Tab on H1 collapses to a plain paragraph', async () => {
    await makeEditor('# title')
    setSelectionAt(1)
    dispatchTab(true)
    expect(getDoc().topNodeName).toBe('paragraph')
  })
})

/**
 * Locate a position inside the first paragraph that contains the given text
 * substring. Used to put the cursor on a specific list item / blockquote line
 * without relying on hard-coded offsets that vary between Milkdown versions.
 */
function selectInsideText(needle: string) {
  editor?.action((ctx) => {
    const view = ctx.get(editorViewCtx)
    let target = -1
    view.state.doc.descendants((node, pos) => {
      if (target !== -1)
        return false
      if (node.isTextblock && node.textContent.includes(needle)) {
        const offset = node.textContent.indexOf(needle)
        target = pos + 1 + offset + 1
        return false
      }
      return true
    })
    if (target === -1)
      throw new Error(`could not find textblock containing "${needle}"`)
    const $pos = view.state.doc.resolve(target)
    view.dispatch(view.state.tr.setSelection(TextSelection.near($pos)))
  })
}

describe('tab key plugin — list items', () => {
  it('tab indents a bullet list item one level (sinkListItem)', async () => {
    await makeEditor('- item 1\n- item 2')
    selectInsideText('item 2')
    dispatchTab()

    const md = getMarkdown()
    // After indenting, item 2 becomes a nested list under item 1. Milkdown's
    // serializer may emit `-` or `*` for the inner marker; accept either.
    expect(md).toMatch(/[-*]\s+item 1\n\s+[-*]\s+item 2/)
  })

  it('shift+Tab on a nested bullet list item lifts it back out', async () => {
    await makeEditor('- item 1\n  - nested')
    selectInsideText('nested')
    dispatchTab(true)

    const md = getMarkdown()
    // After lifting, "nested" must no longer be indented under "item 1".
    expect(md).not.toMatch(/\n\s{2,}[-*]\s+nested/)
    // Both list items should appear at top-level (with either bullet marker).
    expect(md).toMatch(/[-*]\s+item 1/)
    expect(md).toMatch(/[-*]\s+nested/)
  })
})

describe('tab key plugin — blockquote', () => {
  it('tab inside a blockquote nests it deeper', async () => {
    await makeEditor('> quoted')
    // Position cursor inside the blockquote text.
    editor?.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      // Blockquote starts at 0, paragraph inside at 1, text at 2.
      const $pos = view.state.doc.resolve(3)
      view.dispatch(view.state.tr.setSelection(TextSelection.near($pos)))
    })

    dispatchTab()

    const html = getDoc().html
    expect(html).toContain('<blockquote>')
    // Two nested blockquotes.
    expect((html.match(/<blockquote>/g) ?? []).length).toBeGreaterThanOrEqual(2)
  })

  it('shift+Tab inside a nested blockquote lifts one level out', async () => {
    await makeEditor('> > deeply quoted')
    // Cursor inside the inner blockquote.
    editor?.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      const $pos = view.state.doc.resolve(5)
      view.dispatch(view.state.tr.setSelection(TextSelection.near($pos)))
    })

    dispatchTab(true)

    const html = getDoc().html
    expect((html.match(/<blockquote>/g) ?? []).length).toBe(1)
  })
})

describe('tab key plugin — code block', () => {
  it('tab at column 0 inserts spaces to the next tab stop (2 spaces)', async () => {
    await makeEditor('```\n```')
    // Cursor inside the empty code block.
    editor?.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      // Code block at pos 0; text inside at pos 1.
      const $pos = view.state.doc.resolve(1)
      view.dispatch(view.state.tr.setSelection(TextSelection.near($pos)))
    })

    dispatchTab()

    const md = getMarkdown()
    // Two spaces inserted on the empty line inside the fence.
    expect(md).toContain('```\n  \n```')
  })

  it('shift+Tab on a 4-space indented line snaps back one tab stop (to 2 spaces)', async () => {
    await makeEditor('```\n    indented\n```')
    // Cursor anywhere on the indented line — Shift+Tab uses the line offset,
    // not the cursor column.
    editor?.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      let codeBlockStart = -1
      view.state.doc.descendants((node, pos) => {
        if (node.type.name === 'code_block' && codeBlockStart === -1) {
          codeBlockStart = pos + 1
          return false
        }
        return true
      })
      const $pos = view.state.doc.resolve(codeBlockStart + 4)
      view.dispatch(view.state.tr.setSelection(TextSelection.near($pos)))
    })

    dispatchTab(true)

    const md = getMarkdown()
    expect(md).toContain('```\n  indented\n```')
  })
})

describe('tab key plugin — modifier guards', () => {
  it('cmd+Tab is ignored', async () => {
    const onShift = vi.fn()
    await makeEditor('hello', onShift)
    setSelectionAt(1)

    editor?.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      const event = new KeyboardEvent('keydown', { key: 'Tab', metaKey: true, bubbles: true, cancelable: true })
      view.someProp('handleKeyDown', f => f(view, event))
    })

    // No transformation — paragraph stays a paragraph.
    expect(getDoc().topNodeName).toBe('paragraph')
    expect(onShift).not.toHaveBeenCalled()
  })

  it('ctrl+Shift+Tab is ignored', async () => {
    const onShift = vi.fn()
    await makeEditor('hello', onShift)
    setSelectionAt(1)

    editor?.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      const event = new KeyboardEvent('keydown', { key: 'Tab', ctrlKey: true, shiftKey: true, bubbles: true, cancelable: true })
      view.someProp('handleKeyDown', f => f(view, event))
    })

    expect(getDoc().topNodeName).toBe('paragraph')
    expect(onShift).not.toHaveBeenCalled()
  })

  it('non-Tab key is a no-op', async () => {
    await makeEditor('hello')
    setSelectionAt(1)

    editor?.action((ctx) => {
      const view = ctx.get(editorViewCtx)
      const event = new KeyboardEvent('keydown', { key: 'a', bubbles: true, cancelable: true })
      view.someProp('handleKeyDown', f => f(view, event))
    })

    expect(getDoc().topNodeName).toBe('paragraph')
  })
})
