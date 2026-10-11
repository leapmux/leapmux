import type { Locator, Page } from '@playwright/test'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { codeBlockText, pasteText } from './composer'

vi.mock('./ui', () => ({
  composerEditor: (page: { composer: unknown }) => page.composer,
}))

afterEach(() => {
  vi.unstubAllGlobals()
  document.body.replaceChildren()
})

/** A locator whose `evaluate` runs the page function against `element`, as Playwright does in the browser. */
function elementLocator(element: Element, children: Record<string, Element> = {}): Locator {
  const locator = {
    evaluate: async <R, A>(body: (node: Element, argument: A) => R, argument: A) => body(element, argument),
    locator: (selector: string) => {
      const child = children[selector]
      if (!child)
        throw new Error(`The test prepared no element for ${selector}.`)
      return elementLocator(child)
    },
  }
  return locator as unknown as Locator
}

/** The minimum of `DataTransfer` that a paste reads, which jsdom does not implement. */
class FakeDataTransfer {
  private readonly items = new Map<string, string>()
  setData(format: string, value: string) {
    this.items.set(format, value)
  }

  getData(format: string) {
    return this.items.get(format) ?? ''
  }
}

/** A `ClipboardEvent` that carries its `clipboardData`, which jsdom does not implement either. */
class FakeClipboardEvent extends Event {
  readonly clipboardData: FakeDataTransfer | null
  constructor(type: string, init: EventInit & { clipboardData?: FakeDataTransfer }) {
    super(type, init)
    this.clipboardData = init.clipboardData ?? null
  }
}

describe('pasteText', () => {
  it('dispatches one cancelable, bubbling paste on the visible composer, with the text as plain text', async () => {
    vi.stubGlobal('DataTransfer', FakeDataTransfer)
    vi.stubGlobal('ClipboardEvent', FakeClipboardEvent)
    const host = document.createElement('div')
    const editor = document.createElement('div')
    host.append(editor)
    document.body.append(host)
    const received: FakeClipboardEvent[] = []
    host.addEventListener('paste', event => received.push(event as FakeClipboardEvent))

    await pasteText({ composer: elementLocator(editor) } as unknown as Page, '- foo\n- bar')

    expect(received).toHaveLength(1)
    const [event] = received
    expect(event!.target).toBe(editor)
    expect(event!.bubbles).toBe(true)
    expect(event!.cancelable).toBe(true)
    expect(event!.clipboardData?.getData('text/plain')).toBe('- foo\n- bar')
  })
})

describe('codeBlockText', () => {
  /** An editor whose `pre code` holds the language label widget before the code. */
  function editorWithCode(label: string, code: string): { editor: Locator, element: Element } {
    const element = document.createElement('code')
    const widget = document.createElement('span')
    widget.className = 'code-lang-label'
    widget.textContent = label
    element.append(widget, document.createTextNode(code))
    return { editor: elementLocator(document.createElement('div'), { 'pre code': element }), element }
  }

  it('reads the code without the language label', async () => {
    const { editor } = editorWithCode('python', 'print("hello")')
    await expect(codeBlockText(editor)).resolves.toBe('print("hello")')
  })

  it('leaves the editor itself unchanged', async () => {
    const { editor, element } = editorWithCode('plaintext', 'just plain text')
    await codeBlockText(editor)
    expect(element.textContent).toBe('plaintextjust plain text')
  })

  it('reads an empty block as the empty string', async () => {
    const { editor } = editorWithCode('plaintext', '')
    await expect(codeBlockText(editor)).resolves.toBe('')
  })
})
