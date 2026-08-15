import type { TerminalImeHandle } from './terminalIme'
import { Terminal } from '@xterm/xterm'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { compositionPreview } from '~/test-support/compositionPreview'
import { stubMatchMedia } from '~/test-support/matchMediaStub'
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
  dispatchInputEvent(h.textarea, 'beforeinput', { data, inputType })
  h.textarea.value = nextValue
  h.textarea.setSelectionRange(nextValue.length, nextValue.length)
  dispatchInputEvent(h.textarea, 'input', { data, inputType })
  keydown(h.textarea, { key: data.slice(-1), keyCode: 229 })
  h.textarea.dispatchEvent(new KeyboardEvent('keyup', { key: data.slice(-1), keyCode: 68, bubbles: true }))
}

/**
 * Dispatch one input-type event with the init bag a trusted event carries.
 * Trusted input events are composed, and xterm's `_inputEvent` reads that
 * flag to decide whether the key path already sent the character. Leaving
 * it off makes xterm double-send and the test measures the wrong thing.
 */
function dispatchInputEvent(
  target: HTMLElement,
  type: 'beforeinput' | 'input',
  init: { data?: string | null, inputType?: string, isComposing?: boolean },
): void {
  target.dispatchEvent(new InputEvent(type, {
    data: init.data,
    inputType: init.inputType,
    isComposing: init.isComposing ?? false,
    bubbles: true,
    cancelable: true,
    composed: true,
  }))
}

/** One beforeinput + input pair for the same edit, as an engine fires them. */
function input(target: HTMLElement, inputType: string, data: string | null, isComposing = false): void {
  for (const type of ['beforeinput', 'input'] as const)
    dispatchInputEvent(target, type, { data, inputType, isComposing })
}

function keydown(target: HTMLElement, init: KeyboardEventInit & { keyCode?: number }): void {
  target.dispatchEvent(new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init }))
}

/** One trusted-shape keypress carrying a charCode — the echo of a deferred A–Z. */
function keypress(target: HTMLElement, charCode: number): void {
  target.dispatchEvent(new KeyboardEvent('keypress', { charCode, bubbles: true, cancelable: true } as KeyboardEventInit))
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
  let matchMedia: ReturnType<typeof stubMatchMedia>

  beforeEach(() => {
    // jsdom has no matchMedia; the shared stub carries the legacy listener
    // pair xterm's open() needs.
    matchMedia = stubMatchMedia()
  })

  afterEach(() => {
    for (const h of harnesses) {
      h.ime.dispose()
      h.terminal.dispose()
      h.container.remove()
    }
    harnesses = []
    // Restore rather than leave the stub behind.
    matchMedia.restore()
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
    dispatchInputEvent(h.textarea, 'beforeinput', { data: 'x', inputType: 'insertText' })
    // The measurement is only good for the paired input event.
    await Promise.resolve()

    h.textarea.value = 'abc'
    h.textarea.setSelectionRange(3, 3)
    dispatchInputEvent(h.textarea, 'input', { data: '안', inputType: 'insertText' })

    expect(h.sent).toEqual(['안'])
  })

  it('applies the erase across the microtask checkpoint a real browser runs', async () => {
    const h = createHarness()

    // The production shape: a real browser dispatches beforeinput and input
    // as two separate event dispatches, and the JS stack is empty between
    // them, so a microtask checkpoint runs exactly here. A timing-based
    // reset of the measurement dies at that checkpoint, and the erase half
    // of every WKWebView replacement silently never fires. jsdom hides this
    // because a synchronous dispatchEvent keeps the stack non-empty.
    h.textarea.value = 'ㅇ'
    h.textarea.setSelectionRange(0, 1)
    dispatchInputEvent(h.textarea, 'beforeinput', { data: '아', inputType: 'insertReplacementText' })
    await Promise.resolve()
    h.textarea.value = '아'
    h.textarea.setSelectionRange(1, 1)
    dispatchInputEvent(h.textarea, 'input', { data: '아', inputType: 'insertReplacementText' })

    expect(h.sent).toEqual(['\x7F아'])
  })

  it('prefers the engine target ranges over the live selection', () => {
    const h = createHarness()

    // The selection has already collapsed by the time an engine that
    // populates getTargetRanges dispatches beforeinput; the ranges are the
    // engine's own statement of what the edit replaces.
    h.textarea.value = 'abcd'
    h.textarea.setSelectionRange(4, 4)
    const beforeinput = new InputEvent('beforeinput', {
      data: 'x',
      inputType: 'insertText',
      bubbles: true,
      cancelable: true,
      composed: true,
    })
    Object.defineProperty(beforeinput, 'getTargetRanges', {
      value: () => [{
        startContainer: h.textarea,
        endContainer: h.textarea,
        startOffset: 1,
        endOffset: 3,
      }],
    })
    h.textarea.dispatchEvent(beforeinput)
    h.textarea.value = 'axd'
    h.textarea.setSelectionRange(2, 2)
    dispatchInputEvent(h.textarea, 'input', { data: 'x', inputType: 'insertText' })

    expect(h.sent).toEqual(['\x7F\x7Fx'])
  })

  it('falls back to the selection when the target ranges point at another node', () => {
    const h = createHarness()

    // A range shape this module cannot interpret (containers that are not
    // the textarea) must not silence the measurement: the live selection
    // still covers the replaced range in every recorded trace.
    h.textarea.value = 'ab'
    h.textarea.setSelectionRange(0, 1)
    const beforeinput = new InputEvent('beforeinput', {
      data: 'x',
      inputType: 'insertText',
      bubbles: true,
      cancelable: true,
      composed: true,
    })
    Object.defineProperty(beforeinput, 'getTargetRanges', {
      value: () => [{
        startContainer: document.body,
        endContainer: document.body,
        startOffset: 0,
        endOffset: 9,
      }],
    })
    h.textarea.dispatchEvent(beforeinput)
    h.textarea.value = 'xb'
    h.textarea.setSelectionRange(1, 1)
    dispatchInputEvent(h.textarea, 'input', { data: 'x', inputType: 'insertText' })

    expect(h.sent).toEqual(['\x7Fx'])
  })

  it('claims an IME insert that lands while a plain capital is still in flight', () => {
    const h = createHarness()

    // Rollover: the capital's keyup has not arrived when the IME's first
    // jamo does. xterm already sent the capital from keypress, and its
    // `_keyDownSeen` guard drops the jamo — reading the stale keydown as
    // "xterm owns the insertText" loses the jamo, and the follow-up
    // replacement then erases the capital the user actually typed.
    keydown(h.textarea, { key: 'C', keyCode: 67 })
    keypress(h.textarea, 67)
    h.textarea.value = 'C'
    input(h.textarea, 'insertText', 'ㅇ')
    h.textarea.value = 'Cㅇ'
    wkKeystroke(h, 'insertReplacementText', '아', [1, 2], 'C아')

    expect(applyToLine([...h.fromXterm, ...h.sent])).toBe('C아')
  })

  it('claims a dead key precomposed character that xterm never sends', () => {
    const h = createHarness()

    // Dead key: xterm records the dead key, and the follow-up keydown takes
    // xterm's `_unprocessedDeadKey` branch — no bytes, no preventDefault —
    // so the composed character arrives only as an insertText whose text
    // differs from the key in flight.
    keydown(h.textarea, { key: 'Dead' })
    keydown(h.textarea, { key: 'e', keyCode: 69 })
    h.textarea.value = 'é'
    input(h.textarea, 'insertText', 'é')

    expect(h.sent).toEqual(['é'])
    expect(h.fromXterm).toEqual([])
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

  it('suppresses every echo of a multi-character commit', () => {
    const h = createHarness()

    // Japanese and Chinese commit a whole reading in one compositionend. If
    // the engine echoes it as one synthetic keypress per character, every
    // echo must be suppressed — a one-shot suppression lets xterm's
    // `_keyPress` send the remaining characters a second time.
    composition(h.textarea, 'compositionstart', '')
    composition(h.textarea, 'compositionupdate', 'ab')
    composition(h.textarea, 'compositionend', 'ab')

    for (const ch of ['a', 'b']) {
      expect(h.ime.shouldBypassKeyEvent(
        new KeyboardEvent('keypress', { charCode: ch.charCodeAt(0) } as KeyboardEventInit),
      )).toBe(true)
    }
    // The window is spent: a further keypress of a committed character is
    // genuine and must survive.
    expect(h.ime.shouldBypassKeyEvent(
      new KeyboardEvent('keypress', { charCode: 97 } as KeyboardEventInit),
    )).toBe(false)
    expect(h.sent).toEqual(['ab'])
    expect(h.fromXterm).toEqual([])
  })

  it('never lets a lone-surrogate echo keypress reach xterm', () => {
    const h = createHarness()

    // An astral commit (an emoji) is two UTF-16 units, and an engine that
    // echoes per unit emits one synthetic keypress per surrogate half. When
    // its echo count diverges from the per-unit bookkeeping (a repeated
    // half), the surplus keypress carries a lone surrogate. xterm's
    // `_keyPress` would send String.fromCharCode(charCode) verbatim, and a
    // lone surrogate reaches the PTY as U+FFFD — so it must be bypassed
    // unconditionally.
    composition(h.textarea, 'compositionstart', '')
    composition(h.textarea, 'compositionend', '😀')

    const high = 0xD83D
    expect(h.ime.shouldBypassKeyEvent(new KeyboardEvent('keypress', { charCode: high } as KeyboardEventInit))).toBe(true)
    // The divergent surplus echo of the same half is garbage, not input.
    expect(h.ime.shouldBypassKeyEvent(new KeyboardEvent('keypress', { charCode: high } as KeyboardEventInit))).toBe(true)
    // The low half still goes through the ordinary consumption.
    expect(h.ime.shouldBypassKeyEvent(new KeyboardEvent('keypress', { charCode: 0xDE00 } as KeyboardEventInit))).toBe(true)

    expect(h.sent).toEqual(['😀'])
    expect(h.fromXterm).toEqual([])
  })

  it('still suppresses the echo when the IME keydown lands after the commit', () => {
    const h = createHarness()

    // WKWebView delivers the IME-consumed keydown AFTER the input event it
    // produced; the same order around a commit must not close the echo
    // window before the synthetic keypress arrives.
    composition(h.textarea, 'compositionstart', '')
    composition(h.textarea, 'compositionend', '안')
    keydown(h.textarea, { key: 'ㅇ', keyCode: 229 })

    expect(h.ime.shouldBypassKeyEvent(
      new KeyboardEvent('keypress', { charCode: '안'.charCodeAt(0) } as KeyboardEventInit),
    )).toBe(true)
    expect(h.sent).toEqual(['안'])
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

  it('leaves ordinary typing on xterm\'s own key path', () => {
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
    keypress(h.textarea, 67)
    input(h.textarea, 'insertText', 'C')

    expect(h.fromXterm).toEqual(['C'])
    expect(h.sent).toEqual([])
  })

  it('leaves caps-lock-folded text to xterm, which sends the folded case', () => {
    const h = createHarness()

    // Caps lock on: the keydown reports 'C', the keypress (and xterm's send)
    // carries 'c'. Ownership reads the OBSERVED emission, so the input event
    // for 'c' is xterm's echo — claiming it would double the letter.
    keydown(h.textarea, { key: 'C', keyCode: 67 })
    keypress(h.textarea, 99)
    h.textarea.value = 'c'
    input(h.textarea, 'insertText', 'c')

    expect(h.fromXterm).toEqual(['c'])
    expect(h.sent).toEqual([])
  })

  it('claims text whose keystroke produced no xterm emission', () => {
    const h = createHarness()

    // A deferred key whose keypress never ran — an engine that skips it, or
    // delivers the input event first. xterm emits nothing, so the text is
    // ours; reading the key's shape instead would hand it to an xterm path
    // that never fires, and the character would be lost.
    keydown(h.textarea, { key: 'C', keyCode: 67 })
    h.textarea.value = 'C'
    input(h.textarea, 'insertText', 'C')

    expect(h.sent).toEqual(['C'])
    expect(h.fromXterm).toEqual([])
  })

  it('claims an IME insert whose text merely occurs inside an in-flight emission', () => {
    const h = createHarness()

    // An arrow key emits '\x1b[C' through xterm's key path. If its keyup has
    // not landed (WKWebView delivers events out of band; fast typists), a
    // substring match reads an IME-committed 'C' as that emission's echo and
    // silently drops it. An emission must answer only its exact echo.
    keydown(h.textarea, { key: 'ArrowRight', keyCode: 39 })
    input(h.textarea, 'insertText', 'C')

    // The arrow's emission really is in flight — that is what makes 'C'
    // look like a substring of it.
    expect(h.fromXterm).toEqual(['\x1B[C'])
    expect(h.sent).toEqual(['C'])
  })

  it('answers each deferred capital\'s echo with its own emission', () => {
    const h = createHarness()

    // Rollover: both capitals' keypresses ran before either input event
    // arrived. Each input event must consume its own emission — an arrow's
    // '\x1b[C' above shows why the entries cannot be merged into one string —
    // or the second echo would ride the first's match and be double-sent.
    keydown(h.textarea, { key: 'C', keyCode: 67 })
    keypress(h.textarea, 67)
    keydown(h.textarea, { key: 'A', keyCode: 65 })
    keypress(h.textarea, 65)
    input(h.textarea, 'insertText', 'C')
    input(h.textarea, 'insertText', 'A')

    expect(h.fromXterm).toEqual(['C', 'A'])
    expect(h.sent).toEqual([])
  })

  it('expires the emission when the keystroke ends', () => {
    const h = createHarness()

    // A fully-typed capital leaves no emission behind, so a later
    // mouse-committed candidate of the same character is ours, not an echo.
    keydown(h.textarea, { key: 'C', keyCode: 67 })
    keypress(h.textarea, 67)
    h.textarea.dispatchEvent(new KeyboardEvent('keyup', { key: 'C', keyCode: 67, bubbles: true }))
    h.textarea.value = ''
    input(h.textarea, 'insertText', 'C')

    expect(h.sent).toEqual(['C'])
    expect(h.fromXterm).toEqual(['C'])
  })

  it('leaves paste to xterm\'s own paste handler', () => {
    const h = createHarness()

    // xterm binds `paste` on the textarea and writes the clipboard itself.
    // Claiming the input event that follows would send every paste twice, and
    // there is no keystroke in flight to tell the two apart.
    input(h.textarea, 'insertFromPaste', 'pasted text')

    expect(h.sent).toEqual([])
  })

  it('leaves editing input types alone', () => {
    const h = createHarness()

    // Backspace and similar editing keys reach the PTY through xterm's key
    // path as control bytes. Forwarding the matching input event would send
    // them a second time as text.
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
    const preview = () => compositionPreview(h.container)!

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
    const preview = () => compositionPreview(h.container)!

    // xterm parks its helper textarea on the cursor, and the preview borrows
    // that box rather than measuring cells itself.
    h.textarea.style.left = '48px'
    h.textarea.style.top = '17px'
    h.textarea.style.height = '19px'
    // One token, so the assertion below does not really test how the CSSOM
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

  it('drops a keypress that arrives mid-composition', () => {
    const h = createHarness()

    composition(h.textarea, 'compositionstart', '')
    expect(h.ime.shouldBypassKeyEvent(
      new KeyboardEvent('keypress', { charCode: 100 } as KeyboardEventInit),
    )).toBe(true)
  })

  it('releases its listeners and its preview on dispose', () => {
    const h = createHarness()
    expect(compositionPreview(h.container)).not.toBeNull()

    h.ime.dispose()
    expect(compositionPreview(h.container)).toBeNull()

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

  it('refuses to attach when the textarea was detached from the DOM', () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const terminal = new Terminal({ cols: 80, rows: 24 })
    terminal.open(container)
    // A DOM shape change that detaches the helper must fail loudly: a preview
    // silently re-parented to another containing block would position wrong.
    terminal.textarea!.remove()
    expect(() => attachTerminalIme(terminal, () => {})).toThrow(/attached/)
    terminal.dispose()
    container.remove()
  })
})
