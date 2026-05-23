import { cleanup, render, screen } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { refreshFileTree, toggleHiddenFiles } from '~/lib/fileTreeOps'
import { DirectorySelector } from './DirectorySelector'

vi.mock('~/components/tree/DirectoryTree', () => ({
  DirectoryTree: () => <div data-testid="directory-tree" />,
}))

vi.mock('~/lib/browserStorage', () => ({
  KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN: 'directory-selector-show-hidden',
  localStorageGet: vi.fn(() => true),
  localStorageSet: vi.fn(),
}))

vi.mock('~/lib/shortcuts/display', () => ({
  shortcutHint: (label: string) => label,
}))

afterEach(() => {
  cleanup()
})

function makeState() {
  const refreshTree = vi.fn()
  return {
    state: {
      workerId: () => 'worker-1',
      setWorkerId: vi.fn(),
      workers: () => [],
      refreshWorkers: vi.fn(),
      workersRefreshing: () => false,
      workingDir: () => '/repo',
      setWorkingDir: vi.fn(),
    },
    tree: {
      treeKey: () => 0,
      setTreeRef: vi.fn(),
      refreshTree,
    },
    refreshTree,
  }
}

describe('directorySelector', () => {
  it('refreshFileTree invokes the current tree state refreshTree', () => {
    const { state, tree, refreshTree } = makeState()
    render(() => <DirectorySelector state={state as any} tree={tree as any} />)

    refreshFileTree()

    expect(refreshTree).toHaveBeenCalledOnce()
  })

  it('toggleHiddenFiles updates the visible button title through the registry callback', () => {
    const { state, tree } = makeState()
    render(() => <DirectorySelector state={state as any} tree={tree as any} />)

    expect(screen.getByRole('button', { name: 'Hide hidden files' })).toBeInTheDocument()

    toggleHiddenFiles()

    expect(screen.getByRole('button', { name: 'Show hidden files' })).toBeInTheDocument()
  })

  it('unregisters dialog ops on unmount', () => {
    const { state, tree, refreshTree } = makeState()
    const view = render(() => <DirectorySelector state={state as any} tree={tree as any} />)

    view.unmount()
    refreshFileTree()

    expect(refreshTree).not.toHaveBeenCalled()
  })
})
