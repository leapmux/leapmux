import { render, waitFor } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { clearDraft, loadDraft, saveDraft } from '~/lib/editor/draftPersistence'
import { MarkdownEditor } from './MarkdownEditor'

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
    expect(loadDraft(DRAFT_KEY).content).toBe('')
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
