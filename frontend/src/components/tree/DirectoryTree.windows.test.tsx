import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { DirectoryTree } from './DirectoryTree'

// Stub the listDirectory RPC — these tests only exercise the path-input.
vi.mock('~/api/workerRpc', () => ({
  listDirectory: vi.fn(async () => ({ entries: [], truncated: false })),
  channelManager: { subscribe: () => () => {} },
}))

function renderTree(props: {
  selectedPath: string
  homeDir: string
  onSelect: (path: string) => void
}) {
  return render(() => (
    <DirectoryTree
      workerId="w1"
      selectedPath={props.selectedPath}
      homeDir={props.homeDir}
      onSelect={props.onSelect}
      rootPath={props.homeDir}
    />
  ))
}

describe('directoryTree on a Windows worker', () => {
  it('expands ~\\Documents using the Windows homeDir', () => {
    const onSelect = vi.fn()
    renderTree({ selectedPath: 'C:\\Users\\test', homeDir: 'C:\\Users\\test', onSelect })
    const input = screen.getByPlaceholderText('Enter path...') as HTMLInputElement
    fireEvent.input(input, { target: { value: '~\\Documents' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSelect).toHaveBeenCalledWith('C:\\Users\\test\\Documents')
  })

  it('expands ~/Documents using the Windows homeDir (forward slash accepted)', () => {
    const onSelect = vi.fn()
    renderTree({ selectedPath: 'C:\\Users\\test', homeDir: 'C:\\Users\\test', onSelect })
    const input = screen.getByPlaceholderText('Enter path...') as HTMLInputElement
    fireEvent.input(input, { target: { value: '~/Documents' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSelect).toHaveBeenCalledWith('C:\\Users\\test\\Documents')
  })

  it('passes through an already-absolute Windows path', () => {
    const onSelect = vi.fn()
    renderTree({ selectedPath: 'C:\\Users\\test', homeDir: 'C:\\Users\\test', onSelect })
    const input = screen.getByPlaceholderText('Enter path...') as HTMLInputElement
    fireEvent.input(input, { target: { value: 'C:\\Windows\\System32' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSelect).toHaveBeenCalledWith('C:\\Windows\\System32')
  })

  it('shows a hint when a POSIX path is entered on a Windows worker', async () => {
    const onSelect = vi.fn()
    renderTree({ selectedPath: 'C:\\Users\\test', homeDir: 'C:\\Users\\test', onSelect })
    const input = screen.getByPlaceholderText('Enter path...') as HTMLInputElement
    fireEvent.input(input, { target: { value: '/home/alice/proj' } })
    const hint = await screen.findByTestId('path-flavor-hint')
    expect(hint.textContent).toMatch(/POSIX path/i)
  })
})

describe('directoryTree on a POSIX worker', () => {
  it('expands ~/proj using the POSIX homeDir', () => {
    const onSelect = vi.fn()
    renderTree({ selectedPath: '/home/alice', homeDir: '/home/alice', onSelect })
    const input = screen.getByPlaceholderText('Enter path...') as HTMLInputElement
    fireEvent.input(input, { target: { value: '~/proj' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSelect).toHaveBeenCalledWith('/home/alice/proj')
  })

  it('shows a hint when a Windows-looking path is entered on a POSIX worker', async () => {
    const onSelect = vi.fn()
    renderTree({ selectedPath: '/home/alice', homeDir: '/home/alice', onSelect })
    const input = screen.getByPlaceholderText('Enter path...') as HTMLInputElement
    fireEvent.input(input, { target: { value: 'C:\\Users\\alice' } })
    const hint = await screen.findByTestId('path-flavor-hint')
    expect(hint.textContent).toMatch(/Windows path/i)
  })

  it('does not show a hint for a matching POSIX path', () => {
    const onSelect = vi.fn()
    renderTree({ selectedPath: '/home/alice', homeDir: '/home/alice', onSelect })
    const input = screen.getByPlaceholderText('Enter path...') as HTMLInputElement
    fireEvent.input(input, { target: { value: '/opt/data' } })
    expect(screen.queryByTestId('path-flavor-hint')).toBeNull()
  })
})
