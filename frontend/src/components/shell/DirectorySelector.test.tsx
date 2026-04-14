import { cleanup, render, screen } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { refreshFileTree, toggleHiddenFiles } from '~/lib/fileTreeOps'
import { DirectorySelector } from './DirectorySelector'

vi.mock('~/components/tree/DirectoryTree', () => ({
  DirectoryTree: () => <div data-testid="directory-tree" />,
}))

vi.mock('~/lib/browserStorage', () => ({
  KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN: 'directory-selector-show-hidden',
  safeGetJson: vi.fn(() => true),
  safeSetJson: vi.fn(),
}))

vi.mock('~/lib/shortcuts/display', () => ({
  shortcutHint: (label: string) => label,
}))

afterEach(() => {
  cleanup()
})

function makeState() {
  return {
    refreshTree: vi.fn(),
    workerId: () => 'worker-1',
    workingDir: () => '/repo',
    setWorkingDir: vi.fn(),
    workerInfoStore: { getHomeDir: vi.fn(() => '/home/user') },
    treeRef: vi.fn(),
  }
}

describe('directorySelector', () => {
  it('refreshFileTree invokes the current dialog state refreshTree', () => {
    const state = makeState()
    render(() => <DirectorySelector state={state as any} />)

    refreshFileTree()

    expect(state.refreshTree).toHaveBeenCalledOnce()
  })

  it('toggleHiddenFiles updates the visible button title through the registry callback', () => {
    const state = makeState()
    render(() => <DirectorySelector state={state as any} />)

    expect(screen.getByRole('button', { name: 'Hide hidden files' })).toBeInTheDocument()

    toggleHiddenFiles()

    expect(screen.getByRole('button', { name: 'Show hidden files' })).toBeInTheDocument()
  })

  it('unregisters dialog ops on unmount', () => {
    const state = makeState()
    const view = render(() => <DirectorySelector state={state as any} />)

    view.unmount()
    refreshFileTree()

    expect(state.refreshTree).not.toHaveBeenCalled()
  })
})
