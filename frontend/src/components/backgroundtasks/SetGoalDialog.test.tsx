import { fireEvent, render, waitFor } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { GOAL_OBJECTIVE_BYTE_LIMIT } from '~/generated/contracts/validate'
import { useTestStorage } from '~/test-support/persistentStorage'
import { SetGoalDialog } from './SetGoalDialog'

useTestStorage()

function mount(props: { initialObjective?: string, onSubmit?: () => void, onClose?: () => void } = {}) {
  const onSubmit = vi.fn(props.onSubmit)
  const onClose = vi.fn(props.onClose)
  const result = render(() => (
    <PreferencesProvider>
      <SetGoalDialog
        initialObjective={props.initialObjective}
        onSubmit={onSubmit}
        onClose={onClose}
      />
    </PreferencesProvider>
  ))
  return { ...result, onSubmit, onClose }
}

/** The editor is built asynchronously, so every case waits for its document. */
async function editorReady(): Promise<HTMLElement> {
  let el: HTMLElement | null = null
  await waitFor(() => {
    el = document.querySelector('[data-testid="goal-editor"] .ProseMirror')
    expect(el).not.toBeNull()
  })
  return el as unknown as HTMLElement
}

describe('setGoalDialog', () => {
  // Replacing prefills the current objective, so the reader EDITS what is there
  // instead of retyping it. It is also what makes Clear safe without a
  // confirmation: the editor reopens holding the objective that was cleared.
  it('starts from the objective it was given', async () => {
    mount({ initialObjective: 'every test passes' })
    const editor = await editorReady()
    await waitFor(() => expect(editor.textContent).toContain('every test passes'))
  })

  it('submits the trimmed objective and closes', async () => {
    const { getByTestId, onSubmit, onClose } = mount({ initialObjective: 'ship it' })
    await editorReady()
    await waitFor(() => expect((getByTestId('set-goal-submit') as HTMLButtonElement).disabled).toBe(false))

    fireEvent.click(getByTestId('set-goal-submit'))

    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith('ship it'))
    expect(onClose).toHaveBeenCalled()
  })

  // The editor is the field for markdown, so what the user wrote as markdown is
  // what the worker stores -- the card renders it back the same way.
  it('submits the objective as markdown source', async () => {
    const { getByTestId, onSubmit } = mount({ initialObjective: 'ship the **auth refactor**' })
    await editorReady()
    await waitFor(() => expect((getByTestId('set-goal-submit') as HTMLButtonElement).disabled).toBe(false))

    fireEvent.click(getByTestId('set-goal-submit'))

    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith('ship the **auth refactor**'))
  })

  it('refuses an empty objective', async () => {
    const { getByTestId, onSubmit } = mount()
    await editorReady()
    const submit = getByTestId('set-goal-submit') as HTMLButtonElement
    expect(submit.disabled).toBe(true)
    fireEvent.click(submit)
    expect(onSubmit).not.toHaveBeenCalled()
  })

  /**
   * The worker REFUSES an objective over the cap rather than truncating it, so
   * the dialog refuses it too -- otherwise the user meets an error whose cause
   * is invisible. The cap counts UTF-8 BYTES, which is why the dialog measures
   * them rather than characters.
   */
  it('refuses an objective over the byte cap and says by how much', async () => {
    const over = 'a'.repeat(GOAL_OBJECTIVE_BYTE_LIMIT + 12)
    const { getByTestId, onSubmit } = mount({ initialObjective: over })
    await editorReady()

    await waitFor(() => expect(getByTestId('set-goal-too-long').textContent).toContain('Too long by 12 bytes'))
    const submit = getByTestId('set-goal-submit') as HTMLButtonElement
    expect(submit.disabled).toBe(true)
    fireEvent.click(submit)
    expect(onSubmit).not.toHaveBeenCalled()
  })

  // A counter over a two-line objective states a number nobody is close to
  // spending. It appears where the refusal becomes a real prospect.
  it('states the remaining budget only near the cap', async () => {
    const { queryByTestId } = mount({ initialObjective: 'a short goal' })
    await editorReady()
    expect(queryByTestId('set-goal-budget')).toBeNull()
    expect(queryByTestId('set-goal-too-long')).toBeNull()
  })

  it('states the remaining budget once the objective approaches the cap', async () => {
    const near = 'a'.repeat(Math.ceil(GOAL_OBJECTIVE_BYTE_LIMIT * 0.95))
    const { getByTestId } = mount({ initialObjective: near })
    await editorReady()
    await waitFor(() => {
      expect(getByTestId('set-goal-budget').textContent)
        .toBe(`${(GOAL_OBJECTIVE_BYTE_LIMIT - near.length).toLocaleString()} bytes left`)
    })
  })

  it('writes nothing when cancelled', async () => {
    const { getByText, onSubmit, onClose } = mount({ initialObjective: 'abandoned' })
    await editorReady()
    fireEvent.click(getByText('Cancel'))
    expect(onSubmit).not.toHaveBeenCalled()
    expect(onClose).toHaveBeenCalled()
  })

  /**
   * The goal editor is not the message composer. `useShortcuts` reads
   * `data-chat-input` for the `chatInputFocused` context, and `$mod+j` sends the
   * chat message there -- so the marker here would send a message from inside
   * this dialog. The test id matters for the same kind of reason: about twenty
   * E2E specs address `chat-editor` with an unscoped locator.
   */
  it('is not the chat composer', async () => {
    mount({ initialObjective: 'every test passes' })
    await editorReady()
    expect(document.querySelector('[data-chat-input]')).toBeNull()
    expect(document.querySelector('[data-testid="chat-editor"]')).toBeNull()
  })
})
