import { render, waitFor } from '@solidjs/testing-library'
import { createSignal, Show } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { flushStorageWrites } from '~/lib/browserStorage'
import { clearDraft, loadDraft, saveDraft } from '~/lib/editor/draftPersistence'
import { useTestStorage } from '~/test-support/persistentStorage'
import { MarkdownEditor } from './MarkdownEditor'

// A HOLD-OPEN SEAM for one draft read.
//
// The swap race this file pins needs a read that is still in flight when the
// next key arrives, and fake-indexeddb answers inside a macrotask -- fast enough
// that every unaided attempt to interleave the two lands after the swap already
// applied. Only `loadDraft` is held open, and only for the key the case names;
// `saveDraft` fails for one key on demand (see `failingSaveKey`). Everything else
// is the real module, so the assertions still read real rows.
let gatedKey: string | null = null
let gateResolvers: Array<() => void> = []
let gateStarted = false

// A WRITE SEAM for one draft key. `saveDraft` throws for the key that the case
// names, as storage does when a key is not registered. Every other key writes for real.
let failingSaveKey: string | null = null

vi.mock('~/lib/editor/draftPersistence', async (importActual) => {
  const actual = await importActual<typeof import('~/lib/editor/draftPersistence')>()
  return {
    ...actual,
    loadDraft: async (agentId: string) => {
      if (agentId === gatedKey) {
        gateStarted = true
        // One resolver per call: a second reader of the same key must not
        // orphan the first one's promise.
        await new Promise<void>((resolve) => {
          gateResolvers.push(resolve)
        })
      }
      return actual.loadDraft(agentId)
    },
    saveDraft: (...args: Parameters<typeof actual.saveDraft>) => {
      if (args[0] === failingSaveKey)
        throw new Error('the storage rejected the draft')
      return actual.saveDraft(...args)
    },
  }
})

function gateDraftRead(key: string): void {
  gatedKey = key
  gateStarted = false
  gateResolvers = []
}

function gatedReadStarted(): boolean {
  return gateStarted
}

function releaseGatedDraft(): void {
  gatedKey = null
  for (const resolve of gateResolvers)
    resolve()
  gateResolvers = []
}

// A draft is on the ASYNCHRONOUS storage tier, which has no in-memory mirror, so
// a round trip here needs a database to round-trip through. Without one the
// write never leaves the write-behind queue and `loadDraft` answers from that
// queue -- so a case would pass while covering no persistence at all.
useTestStorage()

const DRAFT_KEY = 'markdown-editor-send'

afterEach(() => {
  clearDraft(DRAFT_KEY)
  vi.restoreAllMocks()
})

/**
 * `handleSend` awaits the send, and every call site discards the promise that
 * it answers with -- a ProseMirror key handler and two buttons cannot await it.
 * A throw after the await would therefore leave the browser with an unhandled
 * rejection instead of a message.
 */
describe('MarkdownEditor send', () => {
  it('retains a hidden input draft and prevents focus and submission until the input returns', async () => {
    const [hidden, setHidden] = createSignal(true)
    const onSend = vi.fn()
    let send: (() => void | Promise<void>) | undefined
    let focus: (() => void) | undefined
    let insert: ((text: string) => void) | undefined
    saveDraft(DRAFT_KEY, 'saved message', -1)
    const { getByTestId } = render(() => (
      <PreferencesProvider>
        <MarkdownEditor
          surface="chat"
          draftKey={{ key: DRAFT_KEY }}
          hideInput={hidden()}
          onSend={onSend}
          imperative={{ sendRef: fn => send = fn, focusRef: fn => focus = fn, insertRef: fn => insert = fn }}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(send).toBeTypeOf('function'))
    const input = getByTestId('composer-editor')
    const document = input.querySelector('.ProseMirror') as HTMLElement
    await waitFor(() => expect(document).toHaveTextContent('saved message'))
    const focusInput = vi.spyOn(document, 'focus')
    expect(input).toHaveAttribute('hidden')
    focus?.()
    insert?.('hidden insertion')
    await send?.()
    expect(focusInput).not.toHaveBeenCalled()
    expect(onSend).not.toHaveBeenCalled()
    expect((await loadDraft(DRAFT_KEY)).content).toBe('saved message')
    setHidden(false)
    expect(input).not.toHaveAttribute('hidden')
    await send?.()
    expect(onSend).toHaveBeenCalledWith('saved message')
  })

  it('reports a failure of the post-send reset rather than rejecting', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const resetFailure = new Error('reset failed')
    let send: (() => void | Promise<void>) | undefined
    saveDraft(DRAFT_KEY, 'hello worker', -1)
    render(() => (
      <PreferencesProvider>
        <MarkdownEditor
          surface="chat"
          draftKey={{ key: DRAFT_KEY }}
          onSend={() => {}}
          onAfterSend={() => { throw resetFailure }}
          imperative={{ sendRef: (fn) => { send = fn } }}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(send).toBeTypeOf('function'))
    await waitFor(() => expect(document.querySelector('.ProseMirror')).toHaveTextContent('hello worker'))

    await expect(send?.()).resolves.toBeUndefined()

    // The steps before the throw still ran, so the composer is empty.
    expect((await loadDraft(DRAFT_KEY)).content).toBe('')
    expect(warn).toHaveBeenCalledWith('[MarkdownEditor]', 'Failed to reset the composer after a send:', resetFailure)
  })

  // The send resolves after an await, and only the user can move the caret
  // while that request runs. A send that takes the caret back raises the
  // on-screen keyboard over the transcript the reader just uncovered.
  it('leaves the caret where the user moved it during an async send', async () => {
    let send: (() => void | Promise<void>) | undefined
    saveDraft(DRAFT_KEY, 'hello worker', -1)
    const outside = document.createElement('button')
    document.body.append(outside)
    render(() => (
      <PreferencesProvider>
        <MarkdownEditor
          surface="chat"
          draftKey={{ key: DRAFT_KEY }}
          onSend={() => Promise.resolve().then(() => { outside.focus() })}
          imperative={{ sendRef: (fn) => { send = fn } }}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(send).toBeTypeOf('function'))
    const view = document.querySelector('.ProseMirror') as HTMLElement
    await waitFor(() => expect(view).toHaveTextContent('hello worker'))
    const refocus = vi.spyOn(view, 'focus')
    view.focus()
    refocus.mockClear()

    await send?.()

    expect(refocus).not.toHaveBeenCalled()
    expect(document.activeElement).toBe(outside)
    outside.remove()
  })
})

// The composer takes the keyboard when it finishes building. That moment sits
// behind an AWAITED draft read, so it can land while something else already
// owns the keyboard -- the tab-rename input in the strip above is the case that
// pushed this in. `suppressAutoFocus` is how the shell says so.
//
// `imperative.onReady` fires AFTER `applyEditorState`, so it is the signal that
// makes the negative case a real assertion rather than a race the test happens
// to win: at that point an unsuppressed editor already holds the keyboard, and
// the first case proves it.
describe('MarkdownEditor autofocus', () => {
  const FOCUS_KEY = 'markdown-editor-focus'

  afterEach(() => {
    clearDraft(FOCUS_KEY)
  })

  /** Render the composer with the keyboard already elsewhere. */
  function renderWithFocusElsewhere(suppressAutoFocus?: () => boolean) {
    const elsewhere = document.createElement('input')
    document.body.append(elsewhere)
    elsewhere.focus()
    let ready = false
    render(() => (
      <PreferencesProvider>
        <MarkdownEditor
          surface="chat"
          draftKey={{ key: FOCUS_KEY }}
          onSend={() => {}}
          {...(suppressAutoFocus === undefined ? {} : { suppressAutoFocus })}
          imperative={{ onReady: () => { ready = true } }}
        />
      </PreferencesProvider>
    ))
    return { elsewhere, ready: () => ready }
  }

  it('takes the keyboard once the editor is built', async () => {
    const { elsewhere, ready } = renderWithFocusElsewhere()
    await waitFor(() => expect(ready()).toBe(true))

    expect(document.activeElement).toBe(document.querySelector('.ProseMirror'))
    elsewhere.remove()
  })

  it('leaves the keyboard alone while the shell suppresses the autofocus', async () => {
    const { elsewhere, ready } = renderWithFocusElsewhere(() => true)
    await waitFor(() => expect(ready()).toBe(true))

    expect(document.activeElement).toBe(elsewhere)
    // Only the FOCUS is in question. The editor is still made editable, so a
    // composer the user then clicks into types normally.
    expect(document.querySelector('.ProseMirror')?.getAttribute('contenteditable')).toBe('true')
    elsewhere.remove()
  })
})

// The draft read is ASYNCHRONOUS, so between a key change and the replace it
// answers there is a window in which the editor still holds the OUTGOING key's
// prose. `prevDraftKey` names the key whose document is on screen, so it may
// only move when a replace actually lands -- and the save that every swap runs
// first reads it.
describe('MarkdownEditor draft key swaps', () => {
  const KEY_A = 'swap-key-a'
  const KEY_B = 'swap-key-b'
  const KEY_C = 'swap-key-c'

  afterEach(() => {
    clearDraft(KEY_A)
    clearDraft(KEY_B)
    clearDraft(KEY_C)
    releaseGatedDraft()
  })

  it('does not save the outgoing document under a key whose read never landed', async () => {
    saveDraft(KEY_A, 'the A document', -1)
    saveDraft(KEY_B, 'the B document', -1)
    await flushStorageWrites()

    // B's read is HELD OPEN. fake-indexeddb answers within a macrotask, so
    // without a gate the swap always completes and the window this case is about
    // never opens.
    gateDraftRead(KEY_B)

    const [key, setKey] = createSignal(KEY_A)
    let send: (() => void | Promise<void>) | undefined
    // Scoped to THIS render's container: the cases above leave their own
    // `.ProseMirror` in the document, and a bare `querySelector` finds the first.
    const { container } = render(() => (
      <PreferencesProvider>
        <MarkdownEditor
          surface="chat"
          draftKey={{ key: key() }}
          onSend={() => {}}
          imperative={{ sendRef: (fn) => { send = fn } }}
        />
      </PreferencesProvider>
    ))
    const view = () => container.querySelector('.ProseMirror')
    // `sendRef` is handed over at the END of `onMount`, so this is what says the
    // mount is finished. Waiting only for the text would start the race while
    // `onMount` is still deciding the ready key, and the swap effect would be
    // racing the mount rather than a second swap.
    await waitFor(() => expect(send).toBeTypeOf('function'))
    await waitFor(() => expect(view()).toHaveTextContent('the A document'))

    // B's swap starts and blocks on the gate, so the editor still holds A's
    // prose when C arrives -- and it is that document the save at the top of the
    // effect writes, under whichever key `prevDraftKey` names.
    setKey(KEY_B)
    await waitFor(() => expect(gatedReadStarted()).toBe(true))
    setKey(KEY_C)
    await waitFor(() => expect(view()?.textContent).toBe(''))
    releaseGatedDraft()
    await flushStorageWrites()

    // B's saved draft must be untouched. With the pointer moved up front, C's
    // swap saved the document on screen -- A's prose -- under B, destroying a
    // draft the user never even opened.
    expect((await loadDraft(KEY_B)).content).toBe('the B document')
    expect((await loadDraft(KEY_A)).content).toBe('the A document')
  })

  // The unmount save is the third writer of a draft, after the debounced save and
  // the swap save. It follows the same rule: it writes the document on screen under
  // the key that owns that document.
  it('saves the document on screen under its own key when the editor unmounts', async () => {
    saveDraft(KEY_A, 'the A document', -1)
    await flushStorageWrites()

    let send: (() => void | Promise<void>) | undefined
    let type: ((text: string) => void) | undefined
    const { container, unmount } = render(() => (
      <PreferencesProvider>
        <MarkdownEditor
          surface="chat"
          draftKey={{ key: KEY_A }}
          onSend={() => {}}
          imperative={{
            sendRef: (fn) => { send = fn },
            insertRef: (fn) => { type = fn },
          }}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(send).toBeTypeOf('function'))
    await waitFor(() => expect(type).toBeTypeOf('function'))
    await waitFor(() => expect(container.querySelector('.ProseMirror')).toHaveTextContent('the A document'))

    // Typed text is still inside the debounce when the editor unmounts.
    type?.(' and more')
    unmount()
    await flushStorageWrites()

    expect((await loadDraft(KEY_A)).content).toBe('the A document and more')
  })

  // The same mechanism through the unmount: the key moves while a read is open, so
  // the editor still shows the OUTGOING key's prose, and the host unmounts before
  // the read lands. The prop already names the incoming key, but the document on
  // screen belongs to the outgoing key.
  it('does not save the outgoing document under the key of a read that never landed when the editor unmounts', async () => {
    saveDraft(KEY_A, 'the A document', -1)
    saveDraft(KEY_B, 'the B document', -1)
    await flushStorageWrites()
    gateDraftRead(KEY_B)

    const [key, setKey] = createSignal(KEY_A)
    let send: (() => void | Promise<void>) | undefined
    const { container, unmount } = render(() => (
      <PreferencesProvider>
        <MarkdownEditor
          surface="chat"
          draftKey={{ key: key() }}
          onSend={() => {}}
          imperative={{ sendRef: (fn) => { send = fn } }}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(send).toBeTypeOf('function'))
    await waitFor(() => expect(container.querySelector('.ProseMirror')).toHaveTextContent('the A document'))

    setKey(KEY_B)
    await waitFor(() => expect(gatedReadStarted()).toBe(true))
    unmount()
    releaseGatedDraft()
    await flushStorageWrites()

    // B's saved draft must be untouched. The unmount save wrote the document on
    // screen, A's prose, under the key that the prop had already moved to.
    expect((await loadDraft(KEY_B)).content).toBe('the B document')
    expect((await loadDraft(KEY_A)).content).toBe('the A document')
  })
})

// A draft is saved by a chain of two debounces: Milkdown reports the document
// 200ms after the last edit, and the editor writes it 500ms after that report.
// The write happens long after the edit that caused it, so it can come due after
// a send emptied the composer, or after the draft key moved on. Each case below
// is one such order of events.
//
// The clock is fake and ONLY `setTimeout` and `Date` are faked. fake-indexeddb
// answers on `setImmediate`, so the storage tier keeps working while the test
// decides when each debounce comes due. No case sleeps.
describe('MarkdownEditor draft saves', () => {
  const AGENT_KEY = 'draft-save-agent'
  const CONTROL_KEY = 'draft-save-agent-ctrl-1'
  const FIRST_PART = 'Write a two-step to-do list. f2b'
  const LAST_PART = '0100d0130'

  afterEach(() => {
    failingSaveKey = null
    clearDraft(AGENT_KEY)
    clearDraft(CONTROL_KEY)
    releaseGatedDraft()
    vi.useRealTimers()
  })

  /**
   * Yield to the real task queue until `done` holds.
   *
   * The condition decides, not a duration: a swap reads storage through
   * fake-indexeddb, which needs a few real tasks, and `waitFor` cannot be used
   * here because `vi.waitFor` moves the fake clock that these cases place by hand.
   */
  async function settleUntil(done: () => boolean): Promise<void> {
    for (let turn = 0; turn < 1000 && !done(); turn++)
      await new Promise<void>(resolve => setImmediate(resolve))
    expect(done()).toBe(true)
  }

  interface MountedComposer {
    send: () => void | Promise<void>
    /** Type into the composer, as a key press does. */
    type: (text: string) => void
    text: () => string
    setKey: (key: string) => void
    /** The draft keys that the editor finished swapping to, in order. */
    swappedTo: Array<string | null>
    /** Finish the send that `onSend` holds open. */
    finishSend: () => void
  }

  /** Mount the composer on real timers, then switch the two debounces to the fake clock. */
  async function mountComposer(initialKey: string): Promise<MountedComposer> {
    const [key, setKey] = createSignal(initialKey)
    const swappedTo: Array<string | null> = []
    let send: (() => void | Promise<void>) | undefined
    let type: ((text: string) => void) | undefined
    let text: (() => string) | undefined
    let finishSend = () => {}
    render(() => (
      <PreferencesProvider>
        <MarkdownEditor
          surface="chat"
          draftKey={{ key: key() }}
          onSend={() => new Promise<void>((resolve) => { finishSend = resolve })}
          onDraftKeyChanged={next => swappedTo.push(next)}
          imperative={{
            sendRef: (fn) => { send = fn },
            insertRef: (fn) => { type = fn },
            contentRef: (get) => { text = get },
          }}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(send).toBeTypeOf('function'))
    await waitFor(() => expect(type).toBeTypeOf('function'))
    await waitFor(() => expect(text).toBeTypeOf('function'))
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
    return {
      send: () => send?.(),
      type: value => type?.(value),
      text: () => text?.() ?? '',
      setKey,
      swappedTo,
      finishSend: () => finishSend(),
    }
  }

  /**
   * The order of events that the E2E failure shows. A stall splits the typing, so
   * Milkdown reports the FIRST PART alone, and the save of that report is due
   * 500ms later. The user types the rest and sends. The send empties the
   * composer 100ms before the save is due, and Milkdown's report of the empty
   * document is due 200ms after the send, so 100ms after the save.
   *
   * Returns with the send finished and the fake clock 400ms after the first
   * report: the save is due in 100ms and the empty report in 200ms.
   */
  async function sendWhileAStalePartialSaveIsPending(composer: MountedComposer): Promise<void> {
    composer.type(FIRST_PART)
    await vi.advanceTimersByTimeAsync(200)
    await vi.advanceTimersByTimeAsync(300)
    composer.type(LAST_PART)
    const sent = composer.send()
    await vi.advanceTimersByTimeAsync(100)
    composer.finishSend()
    await sent
    expect(composer.text()).toBe('')
  }

  // A save that reads the live document must still write the typed text and the
  // cursor, and it must not write before both debounces come due.
  it('saves the typed text and its cursor once the debounces come due', async () => {
    const composer = await mountComposer(AGENT_KEY)

    composer.type('an unsent message')
    await vi.advanceTimersByTimeAsync(699)
    await flushStorageWrites()
    // One millisecond before the save is due, nothing is stored yet.
    expect(await loadDraft(AGENT_KEY)).toEqual({ content: '', cursor: -1 })

    await vi.advanceTimersByTimeAsync(1)
    await flushStorageWrites()
    expect(await loadDraft(AGENT_KEY)).toEqual({ content: 'an unsent message', cursor: 1 + 'an unsent message'.length })
  })

  // The debounced save guards one failure: the read of the document of an editor
  // that is mid-teardown. A write that fails is another failure, and the guard must
  // not hide it.
  it('does not hide a failed write behind the guard for a torn-down editor', async () => {
    const composer = await mountComposer(AGENT_KEY)
    composer.type('an unsent message')
    failingSaveKey = AGENT_KEY
    await vi.advanceTimersByTimeAsync(200)

    await expect(vi.advanceTimersByTimeAsync(500)).rejects.toThrow('the storage rejected the draft')
  })

  it('saves the text that the user types after a send', async () => {
    const composer = await mountComposer(AGENT_KEY)
    await sendWhileAStalePartialSaveIsPending(composer)

    composer.type('the next message')
    await vi.advanceTimersByTimeAsync(700)
    await flushStorageWrites()

    expect((await loadDraft(AGENT_KEY)).content).toBe('the next message')
  })

  it('leaves no draft behind when a save that was pending before a send comes due after it', async () => {
    const composer = await mountComposer(AGENT_KEY)
    await sendWhileAStalePartialSaveIsPending(composer)

    // The first report's save comes due now: 100ms after the send emptied the
    // composer and 100ms before the report of the empty document.
    await vi.advanceTimersByTimeAsync(150)
    await flushStorageWrites()

    expect((await loadDraft(AGENT_KEY)).content).toBe('')
  })

  // The E2E failure itself. A control request moves the draft key to the
  // request's key and moves it back when the request ends. The way back reads
  // the draft of the agent key, so a stale write under that key reappears in the
  // composer, and the next message is typed after the one that was already sent.
  it('does not bring a sent message back when the draft key returns after a control request', async () => {
    const composer = await mountComposer(AGENT_KEY)
    await sendWhileAStalePartialSaveIsPending(composer)

    composer.setKey(CONTROL_KEY)
    await settleUntil(() => composer.swappedTo.includes(CONTROL_KEY))
    await vi.advanceTimersByTimeAsync(100)

    composer.setKey(AGENT_KEY)
    await settleUntil(() => composer.swappedTo.at(-1) === AGENT_KEY)

    expect(composer.text()).toBe('')
  })

  // The same mechanism through the other writer: the key moves while a read is
  // open, and the user keeps typing into the document of the OUTGOING key. The
  // save that follows must go to the key that owns that document, never to the
  // key that is still loading.
  it('saves the outgoing document under its own key when the user types while the next key loads', async () => {
    saveDraft(AGENT_KEY, 'the agent document', -1)
    saveDraft(CONTROL_KEY, 'the control document', -1)
    await flushStorageWrites()
    const composer = await mountComposer(AGENT_KEY)
    await settleUntil(() => composer.text() === 'the agent document')
    gateDraftRead(CONTROL_KEY)

    composer.setKey(CONTROL_KEY)
    await settleUntil(gatedReadStarted)
    composer.type(' typed while the control draft loads')
    await vi.advanceTimersByTimeAsync(200)
    await vi.advanceTimersByTimeAsync(500)
    releaseGatedDraft()
    await settleUntil(() => composer.swappedTo.includes(CONTROL_KEY))
    await flushStorageWrites()

    expect((await loadDraft(CONTROL_KEY)).content).toBe('the control document')
    expect((await loadDraft(AGENT_KEY)).content).toBe('the agent document typed while the control draft loads')
  })
})

/**
 * The composer is one of the editor's hosts, and the session-goal dialog is
 * another. What separates them is one prop, because the two markers below are
 * one fact -- see `MarkdownEditorSurface`.
 */
describe('MarkdownEditor surface', () => {
  it('marks the chat composer as the chat input', async () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <MarkdownEditor surface="chat" onSend={() => {}} />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(container.querySelector('.ProseMirror')).not.toBeNull())
    expect(container.querySelector('[data-testid="composer-box"]')).not.toBeNull()
    expect(container.querySelector('[data-testid="composer-editor"][data-chat-input]')).not.toBeNull()
  })

  /**
   * `useShortcuts` reads `data-chat-input` for the `chatInputFocused` context,
   * and `$mod+j` maps to `chat.sendMessage` there -- so a goal editor carrying
   * it would send a chat message from inside a dialog. The test ids are the
   * same fact: about twenty E2E specs address `composer-editor` with an unscoped
   * locator, which a second element of that name breaks.
   */
  it('leaves every chat marker off another surface', async () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <MarkdownEditor surface="goal" onSend={() => {}} />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(container.querySelector('.ProseMirror')).not.toBeNull())
    expect(container.querySelector('[data-testid="goal-box"]')).not.toBeNull()
    expect(container.querySelector('[data-testid="goal-editor"]')).not.toBeNull()
    expect(container.querySelector('[data-chat-input]')).toBeNull()
    // Every id, not only the two that identify the editor. The three layout slots
    // would put the same collision back for anything that addresses one.
    for (const id of ['composer-editor', 'composer-box', 'composer-plus-slot', 'composer-separator', 'composer-footer-slot'])
      expect(container.querySelector(`[data-testid="${id}"]`), id).toBeNull()
    expect(container.querySelector('[data-testid="goal-footer-slot"]')).not.toBeNull()
  })

  // The stylesheet reserves a left column for the `[+]` button. A box with no
  // `[+]` would otherwise start its text about 40px in, for a control that is
  // not there.
  it('reserves the left column only for a box that has a [+] button', async () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <MarkdownEditor surface="chat" onSend={() => {}} plus={<button type="button">+</button>} />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(container.querySelector('.ProseMirror')).not.toBeNull())
    expect(container.querySelector('[data-testid="composer-box"]')).toHaveAttribute('data-plus')
  })

  it('reserves no left column for a box with no [+] button', async () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <MarkdownEditor surface="goal" onSend={() => {}} />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(container.querySelector('.ProseMirror')).not.toBeNull())
    expect(container.querySelector('[data-testid="goal-box"]')).not.toHaveAttribute('data-plus')
  })

  /**
   * A slot that RESOLVES to nothing reserves nothing either.
   *
   * `children()` gives an ARRAY for a fragment, and `[]` is truthy -- so a bare
   * truthiness test on the resolved node reads an empty fragment, an empty
   * `<For>`, or a fragment of falsy entries as "a button is there" and leaves a
   * ~40px gutter with nothing in it.
   */
  it.each([
    ['an empty fragment', <></>],
    [
      'a fragment of falsy entries',
      <>
        {false && <button type="button">a</button>}
        {null}
      </>,
    ],
    ['a bare false', false],
  ])('reserves no left column for %s', async (_name, plus) => {
    const { container } = render(() => (
      <PreferencesProvider>
        <MarkdownEditor surface="chat" onSend={() => {}} plus={plus} />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(container.querySelector('.ProseMirror')).not.toBeNull())
    expect(container.querySelector('[data-testid="composer-box"]')).not.toHaveAttribute('data-plus')
  })

  /**
   * `onContentChange` answers "is there anything here"; this one carries the
   * text. The goal dialog measures the objective's UTF-8 length against the
   * worker's cap, which the boolean cannot answer.
   *
   * The assertion is the WHOLE serialized string, not a substring of the input.
   * A `toContain` on the text just handed to `set` passes even when the editor
   * parsed nothing, because `set` used to echo its own argument back.
   */
  it('reports the document text to its host', async () => {
    const seen: string[] = []
    let setContent: ((text: string) => void) | undefined
    render(() => (
      <PreferencesProvider>
        <MarkdownEditor
          surface="goal"
          onSend={() => {}}
          onMarkdownChange={md => seen.push(md)}
          imperative={{ contentRef: (_get, set) => { setContent = set } }}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(setContent).toBeTypeOf('function'))
    setContent?.('ship the **auth refactor**')
    await waitFor(() => expect(seen.at(-1)?.trim()).toBe('ship the **auth refactor**'))
  })

  /**
   * A caption outside the editor names the contenteditable.
   *
   * It has to land on the `.ProseMirror` element, not on the wrapper: the
   * wrapper is not the editable region, so naming it names nothing and a
   * screen reader announces an unnamed editable area.
   */
  it('names the contenteditable from the caption its host points at', async () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <MarkdownEditor surface="goal" ariaLabelledBy="the-caption" onSend={() => {}} />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(container.querySelector('.ProseMirror')).not.toBeNull())
    expect(container.querySelector('.ProseMirror')?.getAttribute('aria-labelledby')).toBe('the-caption')
  })

  /**
   * `minHeight` is a FLOOR, so the ceiling still binds.
   *
   * `pinnedHeight` -- the composer's dragged handle -- becomes a fixed height
   * once the content passes it, which stops `maxHeight` from ever applying. A
   * host that wants a comfortable opening size and room to grow states
   * `minHeight`, and both values must reach the box.
   */
  it('applies a floor and a ceiling together for a host that states minHeight', async () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <MarkdownEditor surface="goal" minHeight={120} maxHeight={320} onSend={() => {}} />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(container.querySelector('.ProseMirror')).not.toBeNull())
    const wrapper = container.querySelector('[data-testid="goal-editor"]') as HTMLElement
    expect(wrapper.style.minHeight).toBe('120px')
    expect(wrapper.style.maxHeight).toBe('320px')
    // Never a fixed height: that is what would make the ceiling unreachable.
    expect(wrapper.style.height).toBe('')
  })

  /**
   * A seeded document is announced at all, and announced as the DOCUMENT.
   *
   * Milkdown's `markdownUpdated` listener never fires for `defaultValueCtx`, so
   * without this emit a host with `onMarkdownChange` learns nothing until the
   * reader types. The value is the round trip, not the prop: the goal dialog
   * measures it against the worker's byte cap, and the escaped form is what a
   * send would actually submit.
   */
  it('reports the serialized document for an initialMarkdown seed', async () => {
    const seen: string[] = []
    render(() => (
      <PreferencesProvider>
        <MarkdownEditor
          surface="goal"
          initialMarkdown="Stop when 2 * 3 = 6"
          onSend={() => {}}
          onMarkdownChange={md => seen.push(md)}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(seen.length).toBeGreaterThan(0))
    expect(seen.at(-1)?.trim()).toBe('Stop when 2 \\* 3 = 6')
  })

  /**
   * The seed WINS over a stored draft.
   *
   * A host that states the starting text is editing a specific document; the
   * draft is what it abandoned last time. Showing the draft would silently edit
   * the wrong thing.
   */
  it('prefers an initialMarkdown seed over a stored draft', async () => {
    const KEY = 'seed-beats-draft'
    await saveDraft(KEY, 'the abandoned draft', -1)
    const { container } = render(() => (
      <PreferencesProvider>
        <MarkdownEditor
          surface="goal"
          draftKey={{ key: KEY }}
          initialMarkdown="the real objective"
          onSend={() => {}}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(container.querySelector('.ProseMirror')).toHaveTextContent('the real objective'))
    expect(container.querySelector('.ProseMirror')).not.toHaveTextContent('abandoned')
  })

  /**
   * A loaded DRAFT is reported the same way: from the document, not from the
   * stored string.
   *
   * Milkdown's `markdownUpdated` listener never fires for a document seeded
   * through `defaultValueCtx`, so this one emit is everything a host with
   * `onMarkdownChange` learns about a restored draft — and it has to be the
   * text a send would submit.
   */
  it('reports the serialized document for a loaded draft, not the stored string', async () => {
    const KEY = 'draft-round-trip'
    await saveDraft(KEY, 'Stop when 2 * 3 = 6', -1)
    const seen: string[] = []
    render(() => (
      <PreferencesProvider>
        <MarkdownEditor
          surface="chat"
          draftKey={{ key: KEY }}
          onSend={() => {}}
          onMarkdownChange={md => seen.push(md)}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(seen.length).toBeGreaterThan(0))
    expect(seen.at(-1)?.trim()).toBe('Stop when 2 \\* 3 = 6')
  })

  /**
   * A programmatic `set` reports what the DOCUMENT holds, not the string it was
   * handed.
   *
   * The round trip through ProseMirror is not the identity: it escapes a bare
   * `*` so the text cannot be re-read as emphasis. A host that mirrors this
   * report -- the goal dialog measures it against the worker's byte cap --
   * otherwise holds a string that differs from the one a send would submit, and
   * corrects itself a debounce later with no keystroke.
   */
  it('reports the serialized document after a programmatic set, not the raw seed', async () => {
    const seen: string[] = []
    let setContent: ((text: string) => void) | undefined
    render(() => (
      <PreferencesProvider>
        <MarkdownEditor
          surface="goal"
          onSend={() => {}}
          onMarkdownChange={md => seen.push(md)}
          imperative={{ contentRef: (_get, set) => { setContent = set } }}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(setContent).toBeTypeOf('function'))
    setContent?.('Stop when 2 * 3 = 6')
    await waitFor(() => expect(seen.at(-1)?.trim()).toBe('Stop when 2 \\* 3 = 6'))
  })

  /**
   * A filename is not markup, and the agent is not a markdown reader.
   *
   * The GitHub Flavored Markdown autolink rule escapes every dot that follows a `w`
   * so a bare `www.example.com` cannot be re-read as a link, and it cannot tell that
   * from the end of an ordinary word. A live census asked ten providers for
   * `blank-new.txt`; the message they received said `blank-new\.txt`, and two of them
   * created a file with the backslash in its name (RL-021).
   */
  it('reports a filename without the autolink rule escape', async () => {
    const seen: string[] = []
    let setContent: ((text: string) => void) | undefined
    render(() => (
      <PreferencesProvider>
        <MarkdownEditor
          surface="goal"
          onSend={() => {}}
          onMarkdownChange={md => seen.push(md)}
          imperative={{ contentRef: (_get, set) => { setContent = set } }}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(setContent).toBeTypeOf('function'))
    setContent?.('Create blank-new.txt and raw.json')
    await waitFor(() => expect(seen.at(-1)?.trim()).toBe('Create blank-new.txt and raw.json'))
  })

  /**
   * An identifier is not markup either.
   *
   * The serializer escapes every `_` of ordinary text, so a typed
   * `COMMANDCODE_MODE_CONTEXT` reached the agent as `COMMANDCODE\_MODE\_CONTEXT`.
   * An underscore between two letters can neither open nor close emphasis, so the
   * escape protects nothing. An emphasis that the reader built keeps its delimiters.
   */
  it('reports an identifier and an emphasis without the escape of the identifier', async () => {
    const seen: string[] = []
    let setContent: ((text: string) => void) | undefined
    render(() => (
      <PreferencesProvider>
        <MarkdownEditor
          surface="goal"
          onSend={() => {}}
          onMarkdownChange={md => seen.push(md)}
          imperative={{ contentRef: (_get, set) => { setContent = set } }}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(setContent).toBeTypeOf('function'))
    setContent?.('Keep COMMANDCODE_MODE_CONTEXT and _this_ emphasis in snake_case_name')
    await waitFor(() => expect(seen.at(-1)?.trim()).toBe('Keep COMMANDCODE_MODE_CONTEXT and _this_ emphasis in snake_case_name'))
  })

  it('sends an identifier without the serializer escape', async () => {
    const onSend = vi.fn()
    let send: (() => void | Promise<void>) | undefined
    saveDraft(DRAFT_KEY, 'Keep COMMANDCODE_MODE_CONTEXT before the native process restarts.', -1)
    const { getByTestId } = render(() => (
      <PreferencesProvider>
        <MarkdownEditor
          surface="chat"
          draftKey={{ key: DRAFT_KEY }}
          onSend={onSend}
          imperative={{ sendRef: fn => send = fn }}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(send).toBeTypeOf('function'))
    const document = getByTestId('composer-editor').querySelector('.ProseMirror') as HTMLElement
    await waitFor(() => expect(document).toHaveTextContent('COMMANDCODE_MODE_CONTEXT'))
    await send?.()
    expect(onSend).toHaveBeenCalledWith('Keep COMMANDCODE_MODE_CONTEXT before the native process restarts.')
  })

  /** A backslash inside a code span is the reader's own, so it survives the send. */
  it('keeps a backslash that a code span holds', async () => {
    const seen: string[] = []
    let setContent: ((text: string) => void) | undefined
    render(() => (
      <PreferencesProvider>
        <MarkdownEditor
          surface="goal"
          onSend={() => {}}
          onMarkdownChange={md => seen.push(md)}
          imperative={{ contentRef: (_get, set) => { setContent = set } }}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(setContent).toBeTypeOf('function'))
    setContent?.('match `new\\.txt` exactly')
    await waitFor(() => expect(seen.at(-1)?.trim()).toBe('match `new\\.txt` exactly'))
  })

  /**
   * The serializer writes a literal backtick of prose as a backslash and a backtick.
   * That backtick opens no code span, so the spans after it keep the backslashes that
   * the reader typed. The text before the span holds an underscore on purpose.
   * Milkdown writes a text without any escape when the text ends in white space and
   * holds no `*`, no `_` and no backslash. That text would carry the backtick as it is.
   */
  it('keeps a backslash that a code span holds after a literal backtick', async () => {
    const seen: string[] = []
    let setContent: ((text: string) => void) | undefined
    render(() => (
      <PreferencesProvider>
        <MarkdownEditor
          surface="goal"
          onSend={() => {}}
          onMarkdownChange={md => seen.push(md)}
          imperative={{ contentRef: (_get, set) => { setContent = set } }}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(setContent).toBeTypeOf('function'))
    setContent?.('a lone \\` in snake_case, then `x\\_1` and `raw\\.json`')
    await waitFor(() => expect(seen.at(-1)?.trim()).toBe('a lone \\` in snake_case, then `x\\_1` and `raw\\.json`'))
  })

  /**
   * A host that unmounts the editor from inside its own `onSend` -- which
   * `SetGoalDialog` does, by closing on a successful submit -- leaves the send's
   * continuation running against a destroyed editor. It must not rebuild the
   * document there, and it must still clear the draft, or `onCleanup` leaves the
   * SENT text behind as a draft that reappears on the next open.
   */
  it('does not touch a disposed editor after a send that unmounted it', async () => {
    const KEY = 'disposed-after-send'
    await saveDraft(KEY, 'the sent message', -1)
    const seen: string[] = []
    let send: (() => void | Promise<void>) | undefined
    const [open, setOpen] = createSignal(true)
    render(() => (
      <PreferencesProvider>
        <Show when={open()} keyed>
          <MarkdownEditor
            surface="chat"
            draftKey={{ key: KEY }}
            onSend={() => { setOpen(false) }}
            onMarkdownChange={md => seen.push(md)}
            imperative={{ sendRef: (fn) => { send = fn } }}
          />
        </Show>
      </PreferencesProvider>
    ))
    await waitFor(() => expect(send).toBeTypeOf('function'))
    await waitFor(() => expect(seen.at(-1)).toContain('the sent message'))
    const before = seen.length

    await send?.()

    // No report from the disposed editor: the reset never ran.
    expect(seen.length).toBe(before)
    expect(await loadDraft(KEY)).toEqual({ content: '', cursor: -1 })
  })
})
