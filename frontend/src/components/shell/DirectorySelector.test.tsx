import { cleanup, render, screen } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { refreshFileTree, toggleHiddenFiles } from '~/lib/fileTreeOps'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { withPreferences } from '~/test-support/preferencesProvider'
import { DirectorySelector } from './DirectorySelector'

vi.mock('~/components/tree/DirectoryTree', () => ({
  DirectoryTree: (props: { showGitStatus?: boolean }) => (
    <div data-testid="directory-tree" data-show-git-status={String(props.showGitStatus ?? true)} />
  ),
}))

// Partial mock: keep the real key constants (modules in this import graph --
// e.g. relayClaim's persisted sequence -- reference them at module scope), and
// stub only the storage accessors this test drives.
vi.mock('~/lib/browserStorage', async importOriginal => ({
  ...(await importOriginal<typeof import('~/lib/browserStorage')>()),
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
    render(withPreferences(() => <DirectorySelector state={state as any} tree={tree as any} repoGitStore={createRepoGitStore()} />))

    refreshFileTree()

    expect(refreshTree).toHaveBeenCalledOnce()
  })

  it('toggleHiddenFiles updates the visible button title through the registry callback', () => {
    const { state, tree } = makeState()
    render(withPreferences(() => <DirectorySelector state={state as any} tree={tree as any} repoGitStore={createRepoGitStore()} />))

    expect(screen.getByRole('button', { name: 'Hide hidden files' })).toBeInTheDocument()

    toggleHiddenFiles()

    expect(screen.getByRole('button', { name: 'Show hidden files' })).toBeInTheDocument()
  })

  it('unregisters dialog ops on unmount', () => {
    const { state, tree, refreshTree } = makeState()
    const view = render(withPreferences(() => <DirectorySelector state={state as any} tree={tree as any} repoGitStore={createRepoGitStore()} />))

    view.unmount()
    refreshFileTree()

    expect(refreshTree).not.toHaveBeenCalled()
  })

  it('disables git status decorations in the picker tree', () => {
    const { state, tree } = makeState()
    render(withPreferences(() => <DirectorySelector state={state as any} tree={tree as any} repoGitStore={createRepoGitStore()} />))

    expect(screen.getByTestId('directory-tree')).toHaveAttribute('data-show-git-status', 'false')
  })
})
