import { cleanup, fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { refreshFileTree, toggleHiddenFiles } from '~/lib/fileTreeOps'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { withPreferences } from '~/test-support/preferencesProvider'
import { DirectorySelector } from './DirectorySelector'

vi.mock('~/components/tree/DirectoryTree', () => ({
  DirectoryTree: (props: {
    showGitStatus?: boolean
    rootPath?: string
    revealPath?: string
    flavor?: string
  }) => (
    <div
      data-testid="directory-tree"
      data-show-git-status={String(props.showGitStatus ?? true)}
      data-root-path={props.rootPath}
      data-reveal-path={props.revealPath}
      data-flavor={props.flavor}
    />
  ),
}))

const listFilesystemRoots = vi.fn<(...args: unknown[]) => Promise<{ roots: string[] }>>(async () => ({ roots: [] }))
vi.mock('~/api/workerRpc', () => ({
  listFilesystemRoots: (...args: unknown[]) => listFilesystemRoots(...args),
}))

const workerOs = vi.fn<() => string | undefined>(() => 'linux')
const workerHome = vi.fn(() => '/home/alice')
vi.mock('~/stores/workerInfo.store', () => ({
  workerInfoStore: {
    getOs: () => workerOs(),
    getHomeDir: () => workerHome(),
    workerInfo: () => null,
    fetchWorkerInfo: async () => null,
  },
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

beforeEach(() => {
  listFilesystemRoots.mockReset()
  listFilesystemRoots.mockResolvedValue({ roots: [] })
  workerOs.mockReturnValue('linux')
  workerHome.mockReturnValue('/home/alice')
})

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

describe('directorySelector root derivation', () => {
  function renderWith(workingDir: string) {
    const { state, tree } = makeState()
    state.workingDir = () => workingDir
    render(withPreferences(() => (
      <DirectorySelector state={state as any} tree={tree as any} repoGitStore={createRepoGitStore()} />
    )))
    return state
  }

  it('roots the tree at the filesystem root of the selected path', () => {
    renderWith('/repo/sub')

    expect(screen.getByTestId('directory-tree')).toHaveAttribute('data-root-path', '/')
  })

  it('falls back to the worker home directory when nothing is selected', () => {
    workerOs.mockReturnValue('windows')
    workerHome.mockReturnValue('D:\\Users\\alice')

    renderWith('')

    expect(screen.getByTestId('directory-tree')).toHaveAttribute('data-root-path', 'D:\\')
  })

  it('passes the worker home directory as the tree reveal path', () => {
    renderWith('')

    expect(screen.getByTestId('directory-tree')).toHaveAttribute('data-reveal-path', '/home/alice')
  })

  /**
   * The single-source-of-truth test. The root is DERIVED from the selection,
   * so there is no drive signal that could disagree with the path box.
   */
  it('re-roots the tree when the selection changes drive', async () => {
    workerOs.mockReturnValue('windows')
    workerHome.mockReturnValue('C:\\Users\\alice')
    const { state, tree } = makeState()
    const [dir, setDir] = createSignal('C:\\a')
    state.workingDir = dir
    render(withPreferences(() => (
      <DirectorySelector state={state as any} tree={tree as any} repoGitStore={createRepoGitStore()} />
    )))

    expect(screen.getByTestId('directory-tree')).toHaveAttribute('data-root-path', 'C:\\')

    setDir('D:\\b')
    await waitFor(() => expect(screen.getByTestId('directory-tree')).toHaveAttribute('data-root-path', 'D:\\'))
  })

  // `filesystemRoot` already knows a POSIX worker has exactly one root, so the
  // round trip would buy nothing. This is also what keeps WSL and Docker
  // workers out of the Windows path: both report `linux`.
  it('never asks a posix worker for its roots', () => {
    renderWith('/repo')

    expect(listFilesystemRoots).not.toHaveBeenCalled()
    expect(screen.getByTestId('directory-tree')).toHaveAttribute('data-root-path', '/')
  })

  // The last link in the fallback chain: a Windows worker whose home has not
  // arrived still has somewhere to root once it reports its drives.
  it('falls back to the first reported root when the home directory is unknown', async () => {
    workerOs.mockReturnValue('windows')
    workerHome.mockReturnValue('')
    listFilesystemRoots.mockResolvedValue({ roots: ['E:\\', 'F:\\'] })

    renderWith('')

    await waitFor(() => expect(screen.getByTestId('directory-tree')).toHaveAttribute('data-root-path', 'E:\\'))
  })

  it('renders a placeholder instead of a tree while a windows root is unknown', () => {
    workerOs.mockReturnValue('windows')
    workerHome.mockReturnValue('')

    renderWith('')

    expect(screen.queryByTestId('directory-tree')).toBeNull()
    expect(screen.getByText('Loading drives…')).toBeInTheDocument()
  })
})

describe('directorySelector drive menu', () => {
  function renderPicker(workingDir = '') {
    const { state, tree } = makeState()
    state.workingDir = () => workingDir
    render(withPreferences(() => (
      <DirectorySelector state={state as any} tree={tree as any} repoGitStore={createRepoGitStore()} />
    )))
    return state
  }

  it('shows the drive selector for a windows worker with more than one root', async () => {
    workerOs.mockReturnValue('windows')
    workerHome.mockReturnValue('C:\\Users\\alice')
    listFilesystemRoots.mockResolvedValue({ roots: ['C:\\', 'D:\\'] })

    renderPicker()

    await waitFor(() => expect(screen.getByTestId('drive-selector-trigger')).toBeInTheDocument())
  })

  it('hides the drive selector when the worker reports a single root', async () => {
    workerOs.mockReturnValue('windows')
    workerHome.mockReturnValue('C:\\Users\\alice')
    listFilesystemRoots.mockResolvedValue({ roots: ['C:\\'] })

    renderPicker()

    await waitFor(() => expect(listFilesystemRoots).toHaveBeenCalled())
    expect(screen.queryByTestId('drive-selector-trigger')).toBeNull()
  })

  /**
   * A WSL worker's home looks like a Windows mount, but the worker itself
   * reports `linux` and really does have a single `/`. Keying on the reported
   * OS is what excludes it, and this pins that.
   */
  it('hides the drive selector for a linux worker whose home looks like a windows mount', async () => {
    workerOs.mockReturnValue('linux')
    workerHome.mockReturnValue('/mnt/c/Users/alice')

    renderPicker()

    await Promise.resolve()
    expect(screen.queryByTestId('drive-selector-trigger')).toBeNull()
    expect(listFilesystemRoots).not.toHaveBeenCalled()
  })

  it('makes the chosen drive the working directory', async () => {
    workerOs.mockReturnValue('windows')
    workerHome.mockReturnValue('C:\\Users\\alice')
    listFilesystemRoots.mockResolvedValue({ roots: ['C:\\', 'D:\\'] })

    const state = renderPicker()
    await waitFor(() => expect(screen.getByTestId('drive-option-d')).toBeInTheDocument())

    fireEvent.click(screen.getByTestId('drive-option-d'))

    expect(state.setWorkingDir).toHaveBeenCalledWith('D:\\')
  })

  it('refreshes the drive list along with the tree', async () => {
    workerOs.mockReturnValue('windows')
    workerHome.mockReturnValue('C:\\Users\\alice')
    listFilesystemRoots.mockResolvedValue({ roots: ['C:\\', 'D:\\'] })
    const { state, tree, refreshTree } = makeState()
    state.workingDir = () => ''
    render(withPreferences(() => (
      <DirectorySelector state={state as any} tree={tree as any} repoGitStore={createRepoGitStore()} />
    )))
    await waitFor(() => expect(listFilesystemRoots).toHaveBeenCalledTimes(1))

    refreshFileTree()

    expect(refreshTree).toHaveBeenCalledOnce()
    await waitFor(() => expect(listFilesystemRoots).toHaveBeenCalledTimes(2))
  })

  // The other entry point. The button and the keyboard shortcut must not
  // disagree, or a user ends up with a re-listed tree beside a stale menu.
  it('refreshes the drive list from the refresh button', async () => {
    workerOs.mockReturnValue('windows')
    workerHome.mockReturnValue('C:\\Users\\alice')
    listFilesystemRoots.mockResolvedValue({ roots: ['C:\\', 'D:\\'] })
    const { state, tree, refreshTree } = makeState()
    state.workingDir = () => ''
    render(withPreferences(() => (
      <DirectorySelector state={state as any} tree={tree as any} repoGitStore={createRepoGitStore()} />
    )))
    await waitFor(() => expect(listFilesystemRoots).toHaveBeenCalledTimes(1))

    fireEvent.click(screen.getByTestId('directory-selector-refresh'))

    expect(refreshTree).toHaveBeenCalledOnce()
    await waitFor(() => expect(listFilesystemRoots).toHaveBeenCalledTimes(2))
  })
})
