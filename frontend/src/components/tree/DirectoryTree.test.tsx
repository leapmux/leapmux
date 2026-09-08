import type { DirectoryTreeHandle } from './DirectoryTree'
import type { FileSortOrder } from '~/lib/fileSort'
import { fireEvent, render, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { PREFIX_DIRECTORY_TREE, sessionStorageClearForTests, sessionStorageGet, sessionStorageSet } from '~/lib/browserStorage'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { DIRECTORY_TREE_STATE_VERSION, DirectoryTree } from './DirectoryTree'

const gitStatusStore = createRepoGitStore()

const listDirectory = vi.fn()
const statFile = vi.fn()
vi.mock('~/api/workerRpc', () => ({
  listDirectory: (...args: unknown[]) => listDirectory(...args),
  statFile: (...args: unknown[]) => statFile(...args),
}))

interface EntryOverrides {
  isDir?: boolean
  size?: bigint
  modTime?: string
  hidden?: boolean
}

function entry(root: string, name: string, overrides: EntryOverrides = {}) {
  return {
    name,
    path: `${root}/${name}`,
    isDir: overrides.isDir ?? false,
    hidden: overrides.hidden ?? false,
    size: overrides.size ?? 0n,
    modTime: overrides.modTime ?? '2026-01-01T00:00:00Z',
  }
}

/**
 * One directory's reply, in the shape ListDirectory answers with.
 *
 * `path` is left empty because nothing reads it on this path: a per-node load
 * keys the cache by the NODE's path, and the tree re-keys a chain's first
 * listing to the root it asked for. A chain test that needs real paths builds
 * its own reply.
 */
function oneListing(
  entries: ReturnType<typeof entry>[],
  opts: { truncated?: boolean, totalEntries?: number } = {},
) {
  return {
    listings: [{
      path: '',
      entries,
      truncated: opts.truncated ?? false,
      totalEntries: opts.totalEntries ?? 0,
    }],
  }
}

/**
 * Drain the microtask queue a few times.
 *
 * An effect that re-fires on its own asynchronous write needs one turn per
 * iteration, so a loop shows up within a handful of them. Not a timer: a
 * fixed delay would make the test slower AND weaker.
 */
async function settle(turns = 10): Promise<void> {
  for (let i = 0; i < turns; i++)
    await Promise.resolve()
}

function rowFor(name: string): Element | undefined {
  return [...document.querySelectorAll('[data-testid="tree-row"]')]
    .find(el => el.querySelector('[data-testid="tree-row-name"]')?.textContent === name)
}

/**
 * The rendered row names, in order. Reads the name hook rather than the row's
 * textContent: the three-dot menu renders inside the row and stays mounted
 * while closed, so the row's text also carries its menu items.
 */
function renderedNames(): string[] {
  return [...document.querySelectorAll('[data-testid="tree-row"]')]
    .map(el => el.querySelector('[data-testid="tree-row-name"]')?.textContent ?? '')
}

beforeEach(() => {
  listDirectory.mockReset()
  statFile.mockReset()
  statFile.mockResolvedValue({ info: { modTime: '2026-01-01T00:00:00Z' } })
  sessionStorageClearForTests()
})

describe('directoryTree', () => {
  /**
   * A background refresh must not re-create the rows it did not change.
   *
   * `<For>` maps by object REFERENCE, so replacing `childrenCache[path]` with a
   * fresh array of fresh objects disposed and rebuilt every sibling row — and
   * the three-dot menu is rendered INSIDE the row, so an open menu went with
   * it. One file written by an agent during a turn was enough to detach every
   * row in that directory at turn end, which is the race the e2e helpers'
   * open-then-click retry loop was written to survive.
   */
  it('keeps an unchanged row mounted when a refresh adds a sibling', async () => {
    const root = '/repo-reconcile'
    listDirectory.mockResolvedValue(oneListing([entry(root, 'a.txt'), entry(root, 'b.txt')]))

    let handle!: DirectoryTreeHandle
    render(() => (
      <DirectoryTree
        workerId="w1"
        gitStatusStore={gitStatusStore}
        showFiles
        rootPath={root}
        selectedPath=""
        onSelect={() => {}}
        ref={(h) => { handle = h }}
      />
    ))
    await waitFor(() => expect(rowFor('a.txt')).toBeTruthy())
    const before = rowFor('a.txt')!

    listDirectory.mockResolvedValue(oneListing([entry(root, 'a.txt'), entry(root, 'b.txt'), entry(root, 'c.txt')]))
    handle.refresh()
    await waitFor(() => expect(rowFor('c.txt')).toBeTruthy())

    expect(rowFor('a.txt')).toBe(before)
    expect(before.isConnected).toBe(true)
  })

  it('marks the selected row with data-active, the one marker the coarse-pointer kebab reveal keys on', async () => {
    const root = '/repo-active'
    listDirectory.mockResolvedValue(oneListing([entry(root, 'a.txt'), entry(root, 'b.txt')]))

    render(() => (
      <DirectoryTree
        workerId="w1"
        gitStatusStore={gitStatusStore}
        showFiles
        rootPath={root}
        selectedPath={`${root}/a.txt`}
        onSelect={() => {}}
      />
    ))
    await waitFor(() => expect(rowFor('a.txt')).toBeTruthy())

    expect(rowFor('a.txt')).toHaveAttribute('data-active', 'true')
    expect(rowFor('b.txt')).toHaveAttribute('data-active', 'false')
  })
})

describe('directoryTree sorting', () => {
  const root = '/repo-sort'

  // Chosen so that name, size, modified and type each produce a DIFFERENT
  // order — otherwise a test could pass with the sort key ignored entirely.
  //   name asc:      apple.ts, banana.md, cherry.js
  //   size asc:      banana.md (10), cherry.js (100), apple.ts (900)
  //   modified desc: banana.md (2026), cherry.js (2023), apple.ts (2020)
  //   type asc:      cherry.js, banana.md, apple.ts
  const entries = [
    entry(root, 'zeta', { isDir: true, modTime: '2020-01-01T00:00:00Z' }),
    entry(root, 'alpha', { isDir: true, modTime: '2026-06-01T00:00:00Z' }),
    entry(root, 'apple.ts', { size: 900n, modTime: '2020-01-01T00:00:00Z' }),
    entry(root, 'banana.md', { size: 10n, modTime: '2026-06-01T00:00:00Z' }),
    entry(root, 'cherry.js', { size: 100n, modTime: '2023-01-01T00:00:00Z' }),
  ]

  /**
   * Renders the tree with a MUTABLE sort order, and returns the setter so a
   * test can change it without re-mounting — the point of sorting at render
   * time is that a change costs no refetch.
   */
  function renderSorted(initial: FileSortOrder) {
    const [sortOrder, setSortOrder] = createSignal(initial)
    const result = render(() => (
      <DirectoryTree
        workerId="w1"
        gitStatusStore={gitStatusStore}
        showFiles
        rootPath={root}
        selectedPath=""
        onSelect={() => {}}
        sortOrder={sortOrder()}
      />
    ))
    return { ...result, setSortOrder }
  }

  beforeEach(() => {
    listDirectory.mockResolvedValue(oneListing(entries))
  })

  it('defaults to directories first, then name ascending', async () => {
    renderSorted({ key: 'name', direction: 'asc' })
    await waitFor(() => expect(renderedNames()).toHaveLength(5))
    expect(renderedNames()).toEqual(['alpha', 'zeta', 'apple.ts', 'banana.md', 'cherry.js'])
  })

  it('sorts files by size while directories keep path order', async () => {
    renderSorted({ key: 'size', direction: 'asc' })
    await waitFor(() => expect(renderedNames()).toHaveLength(5))
    expect(renderedNames()).toEqual(['alpha', 'zeta', 'banana.md', 'cherry.js', 'apple.ts'])
  })

  it('reverses only the files when a size sort is descending', async () => {
    renderSorted({ key: 'size', direction: 'desc' })
    await waitFor(() => expect(renderedNames()).toHaveLength(5))
    expect(renderedNames()).toEqual(['alpha', 'zeta', 'apple.ts', 'cherry.js', 'banana.md'])
  })

  it('sorts files by modification time, newest first', async () => {
    renderSorted({ key: 'modified', direction: 'desc' })
    await waitFor(() => expect(renderedNames()).toHaveLength(5))
    expect(renderedNames()).toEqual(['alpha', 'zeta', 'banana.md', 'cherry.js', 'apple.ts'])
  })

  it('groups files by extension under a type sort', async () => {
    renderSorted({ key: 'type', direction: 'asc' })
    await waitFor(() => expect(renderedNames()).toHaveLength(5))
    expect(renderedNames()).toEqual(['alpha', 'zeta', 'cherry.js', 'banana.md', 'apple.ts'])
  })

  /**
   * The whole point of sorting at render time: the cache already holds the
   * listing, so changing the order must not cost another round trip.
   */
  it('re-orders without re-fetching when the sort order changes', async () => {
    const { setSortOrder } = renderSorted({ key: 'name', direction: 'asc' })
    await waitFor(() => expect(renderedNames()).toHaveLength(5))
    const callsAfterLoad = listDirectory.mock.calls.length
    const alphaRow = rowFor('alpha')!

    setSortOrder({ key: 'name', direction: 'desc' })

    await waitFor(() => expect(renderedNames()[0]).toBe('zeta'))
    expect(renderedNames()).toEqual(['zeta', 'alpha', 'cherry.js', 'banana.md', 'apple.ts'])
    expect(listDirectory.mock.calls.length).toBe(callsAfterLoad)
    // Reordering moves the existing rows; it must not dispose and rebuild them,
    // which would tear an open three-dot menu out of the DOM.
    expect(rowFor('alpha')).toBe(alphaRow)
    expect(alphaRow.isConnected).toBe(true)
  })

  /**
   * An UNVERSIONED payload is whatever an older build wrote, and its shape is
   * unknowable from here. Restoring it would have shown every file as 0 bytes
   * with no modification time, sorted as one tie, until the user refreshed.
   * The whole payload is discarded and re-fetched instead.
   */
  it('discards an unversioned payload entirely', async () => {
    sessionStorageSet(`${PREFIX_DIRECTORY_TREE}w1:${root}:files`, JSON.stringify({
      expandedPaths: { [root]: true },
      childrenCache: {
        [root]: [{ path: `${root}/stale.txt`, displayName: 'stale.txt', isDir: false, hidden: false }],
      },
      truncatedDirs: {},
    }))

    renderSorted({ key: 'size', direction: 'asc' })

    await waitFor(() => expect(renderedNames()).toHaveLength(5))
    expect(renderedNames()).not.toContain('stale.txt')
    expect(listDirectory).toHaveBeenCalled()
  })

  /**
   * The version answers "is this shape current", so a payload from a FUTURE
   * build is discarded too -- forward and backward are the same question.
   */
  it('discards a payload stamped with a different version', async () => {
    sessionStorageSet(`${PREFIX_DIRECTORY_TREE}w1:${root}:files`, JSON.stringify({
      v: DIRECTORY_TREE_STATE_VERSION + 1,
      expandedPaths: { [root]: true },
      childrenCache: {
        [root]: [{ path: `${root}/future.txt`, displayName: 'future.txt', isDir: false, hidden: false, size: 1, modTime: '2026-01-01T00:00:00Z' }],
      },
      truncatedDirs: {},
    }))

    renderSorted({ key: 'name', direction: 'asc' })

    await waitFor(() => expect(listDirectory).toHaveBeenCalled())
    expect(renderedNames()).not.toContain('future.txt')
  })

  it('restores a payload at the current version without re-fetching', async () => {
    sessionStorageSet(`${PREFIX_DIRECTORY_TREE}w1:${root}:files`, JSON.stringify({
      v: DIRECTORY_TREE_STATE_VERSION,
      expandedPaths: { [root]: true },
      childrenCache: {
        [root]: [{ path: `${root}/kept.txt`, displayName: 'kept.txt', isDir: false, hidden: false, size: 5, modTime: '2026-01-01T00:00:00Z' }],
      },
      truncatedDirs: {},
    }))

    renderSorted({ key: 'name', direction: 'asc' })

    await waitFor(() => expect(renderedNames()).toEqual(['kept.txt']))
    expect(listDirectory).not.toHaveBeenCalled()
  })

  /**
   * The version cannot speak for a payload that is the right shape but corrupt
   * -- a hand edit, or a truncated write. That directory alone is dropped.
   */
  it('drops a malformed directory inside an otherwise current payload', async () => {
    sessionStorageSet(`${PREFIX_DIRECTORY_TREE}w1:${root}:files`, JSON.stringify({
      v: DIRECTORY_TREE_STATE_VERSION,
      expandedPaths: { [root]: true },
      childrenCache: {
        [root]: [{ path: `${root}/kept.txt`, displayName: 'kept.txt', isDir: false, hidden: false, size: 5, modTime: '2026-01-01T00:00:00Z' }],
        [`${root}/bad`]: 'not an array',
      },
      truncatedDirs: {},
    }))

    renderSorted({ key: 'name', direction: 'asc' })

    await waitFor(() => expect(renderedNames()).toEqual(['kept.txt']))
    expect(listDirectory).not.toHaveBeenCalled()
  })

  /**
   * The worker reports what the directory really held, so the notice can name
   * the size of what is hidden rather than only that something is.
   */
  it('names how many entries the directory really held', async () => {
    listDirectory.mockResolvedValue(oneListing(entries, { truncated: true, totalEntries: 12043 }))
    const { container } = renderSorted({ key: 'name', direction: 'asc' })
    await waitFor(() => expect(container.textContent).toContain('listing truncated'))
    expect(container.textContent).toContain('5 of 12043 entries')
  })

  /**
   * A worker that predates `total_entries` sends none. The notice must fall
   * back to the open-ended form rather than claim a total of zero -- and it
   * must still APPEAR, which a truthiness test on the count would have broken.
   */
  it('falls back to the open-ended count when the worker sends no total', async () => {
    listDirectory.mockResolvedValue(oneListing(entries, { truncated: true }))
    const { container } = renderSorted({ key: 'name', direction: 'asc' })
    await waitFor(() => expect(container.textContent).toContain('listing truncated'))
    expect(container.textContent).toContain('5+ entries')
    expect(container.textContent).not.toContain('of 0 entries')
  })

  it('says the listing was truncated by name when sorting by something else', async () => {
    listDirectory.mockResolvedValue(oneListing(entries, { truncated: true }))
    const { container, setSortOrder } = renderSorted({ key: 'name', direction: 'asc' })
    await waitFor(() => expect(container.textContent).toContain('listing truncated'))

    setSortOrder({ key: 'size', direction: 'desc' })

    await waitFor(() => expect(container.textContent).toContain('truncated by name before sorting'))
    expect(container.textContent).not.toContain('listing truncated')
  })
})

/**
 * The root row has no parent listing in this tree, so it stats itself. Without
 * that it would be the one directory whose three-dot menu showed nothing.
 */
describe('directoryTree root row info', () => {
  const root = '/repo-root-info'

  /**
   * The info block is built only once the popover opens -- `DropdownMenu`
   * renders its children eagerly, so an ungated builder kept a live
   * `RelativeTime` mounted for every closed menu in the tree.
   */
  function openRootMenu(rootRow: Element): void {
    fireEvent.click(rootRow.querySelector('[data-testid="tree-context-button"]')!)
  }

  /** Waits for the root row, then opens its three-dot menu. */
  async function rootRowWithMenuOpen(): Promise<Element> {
    const row = await waitFor(() => {
      const el = document.querySelector('[data-testid="tree-root-node"]')
      expect(el?.querySelector('[data-testid="tree-context-button"]')).toBeTruthy()
      return el!
    })
    openRootMenu(row)
    return row
  }

  it('stats the root once and shows its modified time', async () => {
    listDirectory.mockResolvedValue(oneListing([entry(root, 'a.txt')]))
    statFile.mockResolvedValue({ info: { modTime: '2026-04-01T09:00:00Z' } })

    render(() => (
      <DirectoryTree workerId="w1" gitStatusStore={gitStatusStore} showFiles rootPath={root} selectedPath="" onSelect={() => {}} />
    ))

    await waitFor(() => expect(statFile).toHaveBeenCalledWith('w1', { workerId: 'w1', path: root }))
    // The root row only renders once the listing settles — statFile can resolve
    // while the tree is still in its loading branch.
    const rootRow = await rootRowWithMenuOpen()

    const info = rootRow.querySelector('[data-testid="tree-info-button"]')!
    expect(info.textContent).toContain('Modified:')
    // A directory's own byte count says nothing about what it holds.
    expect(info.textContent).not.toContain('Size:')
    expect(statFile).toHaveBeenCalledTimes(1)
  })

  it('waits for enabled before stat-ing, then stats once it flips', async () => {
    listDirectory.mockResolvedValue(oneListing([entry(root, 'a.txt')]))
    statFile.mockResolvedValue({ info: { modTime: '2026-04-01T09:00:00Z' } })
    const [enabled, setEnabled] = createSignal(false)

    render(() => (
      <DirectoryTree workerId="w1" gitStatusStore={gitStatusStore} showFiles rootPath={root} selectedPath="" onSelect={() => {}} enabled={enabled()} />
    ))
    expect(statFile).not.toHaveBeenCalled()

    // A worktree-creating agent flips this true once its directory exists.
    setEnabled(true)
    await waitFor(() => expect(statFile).toHaveBeenCalledWith('w1', { workerId: 'w1', path: root }))
  })

  /**
   * A refresh re-stats the same directory. Blanking the value first would make
   * the menu's Modified row blink out at every turn end, so the reset is scoped
   * to a change of worker or root.
   */
  it('keeps the root modified time on screen across a refresh', async () => {
    listDirectory.mockResolvedValue(oneListing([entry(root, 'a.txt')]))
    statFile.mockResolvedValueOnce({ info: { modTime: '2026-04-01T09:00:00Z' } })

    let handle!: DirectoryTreeHandle
    render(() => (
      <DirectoryTree
        workerId="w1"
        gitStatusStore={gitStatusStore}
        showFiles
        rootPath={root}
        selectedPath=""
        onSelect={() => {}}
        ref={(h) => { handle = h }}
      />
    ))
    const rootRow = await rootRowWithMenuOpen()
    expect(rootRow.querySelector('[data-testid="tree-info-button"]')).toBeTruthy()

    // The re-stat never settles, so anything still on screen is the old value.
    statFile.mockReturnValue(new Promise(() => {}))
    handle.refresh()

    await waitFor(() => expect(statFile).toHaveBeenCalledTimes(2))
    expect(rootRow.querySelector('[data-testid="tree-info-button"]')).toBeTruthy()
  })

  it('omits the block when the root cannot be stat-ed', async () => {
    listDirectory.mockResolvedValue(oneListing([entry(root, 'a.txt')]))
    statFile.mockRejectedValue(new Error('permission denied'))

    render(() => (
      <DirectoryTree workerId="w1" gitStatusStore={gitStatusStore} showFiles rootPath={root} selectedPath="" onSelect={() => {}} />
    ))

    await waitFor(() => expect(rowFor('a.txt')).toBeTruthy())
    const rootRow = await rootRowWithMenuOpen()
    expect(rootRow.querySelector('[data-testid="tree-info-button"]')).toBeNull()
  })
})
/**
 * The hidden-files toggle and the git filter both run through the SAME
 * `visibleSortedChildren` helper the sort does, and this change merged them
 * there from two separately written closures. Every other test in this file
 * leaves both filters at their defaults, so the whole predicate --
 * `(showHidden || !c.hidden) && (!isVisible || isVisible(c.path))` -- and the
 * `showHidden && !isVisible` fast path in front of it were unreachable from
 * the unit suite. Inverting either subexpression kept the suite green.
 */
describe('directoryTree filtering', () => {
  const root = '/repo-filter'
  const entries = [
    entry(root, 'visible.ts'),
    entry(root, '.hidden.ts', { hidden: true }),
    entry(root, 'other.ts'),
  ]

  beforeEach(() => {
    listDirectory.mockResolvedValue(oneListing(entries))
  })

  function renderFiltered(props: { showHiddenFiles?: boolean, isVisible?: (path: string) => boolean }) {
    return render(() => (
      <DirectoryTree
        workerId="w1"
        gitStatusStore={gitStatusStore}
        showFiles
        rootPath={root}
        selectedPath=""
        onSelect={() => {}}
        showHiddenFiles={props.showHiddenFiles}
        isVisible={props.isVisible}
      />
    ))
  }

  it('shows hidden entries by default', async () => {
    renderFiltered({})
    await waitFor(() => expect(rowFor('visible.ts')).toBeTruthy())
    expect(renderedNames()).toContain('.hidden.ts')
  })

  it('drops hidden entries when showHiddenFiles is false', async () => {
    renderFiltered({ showHiddenFiles: false })
    await waitFor(() => expect(rowFor('visible.ts')).toBeTruthy())
    const names = renderedNames()
    expect(names).not.toContain('.hidden.ts')
    expect(names).toContain('other.ts')
  })

  it('renders only the paths isVisible accepts', async () => {
    renderFiltered({ isVisible: path => path.endsWith('other.ts') })
    await waitFor(() => expect(rowFor('other.ts')).toBeTruthy())
    const names = renderedNames()
    expect(names).not.toContain('visible.ts')
    expect(names).not.toContain('.hidden.ts')
  })

  /**
   * Both filters at once. The fast path only skips the walk when hidden files
   * are shown AND no predicate is set, so this combination must still apply
   * both terms.
   */
  it('applies the hidden filter and isVisible together', async () => {
    renderFiltered({
      showHiddenFiles: false,
      isVisible: path => path.endsWith('.hidden.ts') || path.endsWith('other.ts'),
    })
    await waitFor(() => expect(rowFor('other.ts')).toBeTruthy())
    const names = renderedNames()
    // Accepted by isVisible, but still hidden.
    expect(names).not.toContain('.hidden.ts')
    // Rejected by isVisible, though not hidden.
    expect(names).not.toContain('visible.ts')
    expect(names).toContain('other.ts')
  })

  it('renders nothing when isVisible rejects every entry', async () => {
    renderFiltered({ isVisible: () => false })
    await waitFor(() => expect(listDirectory).toHaveBeenCalled())
    expect(renderedNames()).not.toContain('visible.ts')
  })
})

/**
 * A worker that answers a chain request, honouring `fromRoot`.
 *
 * `dirs` maps a directory path to its entries. A request with `fromRoot`
 * answers with every directory on the root-to-target chain that `dirs` knows
 * about, outermost first, exactly as the worker does.
 */
function mockChain(dirs: Record<string, ReturnType<typeof entry>[]>, separator = '/') {
  listDirectory.mockImplementation(async (_workerId: string, req: { path: string, fromRoot?: string }) => {
    const chain: string[] = []
    if (req.fromRoot) {
      let cur = req.fromRoot
      chain.push(cur)
      const rest = req.path.slice(req.fromRoot.length).split(/[\\/]+/).filter(Boolean)
      for (const part of rest) {
        cur = cur.endsWith(separator) ? `${cur}${part}` : `${cur}${separator}${part}`
        chain.push(cur)
      }
    }
    else {
      chain.push(req.path)
    }
    return {
      listings: chain
        .filter(path => dirs[path] !== undefined)
        .map(path => ({ path, entries: dirs[path], truncated: false, totalEntries: dirs[path].length })),
    }
  })
}

function dirEntry(parent: string, name: string, separator = '/') {
  return {
    name,
    path: parent.endsWith(separator) ? `${parent}${name}` : `${parent}${separator}${name}`,
    isDir: true,
    hidden: false,
    size: 0n,
    modTime: '2026-01-01T00:00:00Z',
  }
}

function renderTree(props: Partial<Parameters<typeof DirectoryTree>[0]> & { rootPath: string }) {
  return render(() => (
    <DirectoryTree
      workerId="w1"
      gitStatusStore={gitStatusStore}
      selectedPath=""
      onSelect={() => {}}
      {...props}
    />
  ))
}

describe('directoryTree reveal', () => {
  const root = '/'

  it('expands toward revealPath when nothing is selected', async () => {
    mockChain({
      '/': [dirEntry('/', 'home'), dirEntry('/', 'opt')],
      '/home': [dirEntry('/home', 'alice')],
      '/home/alice': [dirEntry('/home/alice', 'proj')],
    })

    renderTree({ rootPath: root, revealPath: '/home/alice' })

    await waitFor(() => expect(rowFor('proj')).toBeTruthy())
    expect(rowFor('home')).toBeTruthy()
    expect(rowFor('alice')).toBeTruthy()
  })

  // The whole point of a separate prop: a revealed node is OPEN, never
  // selected. Seeding the selection instead would arm the New workspace
  // dialog's Create button with a directory the user never chose.
  it('does not mark the revealed node selected', async () => {
    mockChain({
      '/': [dirEntry('/', 'home')],
      '/home': [dirEntry('/home', 'alice')],
      '/home/alice': [],
    })

    renderTree({ rootPath: root, revealPath: '/home/alice' })

    await waitFor(() => expect(rowFor('alice')).toBeTruthy())
    expect(document.querySelectorAll('[data-active="true"]')).toHaveLength(0)
    expect(document.querySelector('[data-testid="tree-root-node"]')?.getAttribute('data-active')).toBe('false')
  })

  it('ignores revealPath once a path is selected', async () => {
    mockChain({
      '/': [dirEntry('/', 'home')],
      '/home': [dirEntry('/home', 'alice')],
      '/home/alice': [dirEntry('/home/alice', 'proj')],
    })

    renderTree({ rootPath: root, selectedPath: '/home', revealPath: '/home/alice' })

    await waitFor(() => expect(rowFor('home')).toBeTruthy())
    expect(rowFor('home')?.getAttribute('data-active')).toBe('true')
    // `/home` is expanded because it holds the selection, but nothing walked
    // on to `/home/alice`'s own children.
    await waitFor(() => expect(rowFor('alice')).toBeTruthy())
    expect(rowFor('proj')).toBeUndefined()
  })

  it('ignores an empty revealPath', async () => {
    mockChain({ '/': [dirEntry('/', 'home')] })

    renderTree({ rootPath: root, revealPath: '' })

    await waitFor(() => expect(rowFor('home')).toBeTruthy())
    expect(listDirectory).toHaveBeenCalledTimes(1)
    expect(listDirectory.mock.calls[0][1]).toMatchObject({ path: '/' })
    expect(listDirectory.mock.calls[0][1].fromRoot).toBeUndefined()
  })
})

describe('directoryTree chain loading', () => {
  it('fetches the whole root-to-reveal chain in one request', async () => {
    mockChain({
      '/': [dirEntry('/', 'home')],
      '/home': [dirEntry('/home', 'alice')],
      '/home/alice': [dirEntry('/home/alice', 'proj')],
      '/home/alice/proj': [],
    })

    renderTree({ rootPath: '/', revealPath: '/home/alice/proj' })

    await waitFor(() => expect(rowFor('proj')).toBeTruthy())
    expect(listDirectory).toHaveBeenCalledTimes(1)
    expect(listDirectory.mock.calls[0][1]).toMatchObject({ path: '/home/alice/proj', fromRoot: '/' })
  })

  it('does not re-request when every chain directory is cached', async () => {
    mockChain({
      '/': [dirEntry('/', 'home')],
      '/home': [dirEntry('/home', 'alice')],
      '/home/alice': [],
    })

    const { unmount } = renderTree({ rootPath: '/', revealPath: '/home/alice' })
    await waitFor(() => expect(rowFor('alice')).toBeTruthy())
    const afterFirst = listDirectory.mock.calls.length
    unmount()

    // A second mount restores from sessionStorage, so the chain is already
    // known and nothing is fetched again.
    renderTree({ rootPath: '/', revealPath: '/home/alice' })
    await waitFor(() => expect(rowFor('alice')).toBeTruthy())
    expect(listDirectory.mock.calls.length).toBe(afterFirst)
  })

  it('re-reveals in one round trip when the target moves to another branch', async () => {
    mockChain({
      '/': [dirEntry('/', 'home'), dirEntry('/', 'opt')],
      '/home': [dirEntry('/home', 'alice')],
      '/home/alice': [],
      '/opt': [dirEntry('/opt', 'tools')],
      '/opt/tools': [],
    })

    const [selected, setSelected] = createSignal('/home/alice')
    render(() => (
      <DirectoryTree
        workerId="w1"
        gitStatusStore={gitStatusStore}
        rootPath="/"
        selectedPath={selected()}
        onSelect={() => {}}
      />
    ))
    await waitFor(() => expect(rowFor('alice')).toBeTruthy())
    const afterFirst = listDirectory.mock.calls.length

    setSelected('/opt/tools')
    await waitFor(() => expect(rowFor('tools')).toBeTruthy())
    expect(listDirectory.mock.calls.length).toBe(afterFirst + 1)
    expect(listDirectory.mock.calls.at(-1)?.[1]).toMatchObject({ path: '/opt/tools', fromRoot: '/' })
  })

  // A chain failure with the root on screen is not a tree failure: the
  // per-node cascade still walks the user there one level at a time.
  it('keeps the tree on screen when a chain request fails', async () => {
    const dirs: Record<string, ReturnType<typeof entry>[]> = {
      '/': [dirEntry('/', 'home')],
      '/home': [dirEntry('/home', 'alice')],
      '/home/alice': [],
    }
    listDirectory.mockImplementation(async (_w: string, req: { path: string, fromRoot?: string }) => {
      if (req.fromRoot)
        throw new Error('chain boom')
      const entries = dirs[req.path] ?? []
      return { listings: [{ path: req.path, entries, truncated: false, totalEntries: entries.length }] }
    })

    const [selected, setSelected] = createSignal('')
    render(() => (
      <DirectoryTree
        workerId="w1"
        gitStatusStore={gitStatusStore}
        rootPath="/"
        selectedPath={selected()}
        onSelect={() => {}}
      />
    ))
    await waitFor(() => expect(rowFor('home')).toBeTruthy())

    setSelected('/home/alice')
    await waitFor(() => expect(rowFor('alice')).toBeTruthy())
    expect(rowFor('home')).toBeTruthy()
    expect(document.body.textContent).not.toContain('Failed to load directory')
  })

  it('does not blank the tree to Loading when the selection changes', async () => {
    mockChain({
      '/': [dirEntry('/', 'home')],
      '/home': [dirEntry('/home', 'alice')],
      '/home/alice': [],
    })

    const [selected, setSelected] = createSignal('')
    render(() => (
      <DirectoryTree
        workerId="w1"
        gitStatusStore={gitStatusStore}
        rootPath="/"
        selectedPath={selected()}
        onSelect={() => {}}
      />
    ))
    await waitFor(() => expect(rowFor('home')).toBeTruthy())

    setSelected('/home/alice')
    // The tree-level loading state REPLACES every row, so its absence is what
    // "the tree did not blank" means. A node's own inline spinner is a
    // different element and is correct while its children arrive.
    expect(document.querySelector('[data-testid="tree-root-node"]')).toBeTruthy()
    expect(rowFor('home')).toBeTruthy()
    await waitFor(() => expect(rowFor('alice')).toBeTruthy())
  })

  /**
   * The worker canonicalizes what it lists, so a symlinked root answers under
   * a different path. The root row's cache is keyed by `rootPath`, so the
   * first listing is re-keyed to what was asked for -- and the effect must not
   * then re-fetch forever because its own guard never matches.
   */
  it('re-keys the first listing when the worker canonicalizes the root', async () => {
    listDirectory.mockResolvedValue({
      listings: [{
        path: '/private/tmp/ws',
        entries: [dirEntry('/private/tmp/ws', 'src')],
        truncated: false,
        totalEntries: 1,
      }],
    })

    renderTree({ rootPath: '/tmp/ws' })

    await waitFor(() => expect(rowFor('src')).toBeTruthy())
    // Settles instead of looping: the re-keyed listing satisfies the cache
    // guard, so the effect's next run finds the chain already loaded.
    await settle()
    expect(listDirectory).toHaveBeenCalledTimes(1)
  })

  /**
   * The same canonicalization, one level DEEPER -- where re-keying cannot
   * help.
   *
   * Only the FIRST listing is re-keyed, because the entries of a deeper one
   * carry the worker's own spelling and the tree's nodes are built from those.
   * So `/tmp/ws/src` is a directory this chain named and the cache never
   * receives: its guard misses forever. This effect subscribes to the cache it
   * writes, so without `lastChainKey` its own write would re-run it, and the
   * tree would issue the same request for as long as it stayed mounted.
   */
  it('does not re-fetch in a loop when the worker answers under paths the chain never named', async () => {
    listDirectory.mockResolvedValue({
      listings: [
        { path: '/private/tmp/ws', entries: [dirEntry('/private/tmp/ws', 'src')], truncated: false, totalEntries: 1 },
        { path: '/private/tmp/ws/src', entries: [], truncated: false, totalEntries: 0 },
      ],
    })

    renderTree({ rootPath: '/tmp/ws', revealPath: '/tmp/ws/src' })

    await waitFor(() => expect(rowFor('src')).toBeTruthy())
    await settle()
    expect(listDirectory).toHaveBeenCalledTimes(1)
    expect(listDirectory.mock.calls[0][1]).toMatchObject({ path: '/tmp/ws/src', fromRoot: '/tmp/ws' })
  })

  /**
   * The chain key describes the request whose result the cache holds, so it
   * must not outlive that cache.
   *
   * `showFiles` is part of the sessionStorage key and NOT part of the chain
   * key, so toggling it replaces the whole cache while `(worker, root,
   * target)` stays the same. A guard that remembered the old request would
   * suppress the one fetch that refills the emptied tree, and the picker would
   * sit blank for the rest of the session.
   */
  it('re-fetches after a cache reset the chain key cannot see', async () => {
    mockChain({ '/': [dirEntry('/', 'home')] })

    const [showFiles, setShowFiles] = createSignal(false)
    render(() => (
      <DirectoryTree
        workerId="w1"
        gitStatusStore={gitStatusStore}
        rootPath="/"
        selectedPath=""
        showFiles={showFiles()}
        onSelect={() => {}}
      />
    ))
    await waitFor(() => expect(rowFor('home')).toBeTruthy())

    setShowFiles(true)

    await waitFor(() => expect(listDirectory).toHaveBeenCalledTimes(2))
    expect(listDirectory.mock.calls[1][1]).toMatchObject({ path: '/', dirsOnly: false })
    await waitFor(() => expect(rowFor('home')).toBeTruthy())
  })

  it('drops a file target from the chain', async () => {
    mockChain({
      '/': [dirEntry('/', 'home')],
      '/home': [
        dirEntry('/home', 'alice'),
        { name: 'notes.txt', path: '/home/notes.txt', isDir: false, hidden: false, size: 4n, modTime: '2026-01-01T00:00:00Z' },
      ],
    })

    const [selected, setSelected] = createSignal('')
    render(() => (
      <DirectoryTree
        workerId="w1"
        gitStatusStore={gitStatusStore}
        showFiles
        rootPath="/"
        selectedPath={selected()}
        onSelect={() => {}}
      />
    ))
    await waitFor(() => expect(rowFor('home')).toBeTruthy())

    setSelected('/home/notes.txt')
    await waitFor(() => expect(rowFor('notes.txt')).toBeTruthy())
    const afterFirst = listDirectory.mock.calls.length

    // Selecting the same file again asks for nothing: the tree knows it is a
    // file, so the chain ends at its parent, which is already cached.
    setSelected('')
    setSelected('/home/notes.txt')
    await waitFor(() => expect(rowFor('notes.txt')).toBeTruthy())
    expect(listDirectory.mock.calls.length).toBe(afterFirst)
  })
})

describe('directoryTree request de-duplication', () => {
  /**
   * The chain loader and the per-node cascade both react to the same reveal
   * target, so on a selection change they ask for the same directories in the
   * same tick. Without one in-flight registry between them, revealing a path
   * costs one request per level again -- the exact cost the chain removes.
   */
  it('issues one request per directory when the chain and a node ask together', async () => {
    const asked: string[] = []
    listDirectory.mockImplementation(async (_w: string, req: { path: string, fromRoot?: string }) => {
      asked.push(req.path)
      const dirs: Record<string, ReturnType<typeof entry>[]> = {
        '/': [dirEntry('/', 'home')],
        '/home': [dirEntry('/home', 'alice')],
        '/home/alice': [],
      }
      const chain = req.fromRoot ? ['/', '/home', '/home/alice'] : [req.path]
      return {
        listings: chain
          .filter(path => dirs[path] !== undefined)
          .map(path => ({ path, entries: dirs[path], truncated: false, totalEntries: dirs[path].length })),
      }
    })

    const [selected, setSelected] = createSignal('')
    render(() => (
      <DirectoryTree
        workerId="w1"
        gitStatusStore={gitStatusStore}
        rootPath="/"
        selectedPath={selected()}
        onSelect={() => {}}
      />
    ))
    await waitFor(() => expect(rowFor('home')).toBeTruthy())

    // `/home` is on screen and unexpanded, so its node reacts to the same
    // change the chain loader does.
    setSelected('/home/alice')
    await waitFor(() => expect(rowFor('alice')).toBeTruthy())

    expect(asked.filter(p => p === '/home')).toHaveLength(0)
    expect(asked.filter(p => p === '/home/alice')).toHaveLength(1)
  })

  /**
   * The worker may answer with FEWER listings than the chain implies: both the
   * listing cap and the payload budget drop levels from the deep end. The
   * per-node cascade must then fetch what is missing, so a short chain costs
   * the user nothing but a second round trip.
   */
  it('lets the cascade fetch a tail the worker truncated away', async () => {
    const dirs: Record<string, ReturnType<typeof entry>[]> = {
      '/': [dirEntry('/', 'home')],
      '/home': [dirEntry('/home', 'alice')],
      '/home/alice': [dirEntry('/home/alice', 'proj')],
    }
    listDirectory.mockImplementation(async (_w: string, req: { path: string, fromRoot?: string }) => {
      // The chain answer stops two levels short of the target.
      const chain = req.fromRoot ? ['/', '/home'] : [req.path]
      return {
        listings: chain
          .filter(path => dirs[path] !== undefined)
          .map(path => ({ path, entries: dirs[path], truncated: false, totalEntries: dirs[path].length })),
      }
    })

    render(() => (
      <DirectoryTree
        workerId="w1"
        gitStatusStore={gitStatusStore}
        rootPath="/"
        selectedPath=""
        revealPath="/home/alice/proj"
        onSelect={() => {}}
      />
    ))

    // `/home/alice` was not in the truncated chain, so its node fetched it.
    await waitFor(() => expect(rowFor('proj')).toBeTruthy())
  })
})

describe('directoryTree at a filesystem root', () => {
  it('labels a posix root row with the root itself', async () => {
    mockChain({ '/': [dirEntry('/', 'home')] })

    renderTree({ rootPath: '/', flavor: 'posix' })

    await waitFor(() => expect(rowFor('home')).toBeTruthy())
    const rootRow = document.querySelector('[data-testid="tree-root-node"]')
    expect(rootRow?.querySelector('[data-testid="tree-row-name"]')?.textContent).toBe('/')
  })

  // `basename('C:\\', 'win32')` is 'C:' -- the drive WITHOUT the separator
  // that makes it a root, and not what the drive selector beside it reads.
  it('labels a windows drive root with the trailing separator', async () => {
    mockChain({ 'C:\\': [dirEntry('C:\\', 'Users', '\\')] }, '\\')

    renderTree({ rootPath: 'C:\\', flavor: 'win32' })

    await waitFor(() => expect(rowFor('Users')).toBeTruthy())
    const rootRow = document.querySelector('[data-testid="tree-root-node"]')
    expect(rootRow?.querySelector('[data-testid="tree-row-name"]')?.textContent).toBe('C:\\')
  })

  it('expands toward a windows revealPath', async () => {
    mockChain({
      'C:\\': [dirEntry('C:\\', 'Users', '\\')],
      'C:\\Users': [dirEntry('C:\\Users', 'alice', '\\')],
      'C:\\Users\\alice': [dirEntry('C:\\Users\\alice', 'proj', '\\')],
    }, '\\')

    renderTree({ rootPath: 'C:\\', flavor: 'win32', revealPath: 'C:\\Users\\alice' })

    await waitFor(() => expect(rowFor('proj')).toBeTruthy())
    expect(listDirectory).toHaveBeenCalledTimes(1)
    expect(listDirectory.mock.calls[0][1]).toMatchObject({ path: 'C:\\Users\\alice', fromRoot: 'C:\\' })
  })

  it('keys sessionStorage on the worker as well as the root path', async () => {
    mockChain({ 'C:\\': [dirEntry('C:\\', 'Users', '\\')] }, '\\')

    renderTree({ rootPath: 'C:\\', flavor: 'win32' })

    await waitFor(() => expect(rowFor('Users')).toBeTruthy())
    expect(sessionStorageGet(`${PREFIX_DIRECTORY_TREE}w1:C:\\:dirs`)).toBeTruthy()
  })

  /**
   * Two workers routinely share a root path -- every POSIX worker roots the
   * picker at `/`. Without the worker in the key, the second one restores the
   * first one's listing and then skips its own fetch.
   */
  it('does not serve one worker cached listing to another', async () => {
    mockChain({ '/': [dirEntry('/', 'from-w1')] })
    const { unmount } = renderTree({ rootPath: '/' })
    await waitFor(() => expect(rowFor('from-w1')).toBeTruthy())
    unmount()

    mockChain({ '/': [dirEntry('/', 'from-w2')] })
    render(() => (
      <DirectoryTree
        workerId="w2"
        gitStatusStore={gitStatusStore}
        rootPath="/"
        selectedPath=""
        onSelect={() => {}}
      />
    ))
    await waitFor(() => expect(rowFor('from-w2')).toBeTruthy())
    expect(rowFor('from-w1')).toBeUndefined()
  })
})
