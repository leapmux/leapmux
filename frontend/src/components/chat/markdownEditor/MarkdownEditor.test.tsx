import { render, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
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
// applied. Only `loadDraft` is replaced, and only for the key the case names;
// everything else is the real module, so the assertions still read real rows.
let gatedKey: string | null = null
let gateResolvers: Array<() => void> = []
let gateStarted = false

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
describe('markdownEditor send', () => {
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
describe('markdownEditor autofocus', () => {
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
          suppressAutoFocus={suppressAutoFocus}
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
describe('markdownEditor draft key swaps', () => {
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
})

/**
 * The composer is one of the editor's hosts, and the session-goal dialog is
 * another. What separates them is one prop, because the two markers below are
 * one fact -- see `MarkdownEditorSurface`.
 */
describe('markdownEditor surface', () => {
  it('marks the chat composer as the chat input', async () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <MarkdownEditor surface="chat" onSend={() => {}} />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(container.querySelector('.ProseMirror')).not.toBeNull())
    expect(container.querySelector('[data-testid="composer-box"]')).not.toBeNull()
    expect(container.querySelector('[data-testid="chat-editor"][data-chat-input]')).not.toBeNull()
  })

  /**
   * `useShortcuts` reads `data-chat-input` for the `chatInputFocused` context,
   * and `$mod+j` maps to `chat.sendMessage` there -- so a goal editor carrying
   * it would send a chat message from inside a dialog. The test ids are the
   * same fact: about twenty E2E specs address `chat-editor` with an unscoped
   * locator, which a second element of that name breaks.
   */
  it('leaves every chat marker off another surface', async () => {
    const { container } = render(() => (
      <PreferencesProvider>
        <MarkdownEditor surface="goal" onSend={() => {}} />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(container.querySelector('.ProseMirror')).not.toBeNull())
    expect(container.querySelector('[data-testid="goal-editor-box"]')).not.toBeNull()
    expect(container.querySelector('[data-testid="goal-editor"]')).not.toBeNull()
    expect(container.querySelector('[data-chat-input]')).toBeNull()
    // Every id, not only the two that name the editor. The three layout slots
    // would put the same collision back for anything that addresses one.
    for (const id of ['chat-editor', 'composer-box', 'composer-plus-slot', 'composer-separator', 'composer-footer-slot'])
      expect(container.querySelector(`[data-testid="${id}"]`), id).toBeNull()
    expect(container.querySelector('[data-testid="goal-editor-footer-slot"]')).not.toBeNull()
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
    expect(container.querySelector('[data-testid="goal-editor-box"]')).not.toHaveAttribute('data-plus')
  })

  /**
   * `onContentChange` answers "is there anything here"; this one carries the
   * text. The goal dialog measures the objective's UTF-8 length against the
   * worker's cap, which the boolean cannot answer.
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
    await waitFor(() => expect(seen.at(-1)).toContain('auth refactor'))
  })
})
