import type { Terminal } from '@xterm/xterm'
import { ERASE_CHARACTER } from './terminal'
import * as styles from './terminalIme.css'

/**
 * LeapMux owns the terminal's text-input path for input methods (IME).
 *
 * xterm never reads `compositionend.data`. It rebuilds the committed text by
 * diffing substrings of its hidden helper textarea across `setTimeout(0)`
 * boundaries (`CompositionHelper._finalizeComposition`), which assumes an event
 * order and a textarea value that only Chromium actually provides. Measured:
 * Chromium composes `안녕` correctly, WebKit does not. WebKit follows each
 * commit with a synthetic `keypress` that carries the COMMITTED character, so
 * xterm emits it a second time from `_keyPress`, and the keystroke after the
 * commit is then dropped by the `_keyDownSeen` guard in `_inputEvent`
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
  /**
   * Whether xterm must not process this key event. Pass every event that
   * `attachCustomKeyEventHandler` receives; return `false` from that handler
   * when this returns true.
   */
  shouldBypassKeyEvent: (event: KeyboardEvent) => boolean
  dispose: () => void
}

/**
 * `inputType` values a browser reports while it edits composed text. Every
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

/** The legacy `keyCode` a browser reports for a keystroke an IME consumed. */
const KEY_CODE_IME_PROCESS = 229

/**
 * The in-progress composition preview: the text the user is currently
 * composing, painted over the cell the cursor sits on.
 *
 * Only layout lives in the stylesheet. The font and the colors are written as
 * inline styles on every `show`, because they must follow the live xterm
 * options and the helper textarea's box, both of which can change while a
 * composition is open (the cursor moves as output arrives; the user can
 * change the theme from the settings panel).
 */
function createCompositionPreview(
  helpers: HTMLElement,
  textarea: HTMLTextAreaElement,
  terminal: Terminal,
): { show: (text: string) => void, hide: () => void, dispose: () => void } {
  const preview = document.createElement('div')
  preview.className = styles.compositionPreview
  // The input method itself already announces the composed text, and the
  // preview duplicates the textarea's own value, so hide it from assistive
  // technology rather than reading it out twice.
  preview.setAttribute('aria-hidden', 'true')
  preview.dataset.testid = 'terminal-composition-preview'
  helpers.appendChild(preview)

  const show = (text: string): void => {
    preview.textContent = text
    // Borrow the textarea's box. xterm's `_syncTextArea()` parks it on the
    // cursor on every cursor move, and it keeps doing so because the
    // interception in this module leaves xterm's `isComposing` false.
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

  const hide = (): void => {
    preview.classList.remove(styles.compositionPreviewActive)
    preview.textContent = ''
  }

  return {
    show,
    hide,
    dispose: () => preview.remove(),
  }
}

/**
 * The keystroke-ownership tracker: what xterm's own key path emitted for the
 * keystroke in flight, and whether a given inserted text is that emission's
 * echo. Extracted from `attachTerminalIme` (as `createCompositionPreview`
 * was) so the emission machine — record, consume, expire — has one boundary
 * a test can target.
 *
 * `onPlainKeydown` fires for every keystroke the input method did NOT
 * consume. The caller owns the echo-suppression window and closes it there:
 * a genuine capital typed some time after a composition that contained the
 * same character must survive.
 */
function createKeystrokeOwnership(
  terminal: Terminal,
  onPlainKeydown: () => void,
): {
  /** Bind on the textarea, capture phase, registered after xterm's own keydown listener. */
  onKeyDownAfterXterm: (event: Event) => void
  /** Bind on the textarea, capture phase, on keyup and blur. */
  onKeystrokeEnd: () => void
  /**
   * Whether xterm's own key path already produced the bytes for this text —
   * and if it did, consume the emission that proves it, so one emission can
   * never answer for two input events.
   *
   * The decision reads the OBSERVED emissions — what xterm's `onData` sent
   * for the keystroke in flight, one entry per emission — and matches
   * EXACTLY: a genuine echo inserts exactly the text one emission carried, so
   * 'C' is an echo only when an emission was 'C', never because an arrow's
   * '\x1b[C' merely contains it. Caps-lock case folding needs no arm of its
   * own, because the emission carries the character xterm actually sent;
   * deferred A–Z rollover (two capitals' keypresses before either input
   * event) works because each keypress contributes its own entry.
   *
   * - xterm handled the key, in keydown or in the `keypress` it defers A-Z
   *   to, and sent the bytes: the matching emission is xterm's echo, and the
   *   input event that follows must be left alone.
   * - No emission matches: a dead key's precomposed character, a jamo the
   *   input method inserted while a previous keystroke is still in flight, a
   *   candidate committed from a popup, a Latin character inside a control
   *   sequence an arrow key emitted, or a deferred key whose keypress never
   *   ran. The text is ours, and xterm's own `_inputEvent` guard would have
   *   dropped it.
   *
   * A keystroke the input method consumed is never xterm's by construction —
   * the caller bypassed xterm's key path for it — and WKWebView's reversed
   * order (keydown AFTER the input event) needs no arm either: the stale
   * keystroke's emission simply does not match the new text.
   *
   * One shape stays ambiguous for any observer: an input method that commits
   * EXACTLY the text xterm emitted for a keystroke still in flight. No
   * recorded engine trace shows it, and distinguishing it from the genuine
   * echo of that keystroke is impossible from the DOM alone.
   */
  consumeXtermEcho: (text: string) => boolean
  dispose: () => void
} {
  /**
   * The keystroke currently in flight, or null between keyup and the next
   * keydown. `consumeXtermEcho` reads it: an input method that consumed the
   * keystroke never left xterm's key path an emission to answer for.
   */
  let activeKeydown: { imeConsumed: boolean } | null = null

  /**
   * What xterm's own key path actually sent (`onData`) since the keystroke in
   * flight began, one entry per emission. Cleared when the keystroke ends
   * (keyup, blur), so an emission can never be attributed to a later text
   * insertion, and consumed by the echo it answers.
   */
  const emissionsInFlight: string[] = []

  const consumeXtermEcho = (text: string): boolean => {
    if (activeKeydown === null || activeKeydown.imeConsumed)
      return false
    const match = emissionsInFlight.indexOf(text)
    if (match === -1)
      return false
    emissionsInFlight.splice(match, 1)
    return true
  }

  // Record what xterm actually emits for the keystroke in flight. `onData`
  // fires synchronously inside xterm's keydown/keypress handlers, before the
  // browser dispatches the input event of the same keystroke — which is what
  // makes the observed emission a sound basis for the decision above.
  const dataSubscription = terminal.onData((data) => {
    emissionsInFlight.push(data)
  })

  const onKeyDownAfterXterm = (event: Event): void => {
    const keyEvent = event as KeyboardEvent
    activeKeydown = {
      imeConsumed: keyEvent.isComposing || keyEvent.keyCode === KEY_CODE_IME_PROCESS,
    }
    // A keystroke the input method consumed keeps the echo window open,
    // because WKWebView delivers that keydown AFTER the compositionend it
    // belongs to, and the echo that follows must still be recognized.
    if (!activeKeydown.imeConsumed)
      onPlainKeydown()
  }

  const onKeystrokeEnd = (): void => {
    activeKeydown = null
    // The emissions belong to the keystroke that produced them; once the
    // keystroke ends, no later text insertion may claim to be its echo.
    emissionsInFlight.length = 0
  }

  return {
    onKeyDownAfterXterm,
    onKeystrokeEnd,
    consumeXtermEcho,
    dispose: () => dataSubscription.dispose(),
  }
}

export function attachTerminalIme(terminal: Terminal, send: (text: string) => void): TerminalImeHandle {
  const root = terminal.element
  const textarea = terminal.textarea
  if (!root || !textarea) {
    // Failing loudly beats attaching nothing: a silent no-op would leave CJK
    // input broken with no sign that the layer never came up.
    throw new Error('attachTerminalIme requires an opened terminal (call terminal.open first)')
  }
  // xterm builds `.xterm-helpers` as `position: absolute`, so it is the
  // containing block for both the helper textarea and the preview below. That
  // is what lets the preview borrow the textarea's own offsets verbatim. No
  // fallback: a detached parent means the DOM shape changed, and a silently
  // re-parented preview would position against the wrong containing block —
  // fail loudly, like the guard above.
  const helpers = textarea.parentElement
  if (!helpers) {
    throw new Error('attachTerminalIme requires the terminal textarea to be attached (xterm builds .xterm-helpers in open())')
  }

  let composing = false
  /**
   * The text the last `compositionend` committed, held only long enough to
   * recognize WebKit's synthetic echo of it. See `shouldBypassKeyEvent`.
   */
  let lastCommit = ''

  /**
   * The edit that the next `input` event applies, measured at `beforeinput`
   * time and PAIRED to the event it was measured for: how many characters the
   * event replaces, under the `inputType` and `data` of the `beforeinput` it
   * belongs to. `onInput` consumes it only when the event that arrives matches
   * the pair, so a measurement can never apply itself to a later, unrelated
   * event. The range has to be measured at `beforeinput` time because the
   * selection still covers it then; by the `input` event it collapses.
   */
  let pendingEdit: { replaced: number, inputType: string, data: string | null } | null = null

  /** Characters, not UTF-16 units, so a surrogate pair counts once. */
  const codePointCount = (text: string): number => [...text].length

  const selectedCharacterCount = (): number => {
    const start = textarea.selectionStart ?? 0
    const end = textarea.selectionEnd ?? start
    if (end <= start)
      return 0
    return codePointCount(textarea.value.slice(start, end))
  }

  /**
   * How many characters the edit behind this `beforeinput` replaces.
   *
   * `event.getTargetRanges()` is the engine's own statement of the replaced
   * ranges, and it stays correct when the live selection has already collapsed
   * or never covered the range. For a textarea the ranges' containers are the
   * element itself and the offsets index its value. An engine that supplies no
   * ranges, or a shape this module cannot interpret, falls back to the live
   * selection, which still covers the replaced range in every recorded trace.
   */
  const replacedCharacterCount = (event: InputEvent): number => {
    const ranges = typeof event.getTargetRanges === 'function' ? event.getTargetRanges() : []
    if (ranges.length === 0)
      return selectedCharacterCount()
    let total = 0
    for (const range of ranges) {
      if (range.startContainer !== textarea || range.endContainer !== textarea)
        return selectedCharacterCount()
      total += codePointCount(textarea.value.slice(range.startOffset, range.endOffset))
    }
    return total
  }

  const preview = createCompositionPreview(helpers, textarea, terminal)

  // The keystroke-ownership machine (what xterm emitted for the keystroke in
  // flight, and which inserted text is that emission's echo). The plain-keydown
  // callback closes the echo-suppression window below: a keystroke the input
  // method did not consume ends the window in which WebKit's synthetic echo of
  // a previous commit can still appear.
  const ownership = createKeystrokeOwnership(terminal, () => {
    lastCommit = ''
  })

  const isCompositionInput = (event: InputEvent): boolean =>
    composing
    || event.isComposing
    || (event.inputType != null && COMPOSITION_INPUT_TYPES.has(event.inputType))

  const onCompositionStart = (event: Event): void => {
    event.stopPropagation()
    composing = true
    lastCommit = ''
    preview.show('')
  }

  const onCompositionUpdate = (event: Event): void => {
    event.stopPropagation()
    preview.show((event as CompositionEvent).data ?? '')
  }

  const onCompositionEnd = (event: Event): void => {
    event.stopPropagation()
    composing = false
    preview.hide()
    const data = (event as CompositionEvent).data ?? ''
    // An empty commit is a cancelled composition (Escape, or a candidate window
    // dismissed). Sending it would put a stray empty write on the wire.
    if (data.length > 0) {
      lastCommit = data
      send(data)
    }
    // This module deliberately leaves the textarea's value alone. Nothing here
    // reads it for the commit path, and xterm already clears it on Enter, on
    // Ctrl+C, and on blur, which limits its growth exactly as it does today.
    // Clearing it inside this handler would reach into the editor while the
    // browser still finalizes the commit.
  }

  const onBeforeInput = (event: Event): void => {
    const inputEvent = event as InputEvent
    const composed = isCompositionInput(inputEvent)
    // Measure the range this event is about to replace while the selection
    // still covers it; by the `input` event it collapses. The measurement is
    // PAIRED to the event it was measured for, and `onInput` consumes it only
    // when the event that arrives matches the pair. A timing-based reset
    // cannot replace the pairing: a real browser runs a microtask checkpoint
    // after the `beforeinput` listener returns — the JS stack is empty between
    // the two dispatches — so a microtask reset would clear the measurement
    // before its own `input` event reads it, and the erase half of every
    // replacement would silently never fire outside jsdom.
    if (composed) {
      pendingEdit = null
      event.stopPropagation()
    }
    else {
      pendingEdit = {
        replaced: replacedCharacterCount(inputEvent),
        inputType: inputEvent.inputType ?? '',
        data: inputEvent.data,
      }
    }
  }

  const onInput = (event: Event): void => {
    const inputEvent = event as InputEvent
    if (isCompositionInput(inputEvent)) {
      event.stopPropagation()
      return
    }
    // Consume the measurement only when this event is the one it was measured
    // for. A `beforeinput` whose `input` event never arrived — something
    // cancelled it, or an engine skipped it — leaves its pair behind, and the
    // next `beforeinput` overwrites it; either way, a stray erase count cannot
    // land on an unrelated event and eat text the user meant to keep.
    const replaced = pendingEdit !== null
      && pendingEdit.inputType === inputEvent.inputType
      && pendingEdit.data === inputEvent.data
      ? pendingEdit.replaced
      : 0
    pendingEdit = null

    // Text that arrived without xterm's key path producing it. xterm drops all
    // of it — its `_inputEvent` bails whenever a keydown is in flight — so it
    // is ours to send.
    //
    // The ownership question only arises for `insertText`, the one type xterm
    // looks at. A replacement is always ours: xterm has no branch for it, so
    // deferring one to xterm discards it.
    const claimable = inputEvent.inputType != null
      && TEXT_INSERTING_INPUT_TYPES.has(inputEvent.inputType)
      && (inputEvent.inputType !== 'insertText' || !ownership.consumeXtermEcho(inputEvent.data ?? ''))
    if (claimable && inputEvent.data) {
      // Retract what this event supersedes before the new text goes out, so a
      // WKWebView replacement lands on the PTY as the edit it actually is.
      // Both halves go out in ONE send: the terminal's input queue must never
      // interleave another keystroke between the erase and its replacement.
      //
      // The count comes from the engine's textarea, which mirrors what the
      // engine itself inserted. Line edits that go through xterm's key path
      // (Ctrl+W, Ctrl+U) change the shell's line without changing the
      // textarea, so a replacement after such an edit can erase more than the
      // shell has at the cursor. No frontend layer can track the shell's true
      // line state — completion and history redraw it from the PTY side — so
      // the engine's own replaced range is the best available source.
      const payload = ERASE_CHARACTER.repeat(replaced) + inputEvent.data
      event.stopPropagation()
      send(payload)
    }
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
  // `stopPropagation()`. A listener on an ancestor therefore never runs at all.
  // A non-capture listener on this same textarea is skipped too: the target's
  // bubble-phase dispatch is a separate pass, and the stop-propagation flag
  // suppresses it. Same phase, same target, later registration is the one
  // position that always runs.
  textarea.addEventListener('keydown', ownership.onKeyDownAfterXterm, true)
  // Close the keystroke out — its keydown and its emission expire together.
  // Blur matters as well as keyup: focus can leave between the two, and a
  // keystroke left standing would make the next mouse-committed candidate
  // look like it belonged to xterm.
  textarea.addEventListener('keyup', ownership.onKeystrokeEnd, true)
  textarea.addEventListener('blur', ownership.onKeystrokeEnd, true)

  /**
   * WebKit follows a commit with a synthetic keypress whose charCode is a
   * character just committed (xtermjs/xterm.js#5894); `compositionend`
   * already sent it. Consume one occurrence of the character per echo, so a
   * multi-character commit that the engine echoes as one keypress per
   * character suppresses every echo. A genuine keystroke of the same
   * character is never swallowed: it is always preceded by its own keydown,
   * and that clears the window (see `onKeyDownAfterXterm`).
   */
  const consumeEchoKeypress = (charCode: number): boolean => {
    const ch = String.fromCharCode(charCode)
    if (!lastCommit.includes(ch))
      return false
    lastCommit = lastCommit.replace(ch, '')
    return true
  }

  /**
   * A keypress whose charCode is half of a surrogate pair. No well-formed
   * keystroke carries one — an astral character needs BOTH halves — and a
   * lone surrogate cannot be encoded as UTF-8: the PTY would see U+FFFD. An
   * engine whose astral echo count diverges from the per-unit bookkeeping
   * above (a repeated or masked half) delivers exactly that, so suppress it
   * unconditionally.
   */
  const isLoneSurrogate = (charCode: number): boolean =>
    charCode >= 0xD800 && charCode <= 0xDFFF

  return {
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
        // Consumption first keeps `lastCommit` exact where the engine is
        // exact; the surrogate guard is the net under divergence.
        if (event.charCode > 0 && (consumeEchoKeypress(event.charCode) || isLoneSurrogate(event.charCode)))
          return true
      }
      return false
    },

    dispose() {
      root.removeEventListener('compositionstart', onCompositionStart, true)
      root.removeEventListener('compositionupdate', onCompositionUpdate, true)
      root.removeEventListener('compositionend', onCompositionEnd, true)
      root.removeEventListener('beforeinput', onBeforeInput, true)
      root.removeEventListener('input', onInput, true)
      textarea.removeEventListener('keydown', ownership.onKeyDownAfterXterm, true)
      textarea.removeEventListener('keyup', ownership.onKeystrokeEnd, true)
      textarea.removeEventListener('blur', ownership.onKeystrokeEnd, true)
      ownership.dispose()
      preview.dispose()
    },
  }
}
