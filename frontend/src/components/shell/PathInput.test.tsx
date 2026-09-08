import type { PathFlavor } from '~/lib/paths'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { PathInput } from './PathInput'

// The component under test owns nothing but the input and its hint, so it
// needs none of the RPC / preferences / platform-bridge mocks the tree does.

function renderInput(props: {
  selectedPath: string
  homeDir: string
  flavor: PathFlavor
  onSubmit: (path: string) => void
}) {
  return render(() => (
    <PathInput
      selectedPath={props.selectedPath}
      homeDir={props.homeDir}
      flavor={props.flavor}
      onSubmit={props.onSubmit}
    />
  ))
}

function pathInput(): HTMLInputElement {
  return screen.getByPlaceholderText('Enter path...') as HTMLInputElement
}

describe('pathInput on a Windows worker', () => {
  const windowsProps = { selectedPath: 'C:\\Users\\test', homeDir: 'C:\\Users\\test', flavor: 'win32' as const }

  it('expands ~\\Documents using the Windows homeDir', () => {
    const onSubmit = vi.fn()
    renderInput({ ...windowsProps, onSubmit })
    const input = pathInput()
    fireEvent.input(input, { target: { value: '~\\Documents' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSubmit).toHaveBeenCalledWith('C:\\Users\\test\\Documents')
  })

  it('expands ~/Documents using the Windows homeDir (forward slash accepted)', () => {
    const onSubmit = vi.fn()
    renderInput({ ...windowsProps, onSubmit })
    const input = pathInput()
    fireEvent.input(input, { target: { value: '~/Documents' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSubmit).toHaveBeenCalledWith('C:\\Users\\test\\Documents')
  })

  it('passes through an already-absolute Windows path', () => {
    const onSubmit = vi.fn()
    renderInput({ ...windowsProps, onSubmit })
    const input = pathInput()
    fireEvent.input(input, { target: { value: 'C:\\Windows\\System32' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSubmit).toHaveBeenCalledWith('C:\\Windows\\System32')
  })

  it('shows a hint when a POSIX path is entered on a Windows worker', async () => {
    const onSubmit = vi.fn()
    renderInput({ ...windowsProps, onSubmit })
    fireEvent.input(pathInput(), { target: { value: '/home/alice/proj' } })
    const hint = await screen.findByTestId('path-flavor-hint')
    expect(hint.textContent).toMatch(/POSIX path/i)
  })
})

describe('pathInput on a POSIX worker', () => {
  const posixProps = { selectedPath: '/home/alice', homeDir: '/home/alice', flavor: 'posix' as const }

  it('expands ~/proj using the POSIX homeDir', () => {
    const onSubmit = vi.fn()
    renderInput({ ...posixProps, onSubmit })
    const input = pathInput()
    fireEvent.input(input, { target: { value: '~/proj' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSubmit).toHaveBeenCalledWith('/home/alice/proj')
  })

  it('shows a hint when a Windows-looking path is entered on a POSIX worker', async () => {
    const onSubmit = vi.fn()
    renderInput({ ...posixProps, onSubmit })
    fireEvent.input(pathInput(), { target: { value: 'C:\\Users\\alice' } })
    const hint = await screen.findByTestId('path-flavor-hint')
    expect(hint.textContent).toMatch(/Windows path/i)
  })

  it('does not show a hint for a matching POSIX path', () => {
    const onSubmit = vi.fn()
    renderInput({ ...posixProps, onSubmit })
    fireEvent.input(pathInput(), { target: { value: '/opt/data' } })
    expect(screen.queryByTestId('path-flavor-hint')).toBeNull()
  })
})

describe('pathInput submission', () => {
  const posixProps = { selectedPath: '/home/alice', homeDir: '/home/alice', flavor: 'posix' as const }

  it('shows the selected path tildified', () => {
    renderInput({ ...posixProps, selectedPath: '/home/alice/proj', onSubmit: vi.fn() })
    expect(pathInput().value).toBe('~/proj')
  })

  it('submits on blur', () => {
    const onSubmit = vi.fn()
    renderInput({ ...posixProps, onSubmit })
    const input = pathInput()
    fireEvent.input(input, { target: { value: '~/other' } })
    fireEvent.blur(input)
    expect(onSubmit).toHaveBeenCalledWith('/home/alice/other')
  })

  it('does not re-emit the displayed value on blur', () => {
    const onSubmit = vi.fn()
    renderInput({ ...posixProps, selectedPath: '/home/alice/proj', onSubmit })
    fireEvent.blur(pathInput())
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('ignores an empty or whitespace-only value', () => {
    const onSubmit = vi.fn()
    renderInput({ ...posixProps, onSubmit })
    const input = pathInput()
    fireEvent.input(input, { target: { value: '   ' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    fireEvent.blur(input)
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('trims surrounding whitespace before expanding', () => {
    const onSubmit = vi.fn()
    renderInput({ ...posixProps, onSubmit })
    const input = pathInput()
    fireEvent.input(input, { target: { value: '  ~/proj  ' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSubmit).toHaveBeenCalledWith('/home/alice/proj')
  })

  it('ignores keys other than Enter', () => {
    const onSubmit = vi.fn()
    renderInput({ ...posixProps, onSubmit })
    const input = pathInput()
    fireEvent.input(input, { target: { value: '~/proj' } })
    fireEvent.keyDown(input, { key: 'a' })
    expect(onSubmit).not.toHaveBeenCalled()
  })
})

describe('pathInput leading slot', () => {
  const base = { selectedPath: '/home/alice', homeDir: '/home/alice', flavor: 'posix' as const }

  function renderWithSlot(leading?: unknown) {
    return render(() => (
      <PathInput
        selectedPath={base.selectedPath}
        homeDir={base.homeDir}
        flavor={base.flavor}
        onSubmit={() => {}}
        leading={leading as never}
      />
    ))
  }

  it('renders the slot immediately before the input inside the row', () => {
    renderWithSlot(<span data-testid="slot">C:\\</span>)

    const input = screen.getByPlaceholderText('Enter path...')
    const slot = screen.getByTestId('slot')
    // A DOM reorder that put the drive to the RIGHT of the input would still
    // look correct in a screenshot of a short path, so pin the order.
    expect(slot.compareDocumentPosition(input) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(slot.parentElement).toBe(input.closest('div'))
  })

  it('renders no leading box when the slot is omitted', () => {
    renderWithSlot(undefined)

    expect(screen.queryByTestId('slot')).toBeNull()
    expect(screen.getByPlaceholderText('Enter path...')).toBeInTheDocument()
  })

  /**
   * The reason the slot lives inside this component rather than in a wrapper
   * the caller builds: the hint belongs UNDER the whole row. A caller that
   * wrapped the drive control and this input in a row of its own would trap
   * the hint inside that row, beside the input.
   */
  it('keeps the flavor hint below the row, not inside it', async () => {
    const view = render(() => (
      <PathInput
        selectedPath="/home/alice"
        homeDir="/home/alice"
        flavor="win32"
        onSubmit={() => {}}
        leading={<span data-testid="slot">C:\\</span>}
      />
    ))

    const input = screen.getByPlaceholderText('Enter path...')
    fireEvent.input(input, { target: { value: '/etc/hosts' } })

    const hint = await screen.findByTestId('path-flavor-hint')
    const row = screen.getByTestId('slot').parentElement!
    expect(row.contains(hint)).toBe(false)
    view.unmount()
  })
})
