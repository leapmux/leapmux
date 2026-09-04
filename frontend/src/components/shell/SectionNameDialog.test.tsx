import type { SectionNamePayload } from './SectionNameDialog'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { Sidebar } from '~/generated/proto/leapmux/v1/section_pb'
import { SectionNameDialog } from './SectionNameDialog'

function renderDialog(
  payload: SectionNamePayload,
  overrides: { onSubmit?: (name: string) => Promise<void>, onClose?: () => void } = {},
) {
  const props = {
    payload,
    onSubmit: overrides.onSubmit ?? vi.fn(async () => {}),
    onClose: overrides.onClose ?? vi.fn(),
  }
  render(() => <SectionNameDialog {...props} />)
  return props
}

const CREATE: SectionNamePayload = { mode: 'create', sidebar: Sidebar.LEFT }
// No `sidebar`: the payload is a union now, and a rename has no sidebar to
// carry. Adding one back is a compile error rather than a field nobody reads.
const RENAME: SectionNamePayload = {
  mode: 'rename',
  sectionId: 'sec-1',
  initialName: 'Old Name',
}

function nameInput(): HTMLInputElement {
  return screen.getByTestId('title-input') as HTMLInputElement
}

function submitButton(): HTMLButtonElement {
  return screen.getByRole('button', { name: /^(Create|Rename)$/ }) as HTMLButtonElement
}

describe('sectionNameDialog', () => {
  it('opens empty and refuses an empty name when creating', () => {
    renderDialog(CREATE)

    expect(screen.getByText('New section')).toBeInTheDocument()
    expect(nameInput().value).toBe('')
    expect(submitButton().disabled).toBe(true)
  })

  it('opens on the current name when renaming', () => {
    renderDialog(RENAME)

    expect(screen.getByText('Rename section')).toBeInTheDocument()
    expect(nameInput().value).toBe('Old Name')
    expect(submitButton().disabled).toBe(false)
  })

  it('submits the name and then closes', async () => {
    const onSubmit = vi.fn(async () => {})
    const props = renderDialog(CREATE, { onSubmit })

    fireEvent.input(nameInput(), { target: { value: 'Reviews' } })
    fireEvent.click(submitButton())

    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith('Reviews'))
    await waitFor(() => expect(props.onClose).toHaveBeenCalledOnce())
  })

  it('submits the CLEANED name, because the hub sanitizes whatever arrives', async () => {
    // Raw text would leave the sidebar showing one name while the hub stored
    // another until the next refresh.
    const onSubmit = vi.fn(async () => {})
    renderDialog(CREATE, { onSubmit })

    fireEvent.input(nameInput(), { target: { value: '  Code\u200Breview\u00A0board  ' } })
    fireEvent.click(submitButton())

    // Trimmed, the zero-width space dropped, and the no-break space folded to
    // an ordinary one -- the same rule `commitRename` applies to a workspace.
    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith('Codereview board'))
  })

  it('refuses a name that cleans to nothing', () => {
    renderDialog(CREATE)

    fireEvent.input(nameInput(), { target: { value: '\u200B\uFEFF\u00AD' } })
    expect(submitButton().disabled).toBe(true)
  })

  it('shows the failure in its own error row and stays open', async () => {
    // The dialog owns the error surface, which is why `useSectionOperations`
    // lets the create and the rename REJECT instead of toasting.
    const onSubmit = vi.fn(async () => {
      throw new Error('section limit reached')
    })
    const props = renderDialog(CREATE, { onSubmit })

    fireEvent.input(nameInput(), { target: { value: 'Reviews' } })
    fireEvent.click(submitButton())

    expect(await screen.findByText('section limit reached')).toBeInTheDocument()
    expect(props.onClose).not.toHaveBeenCalled()
    // And the dialog is USABLE again. `useDialogSubmit` releases both
    // `submitting` and its re-entrancy latch in a `finally`; if either leaked,
    // the user would face an error message over a permanently disabled Create
    // button, with Cancel as the only way out -- and the assertions above
    // would still pass.
    await waitFor(() => expect(submitButton().disabled).toBe(false))
  })

  it('submits again after a failure, so one rejection does not wedge it', async () => {
    const onSubmit = vi.fn()
      .mockRejectedValueOnce(new Error('section limit reached'))
      .mockResolvedValueOnce(undefined)
    const props = renderDialog(CREATE, { onSubmit })

    fireEvent.input(nameInput(), { target: { value: 'Reviews' } })
    fireEvent.click(submitButton())
    expect(await screen.findByText('section limit reached')).toBeInTheDocument()

    await waitFor(() => expect(submitButton().disabled).toBe(false))
    fireEvent.click(submitButton())

    await waitFor(() => expect(props.onClose).toHaveBeenCalledOnce())
    expect(onSubmit).toHaveBeenCalledTimes(2)
  })

  it('cancels without submitting', () => {
    const props = renderDialog(RENAME)

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(props.onSubmit).not.toHaveBeenCalled()
    expect(props.onClose).toHaveBeenCalledOnce()
  })
})
