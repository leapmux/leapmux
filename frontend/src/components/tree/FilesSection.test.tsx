import type { FilesSectionHandle } from './FilesSection'
import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
import type { createGitFileStatusStore, GitFilterTab } from '~/stores/gitFileStatus.store'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import { localStorageGet, localStorageSet, PREFIX_FILES_SORT_ORDER } from '~/lib/browserStorage'
import { DEFAULT_FILE_SORT_ORDER } from '~/lib/fileSort'
import { FilesSection, FilesSectionHeaderActions } from './FilesSection'

vi.mock('~/api/workerRpc', () => ({
  listDirectory: vi.fn(async () => ({ entries: [], truncated: false })),
  statFile: vi.fn(async () => ({ info: { modTime: '2026-01-01T00:00:00Z' } })),
  channelManager: { subscribe: () => () => {} },
}))

const WORKER_ID = 'w1'
const WORKING_DIR = '/repo'

interface EntryInit {
  path: string
  size?: bigint
  modTime?: string
  staged?: boolean
  isDir?: boolean
}

function gitEntry(init: EntryInit): GitFileStatusEntry {
  return {
    path: init.path,
    stagedStatus: init.staged ? GitFileStatusCode.MODIFIED : GitFileStatusCode.UNSPECIFIED,
    unstagedStatus: init.staged ? GitFileStatusCode.UNSPECIFIED : GitFileStatusCode.MODIFIED,
    linesAdded: 0,
    linesDeleted: 0,
    stagedLinesAdded: 0,
    stagedLinesDeleted: 0,
    oldPath: '',
    isDir: init.isDir ?? false,
    size: init.size,
    modTime: init.modTime,
  } as GitFileStatusEntry
}

/**
 * The narrow slice of the git status store FilesSection reads. A real store
 * would need an RPC round trip to hold any entries, and this test is about the
 * ordering the section applies to whatever the store returns.
 */
function fakeGitStore(files: GitFileStatusEntry[]): ReturnType<typeof createGitFileStatusStore> {
  return {
    state: { isGitRepo: true, repoRoot: WORKING_DIR, toplevel: WORKING_DIR, files },
    statusRoot: () => WORKING_DIR,
    getChangedFiles: (_filter: GitFilterTab) => files,
    getFileStatus: () => undefined,
    getNodeDiffStats: () => null,
    hasChanges: () => false,
  } as unknown as ReturnType<typeof createGitFileStatusStore>
}

function renderSection(files: GitFileStatusEntry[]) {
  let handle!: FilesSectionHandle
  const result = render(() => (
    <FilesSection
      workerId={WORKER_ID}
      workingDir={WORKING_DIR}
      homeDir="/home/alice"
      flavor="posix"
      fileTreePath={WORKING_DIR}
      onFileSelect={() => {}}
      gitStatusStore={fakeGitStore(files)}
      hasActiveFileTab={false}
      ref={(h) => { handle = h }}
    />
  ))
  return { ...result, handle: () => handle }
}

function flatListNames(): string[] {
  const list = document.querySelector('[data-testid="files-flat-list"]')
  return [...(list?.children ?? [])].map(el => el.textContent ?? '')
}

const FILES = [
  gitEntry({ path: 'src/apple.ts', size: 900n, modTime: '2020-01-01T00:00:00Z' }),
  gitEntry({ path: 'src/banana.md', size: 10n, modTime: '2026-06-01T00:00:00Z' }),
  gitEntry({ path: 'src/cherry.js', size: 100n, modTime: '2023-01-01T00:00:00Z' }),
]

beforeEach(() => {
  localStorage.clear()
})

describe('filesSection sort preference', () => {
  it('defaults to name ascending', () => {
    const { handle } = renderSection([])
    expect(handle().sortOrder()).toEqual(DEFAULT_FILE_SORT_ORDER)
  })

  it('persists a change under the worker and working-directory key', async () => {
    const { handle } = renderSection([])
    handle().setSortOrder({ key: 'size', direction: 'desc' })
    await waitFor(() => expect(
      localStorageGet(`${PREFIX_FILES_SORT_ORDER}${WORKER_ID}:${WORKING_DIR}`),
    ).toEqual({ key: 'size', direction: 'desc' }))
  })

  it('restores a stored preference on mount', () => {
    localStorageSet(`${PREFIX_FILES_SORT_ORDER}${WORKER_ID}:${WORKING_DIR}`, { key: 'type', direction: 'desc' })
    const { handle } = renderSection([])
    expect(handle().sortOrder()).toEqual({ key: 'type', direction: 'desc' })
  })

  it('falls back to the default when the stored value is not a sort order', () => {
    localStorageSet(`${PREFIX_FILES_SORT_ORDER}${WORKER_ID}:${WORKING_DIR}`, { key: 'colour', direction: 'sideways' })
    const { handle } = renderSection([])
    expect(handle().sortOrder()).toEqual(DEFAULT_FILE_SORT_ORDER)
  })
})

describe('filesSection flat list ordering', () => {
  // The flat list only replaces the tree while a git filter is active, so the
  // tab has to move off "All" first.
  function openFlatList(handle: () => FilesSectionHandle) {
    fireEvent.click(screen.getByTestId('files-filter-changed'))
    handle().toggleFlatListMode()
  }

  it('orders by path ascending by default', async () => {
    const { handle } = renderSection(FILES)
    openFlatList(handle)
    await waitFor(() => expect(flatListNames()).toHaveLength(3))
    expect(flatListNames()).toEqual(['src/apple.ts', 'src/banana.md', 'src/cherry.js'])
  })

  it('orders by size', async () => {
    const { handle } = renderSection(FILES)
    openFlatList(handle)
    handle().setSortOrder({ key: 'size', direction: 'asc' })
    await waitFor(() => expect(flatListNames()[0]).toBe('src/banana.md'))
    expect(flatListNames()).toEqual(['src/banana.md', 'src/cherry.js', 'src/apple.ts'])
  })

  it('orders by modification time, newest first', async () => {
    const { handle } = renderSection(FILES)
    openFlatList(handle)
    handle().setSortOrder({ key: 'modified', direction: 'desc' })
    await waitFor(() => expect(flatListNames()[0]).toBe('src/banana.md'))
    expect(flatListNames()).toEqual(['src/banana.md', 'src/cherry.js', 'src/apple.ts'])
  })

  it('orders by extension', async () => {
    const { handle } = renderSection(FILES)
    openFlatList(handle)
    handle().setSortOrder({ key: 'type', direction: 'asc' })
    await waitFor(() => expect(flatListNames()[0]).toBe('src/cherry.js'))
    expect(flatListNames()).toEqual(['src/cherry.js', 'src/banana.md', 'src/apple.ts'])
  })

  /**
   * Git collapses a wholly untracked subtree into one "build/" row. It stands
   * for a directory, so it groups first and keeps path order, exactly as a
   * directory does in the tree.
   */
  it('groups an untracked-directory row first and leaves it in path order', async () => {
    const { handle } = renderSection([
      ...FILES,
      gitEntry({ path: 'zzz-build/' }),
    ])
    openFlatList(handle)
    handle().setSortOrder({ key: 'size', direction: 'desc' })
    await waitFor(() => expect(flatListNames()).toHaveLength(4))
    expect(flatListNames()).toEqual(['zzz-build/', 'src/apple.ts', 'src/cherry.js', 'src/banana.md'])
  })

  /**
   * A submodule is a DIRECTORY that git names without a trailing slash, so the
   * slash convention never caught it. Before the worker reported `is_dir` it
   * grouped with the files and, carrying no stat, pinned to the bottom next to
   * genuinely deleted files.
   */
  it('groups a submodule with the directories, though its path has no slash', async () => {
    const { handle } = renderSection([
      ...FILES,
      gitEntry({ path: 'zzz-vendor/lib', isDir: true }),
    ])
    openFlatList(handle)
    handle().setSortOrder({ key: 'size', direction: 'desc' })
    await waitFor(() => expect(flatListNames()).toHaveLength(4))
    // First, with the directories -- not last, with the unstat-able entries.
    expect(flatListNames()[0]).toBe('zzz-vendor/lib')
  })

  /**
   * The worker leaves size and mod_time unset for an entry it could not stat
   * (a deleted file). Those sort last rather than reading as a zero-byte file
   * at the epoch, which is where a defaulted 0 / "" would put them.
   */
  it('sorts an entry with no stat last under both directions', async () => {
    const { handle } = renderSection([...FILES, gitEntry({ path: 'src/deleted.ts' })])
    openFlatList(handle)

    handle().setSortOrder({ key: 'size', direction: 'asc' })
    await waitFor(() => expect(flatListNames()).toHaveLength(4))
    expect(flatListNames().at(-1)).toBe('src/deleted.ts')

    handle().setSortOrder({ key: 'size', direction: 'desc' })
    await waitFor(() => expect(flatListNames()[0]).toBe('src/apple.ts'))
    expect(flatListNames().at(-1)).toBe('src/deleted.ts')
  })
})

/**
 * The toolbar reads everything through the section's handle, which is undefined
 * until the section mounts. Every default therefore lives in this component,
 * once -- the mirrored props it replaced each carried their own copy in
 * buildSectionDef, a second value that could drift from what the toolbar shows.
 */
describe('filesSectionHeaderActions', () => {
  function renderActions(handle: FilesSectionHandle | undefined) {
    return render(() => (
      <FilesSectionHeaderActions
        handle={() => handle}
        onLocateFile={() => {}}
        onRefresh={() => {}}
        hasActiveFileTab={false}
      />
    ))
  }

  function stubHandle(overrides: Partial<FilesSectionHandle> = {}): FilesSectionHandle {
    return {
      collapseAll: () => {},
      refresh: () => {},
      isFiltered: () => false,
      flatListMode: () => false,
      toggleFlatListMode: () => {},
      showHiddenFiles: () => true,
      toggleShowHiddenFiles: () => {},
      sortOrder: () => DEFAULT_FILE_SORT_ORDER,
      setSortOrder: () => {},
      ...overrides,
    }
  }

  it('renders with no handle at all, before the section mounts', () => {
    renderActions(undefined)
    // Defaults: hidden files shown, no filter, so no flat-list toggle.
    expect(screen.getByTestId('files-show-hidden-toggle')).toBeInTheDocument()
    expect(screen.getByTestId('files-collapse-all')).toBeInTheDocument()
    expect(screen.getByTestId('files-refresh')).toBeInTheDocument()
    expect(screen.queryByTestId('files-flat-list-toggle')).not.toBeInTheDocument()
  })

  it('shows the flat-list toggle only while a filter is active', () => {
    const { unmount } = renderActions(stubHandle({ isFiltered: () => true }))
    expect(screen.getByTestId('files-flat-list-toggle')).toBeInTheDocument()
    unmount()

    renderActions(stubHandle({ isFiltered: () => false }))
    expect(screen.queryByTestId('files-flat-list-toggle')).not.toBeInTheDocument()
  })

  it('routes each toolbar click to the handle', () => {
    const calls: string[] = []
    renderActions(stubHandle({
      isFiltered: () => true,
      collapseAll: () => calls.push('collapseAll'),
      toggleFlatListMode: () => calls.push('toggleFlatListMode'),
      toggleShowHiddenFiles: () => calls.push('toggleShowHiddenFiles'),
    }))

    fireEvent.click(screen.getByTestId('files-collapse-all'))
    fireEvent.click(screen.getByTestId('files-flat-list-toggle'))
    fireEvent.click(screen.getByTestId('files-show-hidden-toggle'))

    expect(calls).toEqual(['collapseAll', 'toggleFlatListMode', 'toggleShowHiddenFiles'])
  })

  it('reads the sort order off the handle', () => {
    renderActions(stubHandle({ sortOrder: () => ({ key: 'size', direction: 'desc' }) }))
    expect(screen.getByTestId('files-sort-key-size').getAttribute('aria-checked')).toBe('true')
    expect(screen.getByTestId('files-sort-direction-desc').getAttribute('aria-checked')).toBe('true')
  })

  it('falls back to the default sort order with no handle', () => {
    renderActions(undefined)
    expect(screen.getByTestId('files-sort-key-name').getAttribute('aria-checked')).toBe('true')
    expect(screen.getByTestId('files-sort-direction-asc').getAttribute('aria-checked')).toBe('true')
  })
})
