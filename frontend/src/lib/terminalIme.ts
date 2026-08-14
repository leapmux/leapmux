import type { Terminal } from '@xterm/xterm'
import * as styles from './terminalIme.css'

/**
 * LeapMux owns the terminal's text-input path for input methods (IME).
 *
 * xterm never reads `compositionend.data`. It rebuilds the committed text by
 * diffing substrings of its hidden helper textarea across `setTimeout(0)`
 * boundaries (`CompositionHelper._finalizeComposition`), which assumes an event
 * order and a textarea value that only Chromium actually provides. Measured:
 * Chromium composes `안녕` correctly, WebKit does not. On WebKit a commit is
 * followed by a synthetic `keypress` carrying the COMMITTED character, so xterm
 * emits it a second time from `_keyPress`, and the keystroke after the commit is
 * then dropped by the `_keyDownSeen` guard in `_inputEvent`
 * (xtermjs/xterm.js#5894). Korean commits once per syllable, so the damage
 * repeats for every syllable and only stray jamo survive.
 *
 * This module takes the whole path instead of patching the arithmetic. It reads
 * `compositionend.data`, which the UI Events specification defines as the final
 * composed string, so it does not depend on the event order of any engine.
 *
 * It needs two things to be true, both verified against xterm 6.0.0:
 *
 * - `terminal.element` is an ANCESTOR of `terminal.textarea`
 *   (`.xterm > .xterm-screen > .xterm-helpers > .xterm-helper-textarea`), and
 *   xterm binds its composition handlers on the textarea itself. A capture-phase
 *   listener on the ancestor therefore runs first and can stop the event before
 *   xterm's `CompositionHelper` ever sees it.
 * - `attachCustomKeyEventHandler` runs BEFORE `_compositionHelper.keydown()` in
 *   `CoreBrowserTerminal._keyDown`, so returning `false` from it bypasses xterm's
 *   composition machinery for a keystroke completely.
 *
 * With the composition events intercepted, xterm's `isComposing` stays `false`,
 * so its `_syncTextArea()` keeps parking the helper textarea on the cursor. The
 * IME candidate window therefore lands in the right place with no work here.
 */
export interface TerminalImeHandle {
  /** True while a composition is active. */
  readonly composing: boolean
  /**
   * Whether xterm must not process this key event. Pass every event that
   * `attachCustomKeyEventHandler` receives; return `false` from that handler
   * when this returns true.
   */
  shouldBypassKeyEvent: (event: KeyboardEvent) => boolean
  dispose: () => void
}

/**
 * `inputType` values a browser reports while it is editing composed text. Every
 * one of them is the composition's business, so xterm must not see any of them.
 * `insertFromComposition` and `deleteByComposition` are WebKit's; Chromium uses
 * `insertCompositionText` throughout and `deleteCompositionText` to retract.
 */
const COMPOSITION_INPUT_TYPES = new Set([
  'insertCompositionText',
  'deleteCompositionText',
  'insertFromComposition',
  'deleteByComposition',
])

/**
 * `inputType` values that put text into the textarea.
 *
 * `insertReplacementText` is how WKWebView drives an input method, and it is
 * the whole reason CJK failed in the desktop app. Recorded there while typing
 * `안녕`, with no composition event of any kind and `isComposing` false
 * throughout:
 *
 *   insertText            "ㅇ"     value ""    -> "ㅇ"
 *   insertReplacementText "아"     value "ㅇ"  -> "아"
 *   insertReplacementText "안"     value "아"  -> "안"
 *   insertText            "ㄴ"     value "안"  -> "안ㄴ"
 *   insertReplacementText "녀"     value "안ㄴ" -> "안녀"
 *   insertReplacementText "녕"     value "안녀" -> "안녕"
 *
 * WKWebView refines a syllable by REPLACING the text it already inserted. Treat
 * a replacement as an insert and the terminal keeps only the two `insertText`
 * jamo, `ㅇ` and `ㄴ` — exactly the reported symptom.
 */
const TEXT_INSERTING_INPUT_TYPES = new Set([
  'insertText',
  'insertReplacementText',
])

/**
 * What the terminal sends to erase one character, matching the byte xterm
 * itself sends for Backspace. A replacement has to retract the text it
 * supersedes before the new text goes out.
 */
const ERASE_CHARACTER = '\x7F'

/** The legacy `keyCode` a browser reports for a keystroke an IME consumed. */
const KEY_CODE_IME_PROCESS = 229

export function attachTerminalIme(
  terminal: Terminal,
  send: (text: string) => void,
): TerminalImeHandle {
  const root = terminal.element
  const textarea = terminal.textarea
  if (!root || !textarea) {
    // Failing loudly beats attaching nothing: a silent no-op would leave CJK
    // input broken with no sign that the layer never came up.
    throw new Error('attachTerminalIme requires an opened terminal (call terminal.open first)')
  }
  // xterm builds `.xterm-helpers` as `position: absolute`, so it is the
  // containing block for both the helper textarea and the preview below. That
  // is what lets the preview borrow the textarea's own offsets verbatim.
  const helpers = textarea.parentElement ?? root

  let composing = false
  /**
   * The text the last `compositionend` committed, held only long enough to
   * recognize WebKit's synthetic echo of it. See `shouldBypassKeyEvent`.
   */
  let lastCommit = ''
  /**
   * The keystroke currently in flight, or null between keyup and the next
   * keydown. `onInput` uses it to decide whether an `insertText` belongs to
   * xterm or to us; see `xtermOwnsKeystroke`.
   */
  let activeKeydown: { keyLength: number, prevented: boolean, imeConsumed: boolean } | null = null

  /**
   * How many characters the next `input` event replaces, counted at
   * `beforeinput` time. It has to be read then: the selection still covers the
   * range about to be replaced, and by the `input` event it has collapsed.
   */
  let replacedByNextInput = 0

  /** Characters, not UTF-16 units, so a surrogate pair counts once. */
  const selectedCharacterCount = (): number => {
    const start = textarea.selectionStart ?? 0
    const end = textarea.selectionEnd ?? start
    if (end <= start)
      return 0
    return [...textarea.value.slice(start, end)].length
  }

  /**
   * Whether xterm's own key path produces the bytes for the keystroke in
   * flight, which means the `input` event that follows must be left alone.
   *
   * There are three shapes, and the middle one is easy to get wrong:
   *
   * - xterm handled the key in keydown. It calls `preventDefault()` whenever it
   *   turns a key into terminal input, and the browser then fires no `input`
   *   event at all.
   * - xterm DEFERRED the key to `keypress`. It does this for A-Z, deliberately
   *   and without preventing the keydown, to keep lower-case letters working
   *   under an input method while caps lock is on. The browser therefore does
   *   fire an `input` event, and treating that as unclaimed doubles every
   *   capital letter the user types.
   * - xterm produced nothing. Its printable check requires a single-character
   *   `key`, so a keydown reporting something longer that it did not prevent is
   *   one it cannot turn into input. WebKit reports exactly that for the key
   *   after a cancelled dead key (`key === "~/"`), and the character is lost
   *   today because xterm's own `_inputEvent` guard drops it too.
   *
   * With no keystroke in flight the text came from somewhere else entirely — a
   * candidate committed by mouse click — and is ours.
   *
   * A keystroke the input method consumed is never xterm's, whatever it looks
   * like: this module already held it back from xterm's key path. That case
   * needs its own arm because WKWebView delivers the keydown AFTER the input
   * event it produced, so a fast typist can leave the PREVIOUS keystroke still
   * recorded here when the next one's text arrives.
   */
  const xtermOwnsKeystroke = (): boolean => {
    if (activeKeydown === null || activeKeydown.imeConsumed)
      return false
    return activeKeydown.prevented || activeKeydown.keyLength === 1
  }

  const preview = document.createElement('div')
  preview.className = styles.compositionPreview
  // The composed text is already announced by the input method itself, and the
  // preview duplicates the textarea's own value, so hide it from assistive
  // technology rather than reading it out twice.
  preview.setAttribute('aria-hidden', 'true')
  preview.dataset.testid = 'terminal-composition-preview'
  helpers.appendChild(preview)

  // Arrow functions, not declarations: a hoisted `function` loses the narrowing
  // that the guard above established on `textarea`.
  const showPreview = (text: string): void => {
    preview.textContent = text
    // Borrow the textarea's box. xterm's `_syncTextArea()` parks it on the
    // cursor on every cursor move, and it keeps doing so because our
    // interception leaves xterm's `isComposing` false.
    preview.style.left = textarea.style.left
    preview.style.top = textarea.style.top
    preview.style.height = textarea.style.height
    preview.style.lineHeight = textarea.style.height
    const options = terminal.options
    preview.style.fontFamily = options.fontFamily ?? ''
    preview.style.fontSize = options.fontSize != null ? `${options.fontSize}px` : ''
    // Invert the terminal's own colors so the pending text reads as a marked
    // region rather than as text the shell already echoed.
    preview.style.backgroundColor = options.theme?.foreground ?? ''
    preview.style.color = options.theme?.background ?? ''
    preview.classList.add(styles.compositionPreviewActive)
  }

  const hidePreview = (): void => {
    preview.classList.remove(styles.compositionPreviewActive)
    preview.textContent = ''
  }

  const isCompositionInput = (event: InputEvent): boolean =>
    composing
    || event.isComposing
    || (event.inputType != null && COMPOSITION_INPUT_TYPES.has(event.inputType))

  const onCompositionStart = (event: Event): void => {
    event.stopPropagation()
    composing = true
    lastCommit = ''
    showPreview('')
  }

  const onCompositionUpdate = (event: Event): void => {
    event.stopPropagation()
    showPreview((event as CompositionEvent).data ?? '')
  }

  const onCompositionEnd = (event: Event): void => {
    event.stopPropagation()
    composing = false
    hidePreview()
    const data = (event as CompositionEvent).data ?? ''
    // An empty commit is a cancelled composition (Escape, or a candidate window
    // dismissed). Sending it would put a stray empty write on the wire.
    if (data.length > 0) {
      lastCommit = data
      send(data)
    }
    // The textarea's value is deliberately left alone. Nothing here reads it,
    // and xterm already clears it on Enter, on Ctrl+C, and on blur, which bounds
    // its growth exactly as it does today. Clearing it inside this handler would
    // reach into the editor while the browser is still finalizing the commit.
  }

  const onBeforeInput = (event: Event): void => {
    const inputEvent = event as InputEvent
    const composed = isCompositionInput(inputEvent)
    // Measure the range this event is about to replace while the selection
    // still covers it; by the `input` event it has collapsed. Written on EVERY
    // beforeinput, composition included, so a measurement can never outlive the
    // event it was taken for and apply itself to a later one.
    replacedByNextInput = composed ? 0 : selectedCharacterCount()
    // A browser dispatches beforeinput and the input event it precedes in one
    // task, so a measurement that survives into the next one belongs to an edit
    // that never happened — a cancelled beforeinput, or an engine that skipped
    // it. Dropping it there stops those erase bytes from landing on an
    // unrelated input event and eating text the user meant to keep.
    queueMicrotask(() => {
      replacedByNextInput = 0
    })
    if (composed)
      event.stopPropagation()
  }

  const onInput = (event: Event): void => {
    const inputEvent = event as InputEvent
    if (isCompositionInput(inputEvent)) {
      event.stopPropagation()
      return
    }
    const replaced = replacedByNextInput
    replacedByNextInput = 0

    // Text that arrived without xterm's key path producing it. xterm drops all
    // of it — its `_inputEvent` bails whenever a keydown is in flight — so it
    // is ours to send.
    //
    // The ownership question only arises for `insertText`, the one type xterm
    // looks at. A replacement is always ours: xterm has no branch for it, so
    // deferring one to xterm drops it on the floor.
    const claimable = inputEvent.inputType != null
      && TEXT_INSERTING_INPUT_TYPES.has(inputEvent.inputType)
      && (inputEvent.inputType !== 'insertText' || !xtermOwnsKeystroke())
    if (claimable && inputEvent.data) {
      // Retract what this event supersedes before the new text goes out, so a
      // WKWebView replacement lands on the PTY as the edit it actually is.
      // Both halves go out in ONE send: the terminal's input queue must never
      // interleave another keystroke between the erase and its replacement.
      const payload = ERASE_CHARACTER.repeat(replaced) + inputEvent.data
      event.stopPropagation()
      send(payload)
    }
  }

  const onKeyDownAfterXterm = (event: Event): void => {
    const keyEvent = event as KeyboardEvent
    activeKeydown = {
      keyLength: keyEvent.key?.length ?? 0,
      prevented: keyEvent.defaultPrevented,
      imeConsumed: keyEvent.isComposing || keyEvent.keyCode === KEY_CODE_IME_PROCESS,
    }
    // A fresh keystroke ends the window in which an echoed keypress can appear.
    // In the WebKit trace the keydown comes BEFORE the compositionend, so this
    // never clears the commit it has to recognize; what it does rule out is
    // swallowing a genuine capital typed some time after a composition that
    // happened to contain the same character.
    lastCommit = ''
  }

  const onKeystrokeEnd = (): void => {
    activeKeydown = null
  }

  root.addEventListener('compositionstart', onCompositionStart, true)
  root.addEventListener('compositionupdate', onCompositionUpdate, true)
  root.addEventListener('compositionend', onCompositionEnd, true)
  root.addEventListener('beforeinput', onBeforeInput, true)
  root.addEventListener('input', onInput, true)
  // On the textarea, in the CAPTURE phase, registered after xterm's own keydown
  // listener so it runs immediately after it and reads the `preventDefault()`
  // that xterm just left behind. Both details are load-bearing. xterm handles a
  // key by calling `cancel()`, which is `preventDefault()` plus
  // `stopPropagation()`: a listener on an ancestor never runs at all, and a
  // non-capture listener even on this same textarea is skipped too, because the
  // target's bubble-phase dispatch is a separate pass that the stop-propagation
  // flag suppresses. Same phase, same target, later registration is the one
  // position that always runs.
  textarea.addEventListener('keydown', onKeyDownAfterXterm, true)
  // Close the keystroke out. Blur matters as well as keyup: focus can leave
  // between the two, and a keydown left standing would make the next
  // mouse-committed candidate look like it belonged to xterm.
  textarea.addEventListener('keyup', onKeystrokeEnd, true)
  textarea.addEventListener('blur', onKeystrokeEnd, true)

  return {
    get composing() {
      return composing
    },

    shouldBypassKeyEvent(event: KeyboardEvent): boolean {
      if (event.type === 'keydown') {
        // Hand every IME-consumed keystroke to the composition handlers above.
        // This also keeps xterm's `_handleAnyTextareaChanges` from firing, which
        // is what leaks a half-formed jamo onto the wire.
        return event.isComposing || event.keyCode === KEY_CODE_IME_PROCESS
      }
      if (event.type === 'keypress') {
        // A bypassed keydown leaves xterm's `_keyDownHandled` false, so
        // `_keyPress` would happily send the raw Latin key the IME consumed.
        if (composing)
          return true
        // WebKit follows a commit with a synthetic keypress whose charCode is
        // the character just committed (xtermjs/xterm.js#5894). `compositionend`
        // already sent it. Suppress the echo once, then stop looking, so a
        // genuine keystroke of the same character is never swallowed.
        if (lastCommit.length > 0 && event.charCode > 0
          && lastCommit.includes(String.fromCharCode(event.charCode))) {
          lastCommit = ''
          return true
        }
      }
      return false
    },

    dispose() {
      root.removeEventListener('compositionstart', onCompositionStart, true)
      root.removeEventListener('compositionupdate', onCompositionUpdate, true)
      root.removeEventListener('compositionend', onCompositionEnd, true)
      root.removeEventListener('beforeinput', onBeforeInput, true)
      root.removeEventListener('input', onInput, true)
      textarea.removeEventListener('keydown', onKeyDownAfterXterm, true)
      textarea.removeEventListener('keyup', onKeystrokeEnd, true)
      textarea.removeEventListener('blur', onKeystrokeEnd, true)
      preview.remove()
    },
  }
}
