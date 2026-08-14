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
