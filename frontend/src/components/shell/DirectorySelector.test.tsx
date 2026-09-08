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
  const expandTreePath = vi.fn()
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
      expandTreePath,
    },
    refreshTree,
    expandTreePath,
  }
}

/**
 * Render the picker, and hand back everything a test in this file asserts on.
 *
 * `workingDir` takes a string or an accessor: one test drives a signal through
 * it to watch the tree re-root. Omit it to keep `makeState`'s own `/repo`.
 */
function renderSelector(workingDir?: string | (() => string)) {
  const { state, tree, refreshTree, expandTreePath } = makeState()
  if (workingDir !== undefined)
    state.workingDir = typeof workingDir === 'function' ? workingDir : () => workingDir
  const view = render(withPreferences(() => (
    <DirectorySelector state={state as any} tree={tree as any} repoGitStore={createRepoGitStore()} />
  )))
  return { ...view, state, tree, refreshTree, expandTreePath }
}

describe('directorySelector', () => {
  it('refreshFileTree invokes the current tree state refreshTree', () => {
    const { refreshTree } = renderSelector()

    refreshFileTree()

    expect(refreshTree).toHaveBeenCalledOnce()
  })

  it('toggleHiddenFiles updates the visible button title through the registry callback', () => {
    renderSelector()

    expect(screen.getByRole('button', { name: 'Hide hidden files' })).toBeInTheDocument()

    toggleHiddenFiles()

    expect(screen.getByRole('button', { name: 'Show hidden files' })).toBeInTheDocument()
  })

  it('unregisters dialog ops on unmount', () => {
    const { unmount, refreshTree } = renderSelector()

    unmount()
    refreshFileTree()

    expect(refreshTree).not.toHaveBeenCalled()
  })

  it('disables git status decorations in the picker tree', () => {
    renderSelector()

    expect(screen.getByTestId('directory-tree')).toHaveAttribute('data-show-git-status', 'false')
  })
})

describe('directorySelector root derivation', () => {
  it('roots the tree at the filesystem root of the selected path', () => {
    renderSelector('/repo/sub')

    expect(screen.getByTestId('directory-tree')).toHaveAttribute('data-root-path', '/')
  })

  it('falls back to the worker home directory when nothing is selected', () => {
    workerOs.mockReturnValue('windows')
    workerHome.mockReturnValue('D:\\Users\\alice')

    renderSelector('')

    expect(screen.getByTestId('directory-tree')).toHaveAttribute('data-root-path', 'D:\\')
  })

  it('passes the worker home directory as the tree reveal path', () => {
    renderSelector('')

    expect(screen.getByTestId('directory-tree')).toHaveAttribute('data-reveal-path', '/home/alice')
  })

  /**
   * The single-source-of-truth test. The root is DERIVED from the selection,
   * so there is no drive signal that could disagree with the path box.
   */
  it('re-roots the tree when the selection changes drive', async () => {
    workerOs.mockReturnValue('windows')
    workerHome.mockReturnValue('C:\\Users\\alice')
    const [dir, setDir] = createSignal('C:\\a')
    // eslint-disable-next-line solid/reactivity -- renderSelector stores the accessor on the state object the component reads, which IS a tracked scope
    renderSelector(dir)

    expect(screen.getByTestId('directory-tree')).toHaveAttribute('data-root-path', 'C:\\')

    setDir('D:\\b')
    await waitFor(() => expect(screen.getByTestId('directory-tree')).toHaveAttribute('data-root-path', 'D:\\'))
  })

  // `filesystemRoot` already knows a POSIX worker has exactly one root, so the
  // round trip would buy nothing. This is also what keeps WSL and Docker
  // workers out of the Windows path: both report `linux`.
  it('never asks a posix worker for its roots', () => {
    renderSelector('/repo')

    expect(listFilesystemRoots).not.toHaveBeenCalled()
    expect(screen.getByTestId('directory-tree')).toHaveAttribute('data-root-path', '/')
  })

  // The last link in the fallback chain: a Windows worker whose home has not
  // arrived still has somewhere to root once it reports its drives.
  it('falls back to the first reported root when the home directory is unknown', async () => {
    workerOs.mockReturnValue('windows')
    workerHome.mockReturnValue('')
    listFilesystemRoots.mockResolvedValue({ roots: ['E:\\', 'F:\\'] })

    renderSelector('')

    await waitFor(() => expect(screen.getByTestId('directory-tree')).toHaveAttribute('data-root-path', 'E:\\'))
  })

  it('renders a placeholder instead of a tree while a windows root is unknown', () => {
    workerOs.mockReturnValue('windows')
    workerHome.mockReturnValue('')

    renderSelector('')

    expect(screen.queryByTestId('directory-tree')).toBeNull()
    expect(screen.getByText('Loading drives…')).toBeInTheDocument()
  })

  /**
   * `flavorFromOs(undefined)` answers `'posix'`, and that answer now picks the
   * tree's ROOT. A Windows worker whose info has not arrived would mount at
   * `/`, issue a ListDirectory the worker refuses, and paint an error over the
   * whole pane before re-rooting at `C:\`. "Unknown" has to be its own state.
   */
  it('waits rather than rooting at / while the worker os is unknown', () => {
    workerOs.mockReturnValue(undefined)
    workerHome.mockReturnValue('')

    renderSelector('')

    expect(screen.queryByTestId('directory-tree')).toBeNull()
  })

  // An unknown OS does not stop a root the PATH itself states. `filesystemRoot`
  // sniffs the flavor, so a prefilled selection roots immediately either way.
  it('still roots from the selection while the worker os is unknown', () => {
    workerOs.mockReturnValue(undefined)
    workerHome.mockReturnValue('')

    renderSelector('C:\\proj\\app')

    expect(screen.getByTestId('directory-tree')).toHaveAttribute('data-root-path', 'C:\\')
  })

  it('roots a posix selection while the worker os is unknown', () => {
    workerOs.mockReturnValue(undefined)
    workerHome.mockReturnValue('')

    renderSelector('/repo/sub')

    expect(screen.getByTestId('directory-tree')).toHaveAttribute('data-root-path', '/')
  })

  // Without an onError the picker showed "Loading drives…" for a fetch that
  // had already failed, and only the Refresh button escaped -- with nothing on
  // screen saying to press it.
  it('reports a failed drive listing instead of a permanent loading state', async () => {
    workerOs.mockReturnValue('windows')
    workerHome.mockReturnValue('')
    listFilesystemRoots.mockRejectedValue(new Error('worker offline'))

    renderSelector('')

    await waitFor(() => expect(screen.getByTestId('directory-selector-no-root')).toHaveTextContent(/worker offline/))
    expect(screen.queryByText('Loading drives…')).toBeNull()
  })
})

describe('directorySelector drive menu', () => {
  it('shows the drive selector for a windows worker with more than one root', async () => {
    workerOs.mockReturnValue('windows')
    workerHome.mockReturnValue('C:\\Users\\alice')
    listFilesystemRoots.mockResolvedValue({ roots: ['C:\\', 'D:\\'] })

    renderSelector('')

    await waitFor(() => expect(screen.getByTestId('drive-selector-trigger')).toBeInTheDocument())
  })

  it('hides the drive selector when the worker reports a single root', async () => {
    workerOs.mockReturnValue('windows')
    workerHome.mockReturnValue('C:\\Users\\alice')
    listFilesystemRoots.mockResolvedValue({ roots: ['C:\\'] })

    renderSelector('')

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

    renderSelector('')

    await Promise.resolve()
    expect(screen.queryByTestId('drive-selector-trigger')).toBeNull()
    expect(listFilesystemRoots).not.toHaveBeenCalled()
  })

  it('makes the chosen drive the working directory', async () => {
    workerOs.mockReturnValue('windows')
    workerHome.mockReturnValue('C:\\Users\\alice')
    listFilesystemRoots.mockResolvedValue({ roots: ['C:\\', 'D:\\'] })

    const { state } = renderSelector('')
    await waitFor(() => expect(screen.getByTestId('drive-option-d')).toBeInTheDocument())

    fireEvent.click(screen.getByTestId('drive-option-d'))

    expect(state.setWorkingDir).toHaveBeenCalledWith('D:\\')
  })

  it('refreshes the drive list along with the tree', async () => {
    workerOs.mockReturnValue('windows')
    workerHome.mockReturnValue('C:\\Users\\alice')
    listFilesystemRoots.mockResolvedValue({ roots: ['C:\\', 'D:\\'] })
    const { refreshTree } = renderSelector('')
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
    const { refreshTree } = renderSelector('')
    await waitFor(() => expect(listFilesystemRoots).toHaveBeenCalledTimes(1))

    fireEvent.click(screen.getByTestId('directory-selector-refresh'))

    expect(refreshTree).toHaveBeenCalledOnce()
    await waitFor(() => expect(listFilesystemRoots).toHaveBeenCalledTimes(2))
  })
})

describe('directorySelector home button', () => {
  it('selects the home directory and opens it', () => {
    const { state, expandTreePath } = renderSelector()

    fireEvent.click(screen.getByTestId('directory-selector-home'))

    expect(state.setWorkingDir).toHaveBeenCalledWith('/home/alice')
    expect(expandTreePath).toHaveBeenCalledWith('/home/alice')
  })

  // The selection must land FIRST. It can re-root the tree, and a new root
  // replaces the whole expansion state, so the reverse order loses the
  // expansion this button exists to produce.
  it('selects before it expands', () => {
    const calls: string[] = []
    const { state, expandTreePath } = renderSelector()
    vi.mocked(state.setWorkingDir).mockImplementation(() => {
      calls.push('select')
    })
    expandTreePath.mockImplementation(() => {
      calls.push('expand')
    })

    fireEvent.click(screen.getByTestId('directory-selector-home'))

    expect(calls).toEqual(['select', 'expand'])
  })

  // A worker that has not reported yet has no home directory to go to, and
  // selecting '' would clear the working directory instead.
  it('is disabled, and does nothing, while the home directory is unknown', () => {
    workerHome.mockReturnValue('')
    const { state, expandTreePath } = renderSelector()

    const button = screen.getByTestId('directory-selector-home')
    expect(button).toBeDisabled()

    fireEvent.click(button)

    expect(state.setWorkingDir).not.toHaveBeenCalled()
    expect(expandTreePath).not.toHaveBeenCalled()
  })

  // The home directory can live on a drive the picker is not showing. The
  // selection is what re-roots the tree, which is the other reason the button
  // selects before it expands.
  it('re-roots onto the home drive when the home directory is on another one', async () => {
    workerOs.mockReturnValue('windows')
    workerHome.mockReturnValue('C:\\Users\\alice')
    listFilesystemRoots.mockResolvedValue({ roots: ['C:\\', 'D:\\'] })

    const [workingDir, setWorkingDir] = createSignal('D:\\work')
    const { state, tree } = makeState()
    state.workingDir = workingDir
    state.setWorkingDir = vi.fn((path: string) => setWorkingDir(path))
    render(withPreferences(() => (
      <DirectorySelector state={state as any} tree={tree as any} repoGitStore={createRepoGitStore()} />
    )))
    await waitFor(() => expect(screen.getByTestId('directory-tree').getAttribute('data-root-path')).toBe('D:\\'))

    fireEvent.click(screen.getByTestId('directory-selector-home'))

    await waitFor(() => expect(screen.getByTestId('directory-tree').getAttribute('data-root-path')).toBe('C:\\'))
    expect(tree.expandTreePath).toHaveBeenCalledWith('C:\\Users\\alice')
  })

  it('sits between the hidden-files toggle and the refresh button', () => {
    renderSelector()

    const ids = screen.getAllByRole('button')
      .map(b => b.getAttribute('data-testid'))
      .filter((id): id is string => id !== null && id.startsWith('directory-selector-'))

    expect(ids).toEqual([
      'directory-selector-show-hidden-toggle',
      'directory-selector-home',
      'directory-selector-refresh',
    ])
  })
})
