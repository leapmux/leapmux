import { render, waitFor } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'

/**
 * A rejected `buildEditor` must leave a MESSAGE, not a dead grey box.
 *
 * `onMount` builds the editor asynchronously. Without a `catch` the rejection
 * is unhandled, `onReady` never fires, the imperative refs are never installed,
 * and a host that drives its own submit button through `sendRef` has no route
 * to the action at all -- an empty box and a button that does nothing, with no
 * explanation.
 *
 * Its own file, because the mock has to replace `buildEditor` for the whole
 * module graph and the other cases in `./MarkdownEditor.test.tsx` need the real
 * one.
 */
vi.mock('./editorSetup', async (importActual) => {
  const actual = await importActual<typeof import('./editorSetup')>()
  return {
    ...actual,
    buildEditor: () => Promise.reject(new Error('a plugin threw')),
  }
})

const { MarkdownEditor } = await import('./MarkdownEditor')

describe('markdownEditor build failure', () => {
  it('says the editor failed to load instead of leaving an empty box', async () => {
    const error = vi.spyOn(console, 'error').mockImplementation(() => {})
    const { container } = render(() => (
      <PreferencesProvider>
        <MarkdownEditor surface="goal" onSend={() => {}} />
      </PreferencesProvider>
    ))

    await waitFor(() => {
      expect(container.querySelector('[data-testid="goal-build-failed"]')).not.toBeNull()
    })
    expect(container.querySelector('[data-testid="goal-build-failed"]')?.textContent)
      .toContain('The editor failed to load')
    // The cause reaches the console, so the failure is diagnosable.
    expect(error).toHaveBeenCalled()
  })
})
