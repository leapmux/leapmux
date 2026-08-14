import type { TerminalImeHandle } from './terminalIme'
import { Terminal } from '@xterm/xterm'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { attachTerminalIme } from './terminalIme'

/**
 * The two traces below are not invented. The Chromium one was recorded from a
 * real composition driven through Chrome DevTools Protocol
 * (`Input.imeSetComposition`), and the WebKit one is the trace posted on
 * xtermjs/xterm.js#5894. They are the reason this module exists, so the tests
 * replay them rather than a tidied-up approximation.
 */

interface Harness {
  terminal: Terminal
  ime: TerminalImeHandle
  textarea: HTMLTextAreaElement
  /** Text this module handed to the PTY. */
  sent: string[]
  /** Text xterm produced on its own. Composed input must never appear here. */
  fromXterm: string[]
  container: HTMLDivElement
}

let harnesses: Harness[] = []

function createHarness(): Harness {
  const container = document.createElement('div')
  document.body.appendChild(container)

  const terminal = new Terminal({ cols: 80, rows: 24 })
  terminal.open(container)

  const sent: string[] = []
  const fromXterm: string[] = []
  terminal.onData(data => fromXterm.push(data))

  const ime = attachTerminalIme(terminal, data => sent.push(data))
  // Mirror how TerminalView wires the handler, so these tests exercise the real
  // interaction with xterm's key path rather than the module in isolation.
  terminal.attachCustomKeyEventHandler(event => !ime.shouldBypassKeyEvent(event))

  const harness: Harness = { terminal, ime, textarea: terminal.textarea!, sent, fromXterm, container }
  harnesses.push(harness)
  return harness
}

function composition(target: HTMLElement, type: string, data: string): void {
  target.dispatchEvent(new CompositionEvent(type, { data, bubbles: true, cancelable: true, composed: true }))
}

/**
 * Apply what the module sent to a one-line model of the shell's input line, so
 * the assertions read as "the user ended up with 안녕" rather than as a pinned
 * byte sequence. DEL (0x7F) is what xterm sends for Backspace and what readline
 * treats as backward-delete-char.
 */
function applyToLine(writes: string[]): string {
  const line: string[] = []
  for (const ch of [...writes.join('')]) {
    if (ch === '\x7F')
      line.pop()
    else
      line.push(ch)
  }
  return line.join('')
}

/**
 * One WKWebView keystroke: the text event arrives FIRST, and the keydown the
 * input method consumed follows it. That order is the reverse of Chromium's and
 * it is what the recorded desktop trace shows.
 */
function wkKeystroke(h: Harness, inputType: string, data: string, replacedRange: [number, number] | null, nextValue: string): void {
  if (replacedRange) {
    h.textarea.setSelectionRange(replacedRange[0], replacedRange[1])
  }
  else {
    const end = h.textarea.value.length
    h.textarea.setSelectionRange(end, end)
  }
  h.textarea.dispatchEvent(new InputEvent('beforeinput', {
    data,
    inputType,
    isComposing: false,
    bubbles: true,
    cancelable: true,
    composed: true,
  }))
  h.textarea.value = nextValue
  h.textarea.setSelectionRange(nextValue.length, nextValue.length)
  h.textarea.dispatchEvent(new InputEvent('input', {
    data,
    inputType,
    isComposing: false,
    bubbles: true,
    cancelable: true,
    composed: true,
  }))
  keydown(h.textarea, { key: data.slice(-1), keyCode: 229 })
  h.textarea.dispatchEvent(new KeyboardEvent('keyup', { key: data.slice(-1), keyCode: 68, bubbles: true }))
}

function input(target: HTMLElement, inputType: string, data: string | null, isComposing = false): void {
  for (const type of ['beforeinput', 'input']) {
    target.dispatchEvent(new InputEvent(type, {
      data,
      inputType,
      isComposing,
      bubbles: true,
      cancelable: true,
      // Trusted input events are composed, and xterm's `_inputEvent` reads that
      // flag to decide whether the key path already sent the character. Leaving
      // it off makes xterm double-send and the test measures the wrong thing.
      composed: true,
    }))
  }
}

function keydown(target: HTMLElement, init: KeyboardEventInit & { keyCode?: number }): void {
  target.dispatchEvent(new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init }))
}

/**
 * One syllable of the Chromium trace: an IME-consumed keydown per keystroke,
 * then the composition update and the input event that writes the textarea.
 */
function chromiumCompose(h: Harness, steps: string[], opts: { start: boolean }): void {
  steps.forEach((text, index) => {
    keydown(h.textarea, { key: 'Process', keyCode: 229 })
    if (index === 0 && opts.start)
      composition(h.textarea, 'compositionstart', '')
    composition(h.textarea, 'compositionupdate', text)
    input(h.textarea, 'insertCompositionText', text, true)
    // Chromium writes the composing text into the textarea as it goes.
    h.textarea.value = h.textarea.value.slice(0, h.textarea.value.length - (index === 0 ? 0 : 1)) + text
  })
}

describe('attachTerminalIme', () => {
  let originalMatchMedia: typeof window.matchMedia

  beforeEach(() => {
    originalMatchMedia = window.matchMedia
    // jsdom has no matchMedia, and xterm's CoreBrowserService watches the
    // device-pixel-ratio query through the LEGACY addListener/removeListener
    // pair, so a stub with only addEventListener throws inside open().
    window.matchMedia = vi.fn().mockReturnValue({
      matches: false,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }) as any
  })

  afterEach(() => {
    for (const h of harnesses) {
      h.ime.dispose()
      h.terminal.dispose()
      h.container.remove()
    }
    harnesses = []
    // Restore rather than leave the stub behind: a bare stub leaking out of
    // this suite is what the note in TerminalView.test.tsx exists to work
    // around, and there is no reason to add another source of it.
    window.matchMedia = originalMatchMedia
  })

  it('sends each committed syllable once for the recorded Chromium trace', () => {
    const h = createHarness()

    // 안: ㅇ -> 아 -> 안, then the commit that starts 녕.
    chromiumCompose(h, ['ㅇ', '아', '안'], { start: true })
    keydown(h.textarea, { key: 'Process', keyCode: 229 })
    composition(h.textarea, 'compositionend', '안')
    // Chromium starts the next composition in the same burst, with the
    // committed syllable already in the textarea value.
    composition(h.textarea, 'compositionstart', '')
    chromiumCompose(h, ['ㄴ', '녀', '녕'], { start: false })
    composition(h.textarea, 'compositionend', '녕')

    expect(h.sent).toEqual(['안', '녕'])
  })

  it('composes Korean on WKWebView, which sends no composition events', () => {
    const h = createHarness()

    // Replayed from a desktop (Tauri/WKWebView) recording of someone typing
    // 안녕. There is no compositionstart, no compositionupdate and no
    // compositionend anywhere in it, and `isComposing` is false throughout:
    // WKWebView refines each syllable by REPLACING the text it already
    // inserted. Reading only `insertText` leaves the terminal with ㅇ and ㄴ.
    wkKeystroke(h, 'insertText', 'ㅇ', null, 'ㅇ')
    wkKeystroke(h, 'insertReplacementText', '아', [0, 1], '아')
    wkKeystroke(h, 'insertReplacementText', '안', [0, 1], '안')
    wkKeystroke(h, 'insertReplacementText', '안', [0, 1], '안')
    wkKeystroke(h, 'insertText', 'ㄴ', null, '안ㄴ')
    wkKeystroke(h, 'insertReplacementText', '녀', [1, 2], '안녀')
    wkKeystroke(h, 'insertReplacementText', '녕', [1, 2], '안녕')

    expect(applyToLine(h.sent)).toBe('안녕')
    // xterm's own key path must not have added anything of its own.
    expect(h.fromXterm).toEqual([])
  })

  it('erases exactly what a replacement supersedes', () => {
    const h = createHarness()

    wkKeystroke(h, 'insertText', 'ㅇ', null, 'ㅇ')
    expect(h.sent).toEqual(['ㅇ'])

    // One character replaced means one erase, then the new text, and both in a
    // single write so no other keystroke can land between them.
    wkKeystroke(h, 'insertReplacementText', '아', [0, 1], '아')
    expect(h.sent[1]).toBe('\x7F아')
  })

  it('claims the text even when the previous keyup has not landed yet', () => {
    const h = createHarness()

    // WKWebView delivers the keydown AFTER the input event it produced, so a
    // fast typist leaves the PREVIOUS keystroke still recorded when the next
    // one's text arrives. Reading that stale keydown as "xterm owns this"
    // silently drops the character.
    keydown(h.textarea, { key: 'ㅇ', keyCode: 229 })
    // No keyup. The next syllable's text arrives regardless.
    input(h.textarea, 'insertText', 'ㄴ')

    expect(h.sent).toEqual(['ㄴ'])
  })

  it('never defers a replacement to xterm, which has no branch for one', () => {
    const h = createHarness()

    // A plain keydown that xterm did not claim would make `insertText`
    // ambiguous, but a replacement is unambiguous: xterm only ever inspects
    // `insertText`, so leaving one to xterm loses it.
    h.textarea.value = 'a'
    keydown(h.textarea, { key: 'a', keyCode: 65 })
    wkKeystroke(h, 'insertReplacementText', '안', [0, 1], '안')

    expect(applyToLine(h.sent)).toBe('안')
  })

  it('erases every character a multi-character replacement supersedes', () => {
    const h = createHarness()

    // Japanese and Chinese refine a whole reading rather than one syllable, so
    // a replacement can supersede several characters at once.
    h.textarea.value = 'こんにち'
    wkKeystroke(h, 'insertReplacementText', 'こんにちは', [0, 4], 'こんにちは')

    expect(h.sent).toEqual(['\x7F\x7F\x7F\x7Fこんにちは'])
    expect(applyToLine(['こんにち', ...h.sent])).toBe('こんにちは')
  })

  it('does not carry a measurement over to a later event', async () => {
    const h = createHarness()

    // A beforeinput whose input event never arrives — something cancelled it,
    // or the engine skipped it — must not leave its measurement behind. An
    // input event that then arrives on its own would otherwise erase three
    // characters the user never asked to lose.
    h.textarea.value = 'abc'
    h.textarea.setSelectionRange(0, 3)
    h.textarea.dispatchEvent(new InputEvent('beforeinput', {
      data: 'x',
      inputType: 'insertText',
      bubbles: true,
      cancelable: true,
      composed: true,
    }))
    // The measurement is only good for the input event of the same task.
    await Promise.resolve()

    h.textarea.value = 'abc'
    h.textarea.setSelectionRange(3, 3)
    h.textarea.dispatchEvent(new InputEvent('input', {
      data: '안',
      inputType: 'insertText',
      bubbles: true,
      cancelable: true,
      composed: true,
    }))

    expect(h.sent).toEqual(['안'])
  })

  it('counts a replaced surrogate pair as one character', () => {
    const h = createHarness()

    // An emoji is two UTF-16 units but one character, and the terminal erases
    // by character.
    h.textarea.value = '😀'
    wkKeystroke(h, 'insertReplacementText', 'x', [0, 2], 'x')

    expect(h.sent).toEqual(['\x7Fx'])
  })

  it('keeps composed input away from xterm entirely', () => {
    const h = createHarness()

    chromiumCompose(h, ['ㅇ', '아', '안'], { start: true })
    composition(h.textarea, 'compositionend', '안')

    // xterm's own CompositionHelper is the thing being replaced. If any of its
    // handlers still ran, the syllable would be emitted a second time here.
    expect(h.fromXterm).toEqual([])
    expect(h.sent).toEqual(['안'])
  })

  it('sends nothing when the composition is cancelled', () => {
    const h = createHarness()

    composition(h.textarea, 'compositionstart', '')
    composition(h.textarea, 'compositionupdate', 'ㅇ')
    input(h.textarea, 'insertCompositionText', 'ㅇ', true)
    input(h.textarea, 'deleteCompositionText', null, true)
    composition(h.textarea, 'compositionend', '')

    expect(h.sent).toEqual([])
    expect(h.fromXterm).toEqual([])
  })

  it('suppresses the keypress WebKit echoes after a commit', () => {
    const h = createHarness()

    // The trace from xtermjs/xterm.js#5894: WebKit retracts the composing text,
    // re-inserts it as a commit, ends the composition, and then fires a
    // synthetic keypress whose charCode is the character it just committed.
    composition(h.textarea, 'compositionstart', '')
    composition(h.textarea, 'compositionupdate', '안')
    input(h.textarea, 'insertCompositionText', '안', true)
    input(h.textarea, 'deleteCompositionText', null, true)
    input(h.textarea, 'insertFromComposition', '안', true)
    composition(h.textarea, 'compositionend', '안')

    const echo = new KeyboardEvent('keypress', { charCode: '안'.charCodeAt(0) } as KeyboardEventInit)
    expect(h.ime.shouldBypassKeyEvent(echo)).toBe(true)

    // Only the commit reached the PTY -- not the echo, and nothing from xterm.
    expect(h.sent).toEqual(['안'])
    expect(h.fromXterm).toEqual([])
  })

  it('does not swallow a real keystroke typed after the commit', () => {
    const h = createHarness()

    // WebKit's echo arrives with no keydown between it and the commit. A
    // capital the user actually types later does have one, and must survive
    // even when the composition happened to contain the same character.
    composition(h.textarea, 'compositionstart', '')
    composition(h.textarea, 'compositionend', 'C')
    keydown(h.textarea, { key: 'C', keyCode: 67 })

    expect(h.ime.shouldBypassKeyEvent(
      new KeyboardEvent('keypress', { charCode: 67 } as KeyboardEventInit),
    )).toBe(false)
  })

  it('suppresses the echoed keypress only once', () => {
    const h = createHarness()

    composition(h.textarea, 'compositionstart', '')
    composition(h.textarea, 'compositionupdate', 'a')
    composition(h.textarea, 'compositionend', 'a')

    const first = new KeyboardEvent('keypress', { charCode: 97 } as KeyboardEventInit)
    expect(h.ime.shouldBypassKeyEvent(first)).toBe(true)
    // A genuine keystroke of the same character right afterwards must survive.
    const second = new KeyboardEvent('keypress', { charCode: 97 } as KeyboardEventInit)
    expect(h.ime.shouldBypassKeyEvent(second)).toBe(false)
  })

  it('keeps the keystroke that follows a commit, which xterm drops', () => {
    const h = createHarness()

    composition(h.textarea, 'compositionstart', '')
    composition(h.textarea, 'compositionupdate', '~')
    composition(h.textarea, 'compositionend', '~')
    // WebKit reports this keydown with a two-character `key`, so xterm's
    // printable check rejects it and its `_inputEvent` guard drops the insert.
    keydown(h.textarea, { key: '~/' })
    input(h.textarea, 'insertText', '/')

    expect(h.sent).toEqual(['~', '/'])
  })

  it('leaves ordinary typing on xterm own key path', () => {
    const h = createHarness()

    // xterm handles a plain printable key in keydown and calls preventDefault,
    // which is how this module knows the bytes already went out.
    keydown(h.textarea, { key: 'a', keyCode: 65 })
    // A browser would not fire this after a prevented keydown; dispatching it
    // anyway proves the module does not double-send if one arrives.
    input(h.textarea, 'insertText', 'a')

    expect(h.fromXterm).toEqual(['a'])
    expect(h.sent).toEqual([])
  })

  it('leaves capital letters to xterm, which defers them to keypress', () => {
    const h = createHarness()

    // xterm deliberately does NOT preventDefault a keydown for A-Z: it defers
    // those to `keypress` so lower-case letters still work under an input
    // method with caps lock on. The browser therefore delivers a real `input`
    // event for a capital, and claiming it doubles every one the user types --
    // `CANCELLED` arriving as `CCAANNCCEELLLLEEDD`.
    keydown(h.textarea, { key: 'C', keyCode: 67 })
    h.textarea.dispatchEvent(new KeyboardEvent('keypress', {
      charCode: 67,
      bubbles: true,
      cancelable: true,
    } as KeyboardEventInit))
    input(h.textarea, 'insertText', 'C')

    expect(h.fromXterm).toEqual(['C'])
    expect(h.sent).toEqual([])
  })

  it('leaves paste to xterm own paste handler', () => {
    const h = createHarness()

    // xterm binds `paste` on the textarea and writes the clipboard itself.
    // Claiming the input event that follows would send every paste twice, and
    // there is no keystroke in flight to tell the two apart.
    input(h.textarea, 'insertFromPaste', 'pasted text')

    expect(h.sent).toEqual([])
  })

  it('leaves editing input types alone', () => {
    const h = createHarness()

    // Backspace and friends reach the PTY through xterm's key path as control
    // bytes. Forwarding the matching input event would send them a second time
    // as text.
    input(h.textarea, 'deleteContentBackward', null)
    input(h.textarea, 'insertLineBreak', null)

    expect(h.sent).toEqual([])
  })

  it('claims insertText that arrives with no keystroke in flight', () => {
    const h = createHarness()

    // A candidate committed by clicking the input method's popup: there is no
    // key event at all, and xterm's `_inputEvent` drops it.
    input(h.textarea, 'insertText', '안')

    expect(h.sent).toEqual(['안'])
  })

  it('forgets a keystroke that ends without a keyup', () => {
    const h = createHarness()

    keydown(h.textarea, { key: 'C', keyCode: 67 })
    // Focus leaves mid-keystroke, so no keyup ever arrives. A keydown left
    // standing would make the next mouse-committed candidate look like xterm's.
    h.textarea.dispatchEvent(new FocusEvent('blur'))
    input(h.textarea, 'insertText', '안')

    expect(h.sent).toEqual(['안'])
  })

  it('bypasses xterm key path for IME keydowns on both engines', () => {
    const h = createHarness()

    // Chromium marks the keystroke with the legacy 229 code.
    expect(h.ime.shouldBypassKeyEvent(
      new KeyboardEvent('keydown', { key: 'Process', keyCode: 229 } as KeyboardEventInit),
    )).toBe(true)
    // WebKit can report the real key while a composition is running.
    expect(h.ime.shouldBypassKeyEvent(
      new KeyboardEvent('keydown', { key: 'd', isComposing: true } as KeyboardEventInit),
    )).toBe(true)
    // Everything else stays xterm's.
    expect(h.ime.shouldBypassKeyEvent(
      new KeyboardEvent('keydown', { key: 'd', keyCode: 68 }),
    )).toBe(false)
    expect(h.ime.shouldBypassKeyEvent(
      new KeyboardEvent('keyup', { key: 'd', keyCode: 68 }),
    )).toBe(false)
  })

  it('stops xterm sending the raw key of a composed keystroke', () => {
    const h = createHarness()

    // The case the bypass exists for: WebKit reports a real keyCode during
    // composition, so without it xterm would send the Latin letter the input
    // method consumed.
    composition(h.textarea, 'compositionstart', '')
    keydown(h.textarea, { key: 'd', keyCode: 68, isComposing: true })

    expect(h.fromXterm).toEqual([])
    expect(h.sent).toEqual([])
  })

  it('shows the composing text and hides it on commit', () => {
    const h = createHarness()
    const preview = () => h.container.querySelector<HTMLElement>('[data-testid="terminal-composition-preview"]')!

    expect(preview().textContent).toBe('')

    composition(h.textarea, 'compositionstart', '')
    composition(h.textarea, 'compositionupdate', '안')
    expect(preview().textContent).toBe('안')
    expect(preview().className).toContain('Active')

    composition(h.textarea, 'compositionend', '안')
    expect(preview().textContent).toBe('')
    expect(preview().className).not.toContain('Active')
  })

  it('places the preview on the cursor and in the terminal colors', () => {
    const h = createHarness()
    const preview = () => h.container.querySelector<HTMLElement>('[data-testid="terminal-composition-preview"]')!

    // xterm parks its helper textarea on the cursor, and the preview borrows
    // that box rather than measuring cells itself.
    h.textarea.style.left = '48px'
    h.textarea.style.top = '17px'
    h.textarea.style.height = '19px'
    // One token, so the assertion below is not really testing how the CSSOM
    // quotes a family name that contains a space.
    h.terminal.options.fontFamily = 'TestMono'
    h.terminal.options.fontSize = 15
    h.terminal.options.theme = { background: '#101010', foreground: '#f0f0f0' }

    composition(h.textarea, 'compositionstart', '')
    composition(h.textarea, 'compositionupdate', '안')

    expect(preview().style.left).toBe('48px')
    expect(preview().style.top).toBe('17px')
    expect(preview().style.height).toBe('19px')
    expect(preview().style.fontFamily).toBe('TestMono')
    expect(preview().style.fontSize).toBe('15px')
    // Inverted, so the pending text reads as marked rather than as shell output.
    expect(preview().style.backgroundColor).toBe('rgb(240, 240, 240)')
    expect(preview().style.color).toBe('rgb(16, 16, 16)')
  })

  it('tracks composing across the composition', () => {
    const h = createHarness()

    expect(h.ime.composing).toBe(false)
    composition(h.textarea, 'compositionstart', '')
    expect(h.ime.composing).toBe(true)
    composition(h.textarea, 'compositionend', '안')
    expect(h.ime.composing).toBe(false)
  })

  it('drops a keypress that arrives mid-composition', () => {
    const h = createHarness()

    composition(h.textarea, 'compositionstart', '')
    expect(h.ime.shouldBypassKeyEvent(
      new KeyboardEvent('keypress', { charCode: 100 } as KeyboardEventInit),
    )).toBe(true)
  })

  it('releases its listeners and its preview on dispose', () => {
    const h = createHarness()
    const selector = '[data-testid="terminal-composition-preview"]'
    expect(h.container.querySelector(selector)).not.toBeNull()

    h.ime.dispose()
    expect(h.container.querySelector(selector)).toBeNull()

    // With the layer gone the events belong to xterm again, so nothing more
    // reaches our sink.
    const before = h.sent.length
    composition(h.textarea, 'compositionstart', '')
    composition(h.textarea, 'compositionend', '안')
    expect(h.sent.length).toBe(before)

    // dispose() must be safe to call twice -- the instance teardown in
    // ~/lib/terminal calls it, and so does the afterEach here.
    expect(() => h.ime.dispose()).not.toThrow()
  })

  it('refuses to attach to a terminal that was never opened', () => {
    const terminal = new Terminal()
    expect(() => attachTerminalIme(terminal, () => {})).toThrow(/terminal\.open/)
    terminal.dispose()
  })
})
