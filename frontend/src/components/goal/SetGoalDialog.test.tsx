import { fireEvent, render, waitFor } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { GOAL_OBJECTIVE_BYTE_LIMIT } from '~/generated/contracts/validate'
import { useTestStorage } from '~/test-support/persistentStorage'
import { SetGoalDialog } from './SetGoalDialog'

useTestStorage()

function mount(props: { initialObjective?: string, onSubmit?: () => Promise<boolean>, onClose?: () => void } = {}) {
  const onSubmit = vi.fn(props.onSubmit ?? (async () => true))
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

/** Wait for the editor's send function and, when supplied, its initial document. */
async function editorReady(seeded?: string): Promise<HTMLElement> {
  let el: HTMLElement | null = null
  await waitFor(() => {
    el = document.querySelector('[data-testid="goal-editor"] .ProseMirror')
    expect(el).not.toBeNull()
    // The button is disabled until the editor installs its imperative send, so
    // this is the dialog's own readiness signal. `.ProseMirror` appearing is
    // not enough: the refs install after it, and a click before then reaches an
    // `undefined` send.
    const submit = document.querySelector('[data-testid="set-goal-submit"]') as HTMLButtonElement | null
    expect(submit?.disabled).toBe(false)
    if (seeded !== undefined)
      expect(el!.textContent).toContain(seeded)
  })
  return el as unknown as HTMLElement
}

describe('setGoalDialog', () => {
  it('blocks every close path while the goal request is pending', async () => {
    let finish!: (accepted: boolean) => void
    const pending = new Promise<boolean>(resolve => finish = resolve)
    const { getByTestId, getByRole, onClose } = mount({ initialObjective: 'Keep this objective', onSubmit: () => pending })
    await editorReady('Keep this objective')
    fireEvent.click(getByTestId('set-goal-submit'))
    const dialog = getByRole('dialog')
    const openClass = dialog.className
    const cancel = new Event('cancel', { cancelable: true })
    dialog.dispatchEvent(cancel)
    expect(cancel.defaultPrevented).toBe(true)
    expect(dialog.className).toBe(openClass)
    expect(getByRole('button', { name: 'Close' })).toBeDisabled()
    expect(getByRole('button', { name: 'Cancel' })).toBeDisabled()
    expect(onClose).not.toHaveBeenCalled()
    finish(false)
    await waitFor(() => expect(getByRole('button', { name: 'Close' })).toBeEnabled())
  })

  it('keeps the objective until asynchronous delivery succeeds', async () => {
    let finish!: (accepted: boolean) => void
    const pending = new Promise<boolean>(resolve => finish = resolve)
    const { getByTestId, onClose, onSubmit } = mount({ initialObjective: 'Keep this objective', onSubmit: () => pending })
    const editor = await editorReady('Keep this objective')
    fireEvent.click(getByTestId('set-goal-submit'))
    expect(onClose).not.toHaveBeenCalled()
    expect(getByTestId('set-goal-submit')).toBeDisabled()
    expect(editor.textContent).toBe('Keep this objective')
    fireEvent.click(getByTestId('set-goal-submit'))
    expect(onSubmit).toHaveBeenCalledTimes(1)
    finish(false)
    await waitFor(() => expect(getByTestId('set-goal-submit')).toBeEnabled())
    expect(onClose).not.toHaveBeenCalled()
    expect(editor.textContent).toBe('Keep this objective')
  })

  it('closes after asynchronous delivery succeeds', async () => {
    let finish!: (accepted: boolean) => void
    const pending = new Promise<boolean>(resolve => finish = resolve)
    const { getByTestId, onClose } = mount({ initialObjective: 'Deliver this objective', onSubmit: () => pending })
    await editorReady('Deliver this objective')
    fireEvent.click(getByTestId('set-goal-submit'))
    expect(onClose).not.toHaveBeenCalled()
    finish(true)
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
  })

  it('keeps the objective and reports a rejected submission', async () => {
    const { getByTestId, onClose } = mount({
      initialObjective: 'Keep the failed objective',
      onSubmit: async () => {
        throw new Error('The provider is unavailable')
      },
    })
    const editor = await editorReady('Keep the failed objective')
    fireEvent.click(getByTestId('set-goal-submit'))
    await waitFor(() => expect(getByTestId('set-goal-refusal')).toHaveTextContent('The provider is unavailable'))
    expect(onClose).not.toHaveBeenCalled()
    expect(editor.textContent).toBe('Keep the failed objective')
    expect(getByTestId('set-goal-submit')).toBeEnabled()
  })

  it('does not close a new dialog when an old submission finishes after disposal', async () => {
    let finish!: (accepted: boolean) => void
    const pending = new Promise<boolean>(resolve => finish = resolve)
    const { getByTestId, onClose, unmount } = mount({ initialObjective: 'Old objective', onSubmit: () => pending })
    await editorReady('Old objective')
    fireEvent.click(getByTestId('set-goal-submit'))
    unmount()
    finish(true)
    await pending
    expect(onClose).not.toHaveBeenCalled()
  })

  // Replace edits the current objective. A cleared goal no longer supplies an objective.
  it('starts from the objective it was given', async () => {
    mount({ initialObjective: 'every test passes' })
    const editor = await editorReady()
    await waitFor(() => expect(editor.textContent).toContain('every test passes'))
  })

  it('submits the trimmed objective and closes', async () => {
    const { getByTestId, onSubmit, onClose } = mount({ initialObjective: 'ship it' })
    await editorReady('ship it')

    fireEvent.click(getByTestId('set-goal-submit'))

    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith('ship it'))
    expect(onClose).toHaveBeenCalled()
  })

  // The editor is the field for markdown, so what the user wrote as markdown is
  // what the worker stores -- the card renders it back the same way.
  it('submits the objective as markdown source', async () => {
    const { getByTestId, onSubmit } = mount({ initialObjective: 'ship the **auth refactor**' })
    await editorReady('ship the auth refactor')

    fireEvent.click(getByTestId('set-goal-submit'))

    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith('ship the **auth refactor**'))
  })

  /**
   * The button is deliberately never `disabled`.
   *
   * Its state would have to come from the mirrored text, which lags the
   * document by the editor's 200ms listener debounce -- so a click inside that
   * window met a disabled button and did NOTHING, with no message. It always
   * clicks; the refusal reads the live document and says why.
   */
  it('refuses an empty objective and says so', async () => {
    const { getByTestId, onSubmit, onClose } = mount()
    await editorReady()

    fireEvent.click(getByTestId('set-goal-submit'))

    await waitFor(() => expect(getByTestId('set-goal-refusal').textContent)
      .toContain('Write the condition the agent works toward.'))
    expect(onSubmit).not.toHaveBeenCalled()
    expect(onClose).not.toHaveBeenCalled()
  })

  /**
   * Whitespace is empty. The editor trims before it hands the text over, so a
   * document of spaces reaches `submit` as `''` and is refused there -- and the
   * dialog must not hand the worker a blank standing goal the agent can never
   * satisfy.
   */
  it('refuses an objective that is only whitespace', async () => {
    const { getByTestId, onSubmit, onClose } = mount({ initialObjective: '   ' })
    await editorReady()

    fireEvent.click(getByTestId('set-goal-submit'))

    await waitFor(() => expect(getByTestId('set-goal-refusal').textContent)
      .toContain('Write the condition the agent works toward.'))
    expect(onSubmit).not.toHaveBeenCalled()
    expect(onClose).not.toHaveBeenCalled()
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

    await waitFor(() => expect(getByTestId('set-goal-refusal').textContent).toContain('Too long by 12 bytes'))

    fireEvent.click(getByTestId('set-goal-submit'))

    expect(onSubmit).not.toHaveBeenCalled()
  })

  // A counter over a two-line objective states a number nobody is close to
  // spending. It appears where the refusal becomes a real prospect.
  it('states the remaining budget only near the cap', async () => {
    const { queryByTestId } = mount({ initialObjective: 'a short goal' })
    await editorReady()
    expect(queryByTestId('set-goal-budget')).toBeNull()
    expect(queryByTestId('set-goal-refusal')).toBeNull()
  })

  it('states the remaining budget once the objective approaches the cap', async () => {
    const near = 'a'.repeat(Math.ceil(GOAL_OBJECTIVE_BYTE_LIMIT * 0.95))
    const { getByTestId } = mount({ initialObjective: near })
    await editorReady()
    await waitFor(() => {
      // The expected number is spelled with an explicit `en-US`, not with the
      // same `toLocaleString()` the component calls: asserting a value against
      // the call that produced it passes in every locale, including the ones
      // where the rendered string is wrong.
      expect(getByTestId('set-goal-budget').textContent)
        .toBe(`${(GOAL_OBJECTIVE_BYTE_LIMIT - near.length).toLocaleString('en-US')} bytes left`)
    })
  })

  /** Keep direct section and footer children without a form around the editor's LinkPopover form. */
  it('renders no form, and puts the section and footer where Dialog styles them', async () => {
    const { getByTestId } = mount({ initialObjective: 'ship it' })
    await editorReady('ship it')

    const body = getByTestId('set-goal-dialog').querySelector('[class*="body"]')!
    // Scoped to the direct children: the editor's own LinkPopover renders a
    // real `<form>` deeper in the tree, which is the nesting this removal also
    // undoes.
    expect(body.querySelector(':scope > form')).toBeNull()
    expect([...body.children].map(el => el.tagName)).toEqual(['SECTION', 'FOOTER'])
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
   * E2E specs address `composer-editor` with an unscoped locator.
   */
  it('is not the chat composer', async () => {
    mount({ initialObjective: 'every test passes' })
    await editorReady()
    expect(document.querySelector('[data-chat-input]')).toBeNull()
    expect(document.querySelector('[data-testid="composer-editor"]')).toBeNull()
  })
})
