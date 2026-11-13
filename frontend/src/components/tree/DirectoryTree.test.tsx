import type { DirectoryTreeHandle } from './DirectoryTree'
import type { FileSortOrder } from '~/lib/fileSort'
import { fireEvent, render, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { PREFIX_DIRECTORY_TREE, sessionStorageClearForTests, sessionStorageGet, sessionStorageSet, setStorageAccountForTests } from '~/lib/browserStorage'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { DirectoryTree } from './DirectoryTree'
import { DIRECTORY_TREE_STATE_VERSION } from './directoryTreeState'
import { CHILDREN_TRANSITION_MS, childrenWrapperExpanded } from './sharedTree.css'

const gitStatusStore = createRepoGitStore()

const listDirectory = vi.fn()
const statFile = vi.fn()
vi.mock('~/api/workerRpc', () => ({
  workerFile: {
    listDirectory: (...args: unknown[]) => listDirectory(...args),
    statFile: (...args: unknown[]) => statFile(...args),
  },
}))

/**
 * The reduced-motion preference that the tree reads. jsdom has no `matchMedia`,
 * so the real module always reads false, and a test sets this instead.
 */
const reducedMotion = vi.hoisted(() => ({ on: false }))
vi.mock('~/lib/prefersReducedMotion', () => ({
  prefersReducedMotion: () => reducedMotion.on,
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
    listings: [
      {
        path: '',
        entries,
        truncated: opts.truncated ?? false,
        totalEntries: opts.totalEntries ?? 0,
      },
    ],
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

/**
 * Drain the microtask queue until `ready` holds.
 *
 * No timer runs in between, unlike in `waitFor`. A state that a timer ends,
 * such as a close that waits for its fallback timer, is therefore still there
 * when this returns.
 */
async function settleUntil(ready: () => boolean, turns = 100): Promise<void> {
  for (let i = 0; i < turns; i++) {
    if (ready())
      return
    await Promise.resolve()
  }
  throw new Error(`The condition did not hold within ${turns} microtasks.`)
}

function rowFor(name: string): Element | undefined {
  return [
    ...document.querySelectorAll('[data-testid="tree-row"]'),
  ]
    .find(el => el.querySelector('[data-testid="tree-row-name"]')?.textContent === name)
}

/**
 * The rendered row names, in order. Reads the name hook rather than the row's
 * textContent: the three-dot menu renders inside the row, and its items stay
 * mounted after the menu first opens, so the row's text can also carry them.
 */
function renderedNames(): string[] {
  return [
    ...document.querySelectorAll('[data-testid="tree-row"]'),
  ]
    .map(el => el.querySelector('[data-testid="tree-row-name"]')?.textContent ?? '')
}

beforeEach(() => {
  listDirectory.mockReset()
  statFile.mockReset()
  statFile.mockResolvedValue({
    info: { modTime: '2026-01-01T00:00:00Z' },
  })
  sessionStorageClearForTests()
  reducedMotion.on = false
})

/**
 * Run `body` with fake `setTimeout` and `clearTimeout`, so that no fallback
 * timer of a child group fires unless the test advances the clock. Microtasks
 * stay real, so `settle` and `settleUntil` still work. Use neither `waitFor`
 * nor `findBy*` inside: their own time limit runs on `setTimeout`, so a
 * condition that never holds waits until the test times out.
 */
async function withFakeTimeouts(body: () => Promise<void>): Promise<void> {
  vi.useFakeTimers({
    toFake: ['setTimeout', 'clearTimeout'],
  })
  try {
    await body()
  }
  finally {
    vi.useRealTimers()
  }
}

describe('DirectoryTree', () => {
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
    listDirectory.mockResolvedValue(oneListing([
      entry(root, 'a.txt'),
      entry(root, 'b.txt'),
    ]))

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

    listDirectory.mockResolvedValue(oneListing([
      entry(root, 'a.txt'),
      entry(root, 'b.txt'),
      entry(root, 'c.txt'),
    ]))
    handle.refresh()
    await waitFor(() => expect(rowFor('c.txt')).toBeTruthy())

    expect(rowFor('a.txt')).toBe(before)
    expect(before.isConnected).toBe(true)
  })

  it('marks the selected row with data-active, the one marker the coarse-pointer kebab reveal keys on', async () => {
    const root = '/repo-active'
    listDirectory.mockResolvedValue(oneListing([
      entry(root, 'a.txt'),
      entry(root, 'b.txt'),
    ]))

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

describe('DirectoryTree sorting', () => {
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
    expect(renderedNames()).toEqual([
      'alpha',
      'zeta',
      'apple.ts',
      'banana.md',
      'cherry.js',
    ])
  })

  it('sorts files by size while directories keep path order', async () => {
    renderSorted({ key: 'size', direction: 'asc' })
    await waitFor(() => expect(renderedNames()).toHaveLength(5))
    expect(renderedNames()).toEqual([
      'alpha',
      'zeta',
      'banana.md',
      'cherry.js',
      'apple.ts',
    ])
  })

  it('reverses only the files when a size sort is descending', async () => {
    renderSorted({ key: 'size', direction: 'desc' })
    await waitFor(() => expect(renderedNames()).toHaveLength(5))
    expect(renderedNames()).toEqual([
      'alpha',
      'zeta',
      'apple.ts',
      'cherry.js',
      'banana.md',
    ])
  })

  it('sorts files by modification time, newest first', async () => {
    renderSorted({ key: 'modified', direction: 'desc' })
    await waitFor(() => expect(renderedNames()).toHaveLength(5))
    expect(renderedNames()).toEqual([
      'alpha',
      'zeta',
      'banana.md',
      'cherry.js',
      'apple.ts',
    ])
  })

  it('groups files by extension under a type sort', async () => {
    renderSorted({ key: 'type', direction: 'asc' })
    await waitFor(() => expect(renderedNames()).toHaveLength(5))
    expect(renderedNames()).toEqual([
      'alpha',
      'zeta',
      'cherry.js',
      'banana.md',
      'apple.ts',
    ])
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
    expect(renderedNames()).toEqual([
      'zeta',
      'alpha',
      'cherry.js',
      'banana.md',
      'apple.ts',
    ])
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
    sessionStorageSet(`${PREFIX_DIRECTORY_TREE}w1:${root}:files`, ({
      expandedPaths: { [root]: true },
      childrenCache: {
        [root]: [
          {
            path: `${root}/stale.txt`,
            displayName: 'stale.txt',
            isDir: false,
            hidden: false,
          },
        ],
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
    sessionStorageSet(`${PREFIX_DIRECTORY_TREE}w1:${root}:files`, ({
      v: DIRECTORY_TREE_STATE_VERSION + 1,
      expandedPaths: { [root]: true },
      childrenCache: {
        [root]: [
          {
            path: `${root}/future.txt`,
            displayName: 'future.txt',
            isDir: false,
            hidden: false,
            size: 1,
            modTime: '2026-01-01T00:00:00Z',
          },
        ],
      },
      truncatedDirs: {},
    }))

    renderSorted({ key: 'name', direction: 'asc' })

    await waitFor(() => expect(listDirectory).toHaveBeenCalled())
    expect(renderedNames()).not.toContain('future.txt')
  })

  it('restores a payload at the current version without re-fetching', async () => {
    sessionStorageSet(`${PREFIX_DIRECTORY_TREE}w1:${root}:files`, ({
      v: DIRECTORY_TREE_STATE_VERSION,
      expandedPaths: { [root]: true },
      childrenCache: {
        [root]: [
          {
            path: `${root}/kept.txt`,
            displayName: 'kept.txt',
            isDir: false,
            hidden: false,
            size: 5,
            modTime: '2026-01-01T00:00:00Z',
          },
        ],
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
    sessionStorageSet(`${PREFIX_DIRECTORY_TREE}w1:${root}:files`, ({
      v: DIRECTORY_TREE_STATE_VERSION,
      expandedPaths: { [root]: true },
      childrenCache: {
        [root]: [
          {
            path: `${root}/kept.txt`,
            displayName: 'kept.txt',
            isDir: false,
            hidden: false,
            size: 5,
            modTime: '2026-01-01T00:00:00Z',
          },
        ],
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
  it('gives how many entries the directory really held', async () => {
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
describe('DirectoryTree root row info', () => {
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
    listDirectory.mockResolvedValue(oneListing([
      entry(root, 'a.txt'),
    ]))
    statFile.mockResolvedValue({
      info: { modTime: '2026-04-01T09:00:00Z' },
    })

    render(() => (
      <DirectoryTree workerId="w1" gitStatusStore={gitStatusStore} showFiles rootPath={root} selectedPath="" onSelect={() => {}} />
    ))

    await waitFor(() => expect(statFile).toHaveBeenCalledWith('w1', { path: root }))
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
    listDirectory.mockResolvedValue(oneListing([
      entry(root, 'a.txt'),
    ]))
    statFile.mockResolvedValue({
      info: { modTime: '2026-04-01T09:00:00Z' },
    })
    const [enabled, setEnabled] = createSignal(false)

    render(() => (
      <DirectoryTree workerId="w1" gitStatusStore={gitStatusStore} showFiles rootPath={root} selectedPath="" onSelect={() => {}} enabled={enabled()} />
    ))
    expect(statFile).not.toHaveBeenCalled()

    // A worktree-creating agent flips this true once its directory exists.
    setEnabled(true)
    await waitFor(() => expect(statFile).toHaveBeenCalledWith('w1', { path: root }))
  })

  /**
   * A refresh re-stats the same directory. Blanking the value first would make
   * the menu's Modified row blink out at every turn end, so the reset is scoped
   * to a change of worker or root.
   */
  it('keeps the root modified time on screen across a refresh', async () => {
    listDirectory.mockResolvedValue(oneListing([
      entry(root, 'a.txt'),
    ]))
    statFile.mockResolvedValueOnce({
      info: { modTime: '2026-04-01T09:00:00Z' },
    })

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
    listDirectory.mockResolvedValue(oneListing([
      entry(root, 'a.txt'),
    ]))
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
describe('DirectoryTree filtering', () => {
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
        {...(props.showHiddenFiles !== undefined
          ? { showHiddenFiles: props.showHiddenFiles }
          : {})}
        {...(props.isVisible !== undefined
          ? { isVisible: props.isVisible }
          : {})}
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
    renderFiltered({
      isVisible: path => path.endsWith('other.ts'),
    })
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
    renderFiltered({
      isVisible: () => false,
    })
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
        cur = cur.endsWith(separator)
          ? `${cur}${part}`
          : `${cur}${separator}${part}`
        chain.push(cur)
      }
    }
    else {
      chain.push(req.path)
    }
    return {
      listings: chain
        .flatMap((path) => {
          const entries = dirs[path]
          return entries !== undefined
            ? [
                {
                  path,
                  entries,
                  truncated: false,
                  totalEntries: entries.length,
                },
              ]
            : []
        }),
    }
  })
}

function dirEntry(parent: string, name: string, separator = '/') {
  return {
    name,
    path: parent.endsWith(separator)
      ? `${parent}${name}`
      : `${parent}${separator}${name}`,
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

interface StoredTree {
  expandedPaths?: Record<string, boolean>
  childrenCache?: Record<string, unknown>
}

/**
 * A stored tree state in which `${root}/src` is open and lists one directory,
 * `lib`, so a tree that restores it mounts `src` open.
 */
function openSrcState(root: string) {
  const dir = (path: string, displayName: string) => ({
    path,
    displayName,
    isDir: true,
    hidden: false,
    size: 0,
    modTime: '',
  })
  return {
    v: DIRECTORY_TREE_STATE_VERSION,
    expandedPaths: { [root]: true, [`${root}/src`]: true },
    childrenCache: {
      [root]: [
        dir(`${root}/src`, 'src'),
      ],
      [`${root}/src`]: [
        dir(`${root}/src/lib`, 'lib'),
      ],
    },
    truncatedDirs: {},
  }
}

/** The sorted keys of `expandedPaths` in the tree state stored under `key`. */
function storedExpandedPaths(key: string): string[] {
  return Object.keys(sessionStorageGet<StoredTree>(key)?.expandedPaths ?? {}).sort()
}

/** The sorted directories whose listings the tree state stored under `key` keeps. */
function storedListingPaths(key: string): string[] {
  return Object.keys(sessionStorageGet<StoredTree>(key)?.childrenCache ?? {}).sort()
}

/**
 * The child group of a directory row: the element that opens and closes.
 *
 * The row and its group are siblings inside one node element, and the group
 * holds the `role="group"` element that lists the children.
 */
function groupOf(row: Element): HTMLElement {
  const group = row.parentElement?.querySelector(':scope > div > [role="group"]')?.parentElement
  if (!group)
    throw new Error('The row has no child group.')
  return group
}

/** The element that scrolls the tree. */
function scrollContainer(): HTMLElement {
  const container = document.querySelector('[role="tree"]')?.parentElement
  if (!container)
    throw new Error('The tree has no scroll container.')
  return container
}

/**
 * Records every tree row that enters the document from now on.
 *
 * A row that a change mounts again is a new element, so this tells a row that
 * stayed mounted from one that was rebuilt.
 */
function recordAddedRows(): () => Element[] {
  const added: Element[] = []
  const collect = (records: MutationRecord[]) => {
    for (const record of records) {
      for (const node of record.addedNodes) {
        if (!(node instanceof Element))
          continue
        if (node.matches('[data-testid="tree-row"]'))
          added.push(node)
        added.push(...node.querySelectorAll('[data-testid="tree-row"]'))
      }
    }
  }
  const observer = new MutationObserver(collect)
  observer.observe(document.body, { childList: true, subtree: true })
  return () => {
    collect(observer.takeRecords())
    observer.disconnect()
    return added
  }
}

/**
 * Gives chosen elements a layout, because jsdom lays out nothing and reports
 * every rectangle as zero. Every other element keeps the zero rectangle.
 */
function mockLayout(rectOf: (el: Element) => { top: number, bottom: number } | undefined) {
  return vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
    const { top, bottom } = rectOf(this) ?? { top: 0, bottom: 0 }
    return {
      top,
      bottom,
      left: 0,
      right: 0,
      height: bottom - top,
      width: 0,
      x: 0,
      y: top,
      toJSON: () => ({}),
    }
  })
}

describe('DirectoryTree reveal', () => {
  const root = '/'

  it('expands toward revealPath when nothing is selected', async () => {
    mockChain({
      '/': [
        dirEntry('/', 'home'),
        dirEntry('/', 'opt'),
      ],
      '/home': [
        dirEntry('/home', 'alice'),
      ],
      '/home/alice': [
        dirEntry('/home/alice', 'proj'),
      ],
    })

    renderTree({ rootPath: root, revealPath: '/home/alice' })

    // The reveal opens every ancestor of the target, so the target's row shows.
    await waitFor(() => expect(rowFor('alice')).toBeTruthy())
    expect(rowFor('home')).toBeTruthy()
    // One chain request listed the target too, but the target stays closed, and a
    // closed directory mounts no rows.
    expect(listDirectory).toHaveBeenCalledTimes(1)
    expect(listDirectory.mock.calls[0]?.[1]).toMatchObject({ path: '/home/alice', fromRoot: '/' })
    expect(rowFor('proj')).toBeUndefined()
  })

  // The whole point of a separate prop: a revealed node is OPEN, never
  // selected. Seeding the selection instead would arm the New workspace
  // dialog's Create button with a directory the user never chose.
  it('does not mark the revealed node selected', async () => {
    mockChain({
      '/': [
        dirEntry('/', 'home'),
      ],
      '/home': [
        dirEntry('/home', 'alice'),
      ],
      '/home/alice': [],
    })

    renderTree({ rootPath: root, revealPath: '/home/alice' })

    await waitFor(() => expect(rowFor('alice')).toBeTruthy())
    expect(document.querySelectorAll('[data-active="true"]')).toHaveLength(0)
    expect(document.querySelector('[data-testid="tree-root-node"]')?.getAttribute('data-active')).toBe('false')
  })

  it('ignores revealPath once a path is selected', async () => {
    mockChain({
      '/': [
        dirEntry('/', 'home'),
      ],
      '/home': [
        dirEntry('/home', 'alice'),
      ],
      '/home/alice': [
        dirEntry('/home/alice', 'proj'),
      ],
    })

    renderTree({ rootPath: root, selectedPath: '/home', revealPath: '/home/alice' })

    await waitFor(() => expect(rowFor('home')).toBeTruthy())
    expect(rowFor('home')?.getAttribute('data-active')).toBe('true')
    // The reveal walked to the selection, not to `revealPath`.
    await waitFor(() => expect(listDirectory).toHaveBeenCalled())
    expect(listDirectory.mock.calls.map(call => (call[1] as { path: string }).path)).not.toContain('/home/alice')
    expect(rowFor('proj')).toBeUndefined()
  })

  it('ignores an empty revealPath', async () => {
    mockChain({
      '/': [
        dirEntry('/', 'home'),
      ],
    })

    renderTree({ rootPath: root, revealPath: '' })

    await waitFor(() => expect(rowFor('home')).toBeTruthy())
    expect(listDirectory).toHaveBeenCalledTimes(1)
    expect(listDirectory.mock.calls[0]?.[1]).toMatchObject({ path: '/' })
    expect(listDirectory.mock.calls[0]?.[1].fromRoot).toBeUndefined()
  })
})

describe('DirectoryTree chain loading', () => {
  it('fetches the whole root-to-reveal chain in one request', async () => {
    mockChain({
      '/': [
        dirEntry('/', 'home'),
      ],
      '/home': [
        dirEntry('/home', 'alice'),
      ],
      '/home/alice': [
        dirEntry('/home/alice', 'proj'),
      ],
      '/home/alice/proj': [],
    })

    renderTree({ rootPath: '/', revealPath: '/home/alice/proj' })

    await waitFor(() => expect(rowFor('proj')).toBeTruthy())
    expect(listDirectory).toHaveBeenCalledTimes(1)
    expect(listDirectory.mock.calls[0]?.[1]).toMatchObject({ path: '/home/alice/proj', fromRoot: '/' })
  })

  it('does not re-request when every chain directory is cached', async () => {
    mockChain({
      '/': [
        dirEntry('/', 'home'),
      ],
      '/home': [
        dirEntry('/home', 'alice'),
      ],
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
      '/': [
        dirEntry('/', 'home'),
        dirEntry('/', 'opt'),
      ],
      '/home': [
        dirEntry('/home', 'alice'),
      ],
      '/home/alice': [],
      '/opt': [
        dirEntry('/opt', 'tools'),
      ],
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
      '/': [
        dirEntry('/', 'home'),
      ],
      '/home': [
        dirEntry('/home', 'alice'),
      ],
      '/home/alice': [],
    }
    listDirectory.mockImplementation(async (_w: string, req: { path: string, fromRoot?: string }) => {
      if (req.fromRoot)
        throw new Error('chain boom')
      const entries = dirs[req.path] ?? []
      return {
        listings: [
          {
            path: req.path,
            entries,
            truncated: false,
            totalEntries: entries.length,
          },
        ],
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

    setSelected('/home/alice')
    await waitFor(() => expect(rowFor('alice')).toBeTruthy())
    expect(rowFor('home')).toBeTruthy()
    expect(document.body.textContent).not.toContain('Failed to load directory')
  })

  it('does not blank the tree to Loading when the selection changes', async () => {
    mockChain({
      '/': [
        dirEntry('/', 'home'),
      ],
      '/home': [
        dirEntry('/home', 'alice'),
      ],
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
      listings: [
        {
          path: '/private/tmp/ws',
          entries: [
            dirEntry('/private/tmp/ws', 'src'),
          ],
          truncated: false,
          totalEntries: 1,
        },
      ],
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
   * So `/tmp/ws/src` is a directory this chain listed and the cache never
   * receives: its guard misses forever. This effect subscribes to the cache it
   * writes, so without `lastChainKey` its own write would re-run it, and the
   * tree would issue the same request for as long as it stayed mounted.
   */
  it('does not re-fetch in a loop when the worker answers under paths the chain never listed', async () => {
    listDirectory.mockResolvedValue({
      listings: [
        {
          path: '/private/tmp/ws',
          entries: [
            dirEntry('/private/tmp/ws', 'src'),
          ],
          truncated: false,
          totalEntries: 1,
        },
        {
          path: '/private/tmp/ws/src',
          entries: [],
          truncated: false,
          totalEntries: 0,
        },
      ],
    })

    renderTree({ rootPath: '/tmp/ws', revealPath: '/tmp/ws/src' })

    await waitFor(() => expect(rowFor('src')).toBeTruthy())
    await settle()
    expect(listDirectory).toHaveBeenCalledTimes(1)
    expect(listDirectory.mock.calls[0]?.[1]).toMatchObject({ path: '/tmp/ws/src', fromRoot: '/tmp/ws' })
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
    mockChain({
      '/': [
        dirEntry('/', 'home'),
      ],
    })

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
    expect(listDirectory.mock.calls[1]?.[1]).toMatchObject({ path: '/', dirsOnly: false })
    await waitFor(() => expect(rowFor('home')).toBeTruthy())
  })

  it('drops a file target from the chain', async () => {
    mockChain({
      '/': [
        dirEntry('/', 'home'),
      ],
      '/home': [
        dirEntry('/home', 'alice'),
        {
          name: 'notes.txt',
          path: '/home/notes.txt',
          isDir: false,
          hidden: false,
          size: 4n,
          modTime: '2026-01-01T00:00:00Z',
        },
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

describe('DirectoryTree request de-duplication', () => {
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
        '/': [
          dirEntry('/', 'home'),
        ],
        '/home': [
          dirEntry('/home', 'alice'),
        ],
        '/home/alice': [],
      }
      const chain = req.fromRoot
        ? ['/', '/home', '/home/alice']
        : [req.path]
      return {
        listings: chain
          .flatMap((path) => {
            const entries = dirs[path]
            return entries !== undefined
              ? [
                  {
                    path,
                    entries,
                    truncated: false,
                    totalEntries: entries.length,
                  },
                ]
              : []
          }),
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
      '/': [
        dirEntry('/', 'home'),
      ],
      '/home': [
        dirEntry('/home', 'alice'),
      ],
      '/home/alice': [
        dirEntry('/home/alice', 'proj'),
      ],
    }
    listDirectory.mockImplementation(async (_w: string, req: { path: string, fromRoot?: string }) => {
      // The chain answer stops two levels short of the target.
      const chain = req.fromRoot
        ? ['/', '/home']
        : [req.path]
      return {
        listings: chain
          .flatMap((path) => {
            const entries = dirs[path]
            return entries !== undefined
              ? [
                  {
                    path,
                    entries,
                    truncated: false,
                    totalEntries: entries.length,
                  },
                ]
              : []
          }),
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

describe('DirectoryTree at a filesystem root', () => {
  it('labels a posix root row with the root itself', async () => {
    mockChain({
      '/': [
        dirEntry('/', 'home'),
      ],
    })

    renderTree({ rootPath: '/', flavor: 'posix' })

    await waitFor(() => expect(rowFor('home')).toBeTruthy())
    const rootRow = document.querySelector('[data-testid="tree-root-node"]')
    expect(rootRow?.querySelector('[data-testid="tree-row-name"]')?.textContent).toBe('/')
  })

  // `basename('C:\\', 'win32')` is 'C:' -- the drive WITHOUT the separator
  // that makes it a root, and not what the drive selector beside it reads.
  it('labels a windows drive root with the trailing separator', async () => {
    mockChain({
      'C:\\': [
        dirEntry('C:\\', 'Users', '\\'),
      ],
    }, '\\')

    renderTree({ rootPath: 'C:\\', flavor: 'win32' })

    await waitFor(() => expect(rowFor('Users')).toBeTruthy())
    const rootRow = document.querySelector('[data-testid="tree-root-node"]')
    expect(rootRow?.querySelector('[data-testid="tree-row-name"]')?.textContent).toBe('C:\\')
  })

  it('expands toward a windows revealPath', async () => {
    mockChain({
      'C:\\': [
        dirEntry('C:\\', 'Users', '\\'),
      ],
      'C:\\Users': [
        dirEntry('C:\\Users', 'alice', '\\'),
      ],
      'C:\\Users\\alice': [
        dirEntry('C:\\Users\\alice', 'proj', '\\'),
      ],
    }, '\\')

    renderTree({ rootPath: 'C:\\', flavor: 'win32', revealPath: 'C:\\Users\\alice' })

    await waitFor(() => expect(rowFor('alice')).toBeTruthy())
    expect(rowFor('Users')).toBeTruthy()
    // The target stays closed, so its rows stay unmounted.
    expect(rowFor('proj')).toBeUndefined()
    expect(listDirectory).toHaveBeenCalledTimes(1)
    expect(listDirectory.mock.calls[0]?.[1]).toMatchObject({ path: 'C:\\Users\\alice', fromRoot: 'C:\\' })
  })

  it('keys sessionStorage on the worker as well as the root path', async () => {
    mockChain({
      'C:\\': [
        dirEntry('C:\\', 'Users', '\\'),
      ],
    }, '\\')

    renderTree({ rootPath: 'C:\\', flavor: 'win32' })

    await waitFor(() => expect(rowFor('Users')).toBeTruthy())
    // The tree writes its state in an idle period.
    await waitFor(() => expect(sessionStorageGet(`${PREFIX_DIRECTORY_TREE}w1:C:\\:dirs`)).toBeTruthy())
  })

  /**
   * Two workers routinely share a root path -- every POSIX worker roots the
   * picker at `/`. Without the worker in the key, the second one restores the
   * first one's listing and then skips its own fetch.
   */
  it('does not serve one worker cached listing to another', async () => {
    mockChain({
      '/': [
        dirEntry('/', 'from-w1'),
      ],
    })
    const { unmount } = renderTree({ rootPath: '/' })
    await waitFor(() => expect(rowFor('from-w1')).toBeTruthy())
    unmount()

    mockChain({
      '/': [
        dirEntry('/', 'from-w2'),
      ],
    })
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

describe('DirectoryTree canonicalized chain', () => {
  /**
   * The worker canonicalizes what it lists, and a symlink BELOW the root
   * changes the level count with it (`/tmp/ws` is two levels under `/`,
   * `/private/tmp/ws` is three), so no pairing by index recovers the caller's
   * spelling. The chain effect re-keys the ROOT listing alone, and the middle
   * listings land under keys no node reads.
   *
   * The tree must still reach the target -- the per-node cascade re-lists each
   * such level under the path it asked for. This pins that recovery, so a
   * later change to the re-key cannot silently lose it.
   */
  it('still reveals the target when the worker answers under canonicalized paths', async () => {
    listDirectory.mockImplementation(async (_workerId: string, req: { path: string, fromRoot?: string }) => {
      if (req.fromRoot) {
        // The chain answers entirely under `/private/...`, so only the root
        // listing lands on a key the tree holds.
        return {
          listings: [
            {
              path: '/',
              entries: [
                dirEntry('/', 'tmp'),
              ],
              truncated: false,
              totalEntries: 1,
            },
            {
              path: '/private',
              entries: [
                dirEntry('/private', 'tmp'),
              ],
              truncated: false,
              totalEntries: 1,
            },
            {
              path: '/private/tmp',
              entries: [
                dirEntry('/private/tmp', 'ws'),
              ],
              truncated: false,
              totalEntries: 1,
            },
          ],
        }
      }
      // The per-node cascade asks for the path it holds, and gets it.
      const perPath: Record<string, ReturnType<typeof dirEntry>[]> = {
        '/tmp': [
          dirEntry('/tmp', 'ws'),
        ],
        '/tmp/ws': [
          dirEntry('/tmp/ws', 'src'),
        ],
      }
      return {
        listings: [
          {
            path: req.path,
            entries: perPath[req.path] ?? [],
            truncated: false,
            totalEntries: 0,
          },
        ],
      }
    })

    renderTree({ rootPath: '/', revealPath: '/tmp/ws' })

    // `tmp` comes from the re-keyed root listing; `ws` can only come from the
    // cascade re-listing `/tmp` under the spelling the tree actually holds.
    await waitFor(() => expect(rowFor('ws')).toBeTruthy())
    expect(rowFor('tmp')).toBeTruthy()
  })
})

describe('DirectoryTree refresh', () => {
  /**
   * The refresh path used to call the RPC directly, so it carried neither the
   * worker guard nor the in-flight claim. Two workers routinely share a root
   * path -- every POSIX worker roots the picker at `/` -- so a refresh answer
   * that landed after the tree moved to another worker wrote the previous
   * worker's listing into the new worker's tree, with no error and nothing on
   * screen to say the rows belonged to another machine.
   */
  it('drops a refresh response that lands after the tree switched worker', async () => {
    let releaseW1: ((v: unknown) => void) | undefined
    listDirectory.mockImplementation(async (workerId: string) => {
      if (workerId === 'w2') {
        return {
          listings: [
            {
              path: '/',
              entries: [
                dirEntry('/', 'from-w2'),
              ],
              truncated: false,
              totalEntries: 1,
            },
          ],
        }
      }
      if (releaseW1) {
        await new Promise((resolve) => {
          releaseW1 = resolve
        })
      }
      return {
        listings: [
          {
            path: '/',
            entries: [
              dirEntry('/', 'from-w1'),
            ],
            truncated: false,
            totalEntries: 1,
          },
        ],
      }
    })

    const [workerId, setWorkerId] = createSignal('w1')
    let handle: DirectoryTreeHandle | undefined
    const captureHandle = (h: DirectoryTreeHandle) => {
      handle = h
    }
    render(() => (
      <DirectoryTree
        workerId={workerId()}
        gitStatusStore={gitStatusStore}
        rootPath="/"
        selectedPath=""
        onSelect={() => {}}
        ref={captureHandle}
      />
    ))
    await waitFor(() => expect(rowFor('from-w1')).toBeTruthy())

    // The next w1 request hangs until this test releases it.
    releaseW1 = () => {}
    handle!.refresh()
    await new Promise(resolve => setTimeout(resolve, 0))

    setWorkerId('w2')
    await waitFor(() => expect(rowFor('from-w2')).toBeTruthy())

    releaseW1?.(undefined)
    await new Promise(resolve => setTimeout(resolve, 0))

    expect(rowFor('from-w1')).toBeUndefined()
    expect(rowFor('from-w2')).toBeTruthy()
  })
})

describe('DirectoryTree unreadable directories', () => {
  function nodeError(): string | undefined {
    return document.querySelector('[data-testid="tree-node-error"]')?.textContent ?? undefined
  }

  /**
   * The tree has ONE error slot and it belongs to the first load, when there
   * is nothing on screen at all. So a directory the user clicked and could not
   * read used to refuse to open and say nothing at all about why.
   */
  it('shows why a directory would not open', async () => {
    listDirectory.mockImplementation(async (_workerId: string, req: { path: string }) => {
      if (req.path === '/') {
        return {
          listings: [
            {
              path: '/',
              entries: [
                dirEntry('/', 'blocked'),
              ],
              truncated: false,
              totalEntries: 1,
            },
          ],
        }
      }
      throw new Error('permission denied')
    })

    renderTree({ rootPath: '/' })
    await waitFor(() => expect(rowFor('blocked')).toBeTruthy())

    fireEvent.click(rowFor('blocked')!)

    await waitFor(() => expect(nodeError()).toMatch(/permission denied/))
  })

  // The worker names the directory its CHAIN stopped at, and why. That one
  // never reaches the client as a rejection -- the request succeeded.
  it('shows the reason the worker reported for a chain it cut short', async () => {
    listDirectory.mockImplementation(async (_workerId: string, req: { path: string, fromRoot?: string }) => {
      if (req.fromRoot) {
        return {
          listings: [
            {
              path: '/',
              entries: [
                dirEntry('/', 'blocked'),
              ],
              truncated: false,
              totalEntries: 1,
            },
          ],
          unreadable: { path: '/blocked', reason: 'permission denied' },
        }
      }
      // The per-node cascade asks the same directory and is refused too --
      // which is why the chain reported it in the first place.
      throw new Error('permission denied')
    })

    renderTree({ rootPath: '/', revealPath: '/blocked/inner' })

    await waitFor(() => expect(nodeError()).toMatch(/permission denied/))
  })

  // A node whose listing failed stays COLLAPSED, so nothing re-lists it and a
  // refresh cannot clear its reason by arriving with data. Refresh clears the
  // reasons itself -- it means "everything you know is stale" -- which also
  // lifts the "do not ask again" mark, so the next click retries.
  it('clears the reason on refresh, and the next click retries', async () => {
    let fail = true
    listDirectory.mockImplementation(async (_workerId: string, req: { path: string }) => {
      if (req.path === '/') {
        return {
          listings: [
            {
              path: '/',
              entries: [
                dirEntry('/', 'blocked'),
              ],
              truncated: false,
              totalEntries: 1,
            },
          ],
        }
      }
      if (fail)
        throw new Error('permission denied')
      return {
        listings: [
          {
            path: '/blocked',
            entries: [
              dirEntry('/blocked', 'inner'),
            ],
            truncated: false,
            totalEntries: 1,
          },
        ],
      }
    })

    let handle: DirectoryTreeHandle | undefined
    const captureHandle = (h: DirectoryTreeHandle) => {
      handle = h
    }
    renderTree({ rootPath: '/', ref: captureHandle })
    await waitFor(() => expect(rowFor('blocked')).toBeTruthy())
    fireEvent.click(rowFor('blocked')!)
    await waitFor(() => expect(nodeError()).toMatch(/permission denied/))

    fail = false
    handle!.refresh()
    await waitFor(() => expect(nodeError()).toBeUndefined())

    fireEvent.click(rowFor('blocked')!)

    await waitFor(() => expect(rowFor('inner')).toBeTruthy())
    expect(nodeError()).toBeUndefined()
  })

  // The counterpart: a listing that arrives on its own clears the reason too,
  // so a stale message never sits over a directory that lists perfectly well.
  it('clears the reason when the directory lists on a later click', async () => {
    let fail = true
    listDirectory.mockImplementation(async (_workerId: string, req: { path: string }) => {
      if (req.path === '/') {
        return {
          listings: [
            {
              path: '/',
              entries: [
                dirEntry('/', 'blocked'),
              ],
              truncated: false,
              totalEntries: 1,
            },
          ],
        }
      }
      if (fail) {
        fail = false
        throw new Error('permission denied')
      }
      return {
        listings: [
          {
            path: '/blocked',
            entries: [
              dirEntry('/blocked', 'inner'),
            ],
            truncated: false,
            totalEntries: 1,
          },
        ],
      }
    })

    renderTree({ rootPath: '/' })
    await waitFor(() => expect(rowFor('blocked')).toBeTruthy())
    fireEvent.click(rowFor('blocked')!)
    await waitFor(() => expect(nodeError()).toMatch(/permission denied/))

    fireEvent.click(rowFor('blocked')!)

    await waitFor(() => expect(rowFor('inner')).toBeTruthy())
    expect(nodeError()).toBeUndefined()
  })
})

describe('DirectoryTree failure handling', () => {
  /**
   * A directory the caller cannot read fails every time it is asked, and a
   * tree rooted at `/` puts several of those in front of every user
   * (`/root`, `/lost+found`). Marking the node expanded anyway satisfied the
   * "expanded but the cache is missing" effect forever, and that effect reads
   * `loading()` -- so each failure re-armed it. The browser then sent one
   * ListDirectory per round trip for as long as the picker was open.
   */
  it('does not expand or re-request a directory whose listing failed', async () => {
    listDirectory.mockImplementation(async (_workerId: string, req: { path: string }) => {
      if (req.path === '/') {
        return {
          listings: [
            {
              path: '/',
              entries: [
                dirEntry('/', 'blocked'),
              ],
              truncated: false,
              totalEntries: 1,
            },
          ],
        }
      }
      throw new Error('permission denied')
    })

    renderTree({ rootPath: '/' })
    await waitFor(() => expect(rowFor('blocked')).toBeTruthy())

    fireEvent.click(rowFor('blocked')!)
    await waitFor(() => expect(listDirectory.mock.calls.some(c => c[1].path === '/blocked')).toBe(true))

    const afterClick = listDirectory.mock.calls.length
    // Several macrotasks: a loop driven by the effect would issue one request
    // per settled promise, so any growth at all is the defect.
    for (let i = 0; i < 5; i++)
      await new Promise(resolve => setTimeout(resolve, 0))

    expect(listDirectory.mock.calls.length).toBe(afterClick)
  })

  // Refresh is the user's own "try again", so it must re-arm a node the
  // failure above shut off. Without this the only way back is to reopen the
  // dialog.
  it('re-requests a failed directory after a refresh', async () => {
    listDirectory.mockImplementation(async (_workerId: string, req: { path: string }) => {
      if (req.path === '/') {
        return {
          listings: [
            {
              path: '/',
              entries: [
                dirEntry('/', 'blocked'),
              ],
              truncated: false,
              totalEntries: 1,
            },
          ],
        }
      }
      throw new Error('permission denied')
    })

    let handle: DirectoryTreeHandle | undefined
    const captureHandle = (h: DirectoryTreeHandle) => {
      handle = h
    }
    renderTree({ rootPath: '/', ref: captureHandle })
    await waitFor(() => expect(rowFor('blocked')).toBeTruthy())
    fireEvent.click(rowFor('blocked')!)
    await waitFor(() => expect(listDirectory.mock.calls.some(c => c[1].path === '/blocked')).toBe(true))
    const afterClick = listDirectory.mock.calls.length

    handle!.refresh()
    await waitFor(() => expect(listDirectory.mock.calls.length).toBeGreaterThan(afterClick))
  })
})

describe('DirectoryTree win32 separators', () => {
  /**
   * `PathInput` submits whatever the user typed, so a win32 selection reaches
   * the tree spelled `C:/Users/alice` while the worker's own listings spell it
   * `C:\Users\alice`. `relativeUnder` requires one separator on both sides, so
   * an un-normalized comparison answered "not under" and collapsed the whole
   * reveal: no chain request, no node expanded, no row selected.
   */
  it('reveals a target typed with forward slashes on a win32 tree', async () => {
    mockChain({
      'C:\\': [
        dirEntry('C:\\', 'Users', '\\'),
      ],
      'C:\\Users': [
        dirEntry('C:\\Users', 'alice', '\\'),
      ],
      'C:\\Users\\alice': [
        dirEntry('C:\\Users\\alice', 'proj', '\\'),
      ],
    }, '\\')

    renderTree({ rootPath: 'C:\\', flavor: 'win32', revealPath: 'C:/Users/alice' })

    // `alice` is the discriminator: it renders only once `C:\Users` expands,
    // which needs the reveal target to compare as a descendant of it. Without
    // the normalization the chain collapses to the root alone, so the request
    // carries no from_root and `C:\Users` is never listed.
    await waitFor(() => expect(rowFor('alice')).toBeTruthy())
    expect(listDirectory.mock.calls[0]?.[1].fromRoot).toBe('C:\\')
  })

  // The worker reports a name in its own case; a typed path need not match it.
  // A `===` comparison then treats a node the tree already holds as one it has
  // never seen.
  it('matches a target whose case differs from the worker listing', async () => {
    mockChain({
      'C:\\': [
        dirEntry('C:\\', 'Users', '\\'),
      ],
      'C:\\Users': [
        dirEntry('C:\\Users', 'Alice', '\\'),
      ],
      'C:\\Users\\Alice': [
        dirEntry('C:\\Users\\Alice', 'proj', '\\'),
      ],
    }, '\\')

    renderTree({ rootPath: 'C:\\', flavor: 'win32', revealPath: 'c:\\users\\alice' })

    await waitFor(() => expect(rowFor('Alice')).toBeTruthy())
  })

  // The payload keeps the listing of the reveal target, which the cache keys
  // by the worker's spelling. A target typed in another spelling must still
  // find it, or a remount fetches the target again.
  it('persists the listing of a reveal target typed with forward slashes', async () => {
    mockChain({
      'C:\\': [
        dirEntry('C:\\', 'Users', '\\'),
      ],
      'C:\\Users': [
        dirEntry('C:\\Users', 'alice', '\\'),
      ],
      'C:\\Users\\alice': [
        dirEntry('C:\\Users\\alice', 'proj', '\\'),
      ],
    }, '\\')
    const { unmount } = renderTree({ rootPath: 'C:\\', flavor: 'win32', revealPath: 'C:/Users/alice' })
    await waitFor(() => expect(rowFor('alice')).toBeTruthy())
    const afterFirst = listDirectory.mock.calls.length
    unmount()

    renderTree({ rootPath: 'C:\\', flavor: 'win32', revealPath: 'C:/Users/alice' })
    await waitFor(() => expect(rowFor('alice')).toBeTruthy())
    await settle()

    expect(listDirectory.mock.calls.length).toBe(afterFirst)
  })
})

describe('DirectoryTree expandPath', () => {
  const root = '/expand-handle'
  const storeKey = `${PREFIX_DIRECTORY_TREE}w1:${root}:dirs`

  /**
   * The paths the tree holds open, sorted.
   *
   * Read from the persisted state, NOT from the rendered rows: a closed
   * directory keeps its rows mounted while its close animates, so `rowFor` can
   * find a row that no user can see.
   */
  function expandedPaths(): string[] {
    return storedExpandedPaths(storeKey)
  }

  function mockTwoLevels() {
    listDirectory.mockImplementation(async (_workerId: string, req: { path: string, fromRoot?: string }) => {
      if (req.fromRoot) {
        return {
          listings: [
            {
              path: root,
              entries: [
                dirEntry(root, 'home'),
              ],
              truncated: false,
              totalEntries: 1,
            },
            {
              path: `${root}/home`,
              entries: [
                dirEntry(`${root}/home`, 'alice'),
              ],
              truncated: false,
              totalEntries: 1,
            },
          ],
        }
      }
      if (req.path === `${root}/home`) {
        return oneListing([
          dirEntry(`${root}/home`, 'alice'),
        ])
      }
      return oneListing([
        dirEntry(root, 'home'),
      ])
    })
  }

  // The reveal effect opens every ANCESTOR of its target and stops there, so a
  // selection alone never opens the directory it selected.
  it('opens the node that the reveal leaves closed', async () => {
    mockTwoLevels()
    let handle!: DirectoryTreeHandle
    const captureHandle = (h: DirectoryTreeHandle) => {
      handle = h
    }
    renderTree({ rootPath: root, selectedPath: `${root}/home`, ref: captureHandle })
    await waitFor(() => expect(rowFor('home')).toBeTruthy())
    // The tree writes its state in an idle period.
    await waitFor(() => expect(expandedPaths()).toEqual([root]))

    handle.expandPath(`${root}/home`)

    await waitFor(() => expect(expandedPaths()).toEqual([root, `${root}/home`]))
  })

  /**
   * The picker's Home button does exactly this pair, in this order.
   *
   * Selecting a home directory on another Windows drive re-roots the tree, and
   * a new root REPLACES the expansion state wholesale. The write therefore has
   * to land after that replacement, which it does because a signal write runs
   * the whole update cycle before it returns.
   */
  it('survives a root change made in the same tick', async () => {
    listDirectory.mockImplementation(async (_workerId: string, req: { path: string }) => {
      if (req.path === 'C:\\') {
        return oneListing([
          dirEntry('C:\\', 'Users', '\\'),
        ])
      }
      return oneListing([
        dirEntry('D:\\', 'work', '\\'),
      ])
    })
    const [treeRoot, setTreeRoot] = createSignal('D:\\')
    let handle!: DirectoryTreeHandle
    const captureHandle = (h: DirectoryTreeHandle) => {
      handle = h
    }
    render(() => (
      <DirectoryTree
        workerId="w1"
        gitStatusStore={gitStatusStore}
        rootPath={treeRoot()}
        selectedPath=""
        onSelect={() => {}}
        ref={captureHandle}
      />
    ))
    await waitFor(() => expect(rowFor('work')).toBeTruthy())

    setTreeRoot('C:\\')
    handle.expandPath('C:\\Users')

    // The tree writes its state in an idle period.
    await waitFor(() => expect(storedExpandedPaths(`${PREFIX_DIRECTORY_TREE}w1:C:\\:dirs`)).toEqual(['C:\\', 'C:\\Users']))
  })

  // An empty path names no node. The key would persist, and no chevron could
  // ever collapse it again.
  it('ignores an empty path instead of persisting a key for it', async () => {
    mockTwoLevels()
    let handle!: DirectoryTreeHandle
    const captureHandle = (h: DirectoryTreeHandle) => {
      handle = h
    }
    const { unmount } = renderTree({ rootPath: root, ref: captureHandle })
    await waitFor(() => expect(rowFor('home')).toBeTruthy())
    await waitFor(() => expect(expandedPaths()).toEqual([root]))

    handle.expandPath('')
    // An unmount writes a waiting state at once, so the read below sees any
    // change that the empty path made.
    unmount()

    expect(expandedPaths()).toEqual([root])
  })
})

describe('DirectoryTree keyboard navigation', () => {
  const root = '/aria'

  /**
   * Two directories at the root, one of them holding a file.
   *
   * Both children are directories on purpose: the visible order is then a
   * plain name sort, whatever the comparator does with files. `inner.txt`
   * gives the walk a row that IS in the DOM while its parent closes.
   */
  function mockTree() {
    listDirectory.mockImplementation(async (_workerId: string, req: { path: string }) => {
      if (req.path === `${root}/alpha`) {
        return oneListing([
          entry(`${root}/alpha`, 'inner.txt'),
        ])
      }
      if (req.path === `${root}/beta`)
        return oneListing([])
      return oneListing([
        dirEntry(root, 'alpha'),
        dirEntry(root, 'beta'),
      ])
    })
  }

  function treeEl(): HTMLElement {
    return document.querySelector('[role="tree"]') as HTMLElement
  }

  function rootRow(): HTMLElement {
    return document.querySelector('[data-testid="tree-root-node"]') as HTMLElement
  }

  // Dispatched on the focused row, not on the container, so it travels the
  // path a browser gives it and the delegation is under test too.
  function press(key: string) {
    fireEvent.keyDown(document.activeElement ?? treeEl(), { key })
  }

  async function renderReady(props: Record<string, unknown> = {}) {
    mockTree()
    renderTree({ rootPath: root, ...props })
    await waitFor(() => expect(rowFor('alpha')).toBeTruthy())
    await settle()
  }

  it('marks the tree, its rows and its groups', async () => {
    await renderReady()

    expect(treeEl()).toBeTruthy()
    expect(rootRow().getAttribute('role')).toBe('treeitem')
    expect(rootRow().getAttribute('aria-expanded')).toBe('true')
    expect(rootRow().getAttribute('aria-level')).toBe('1')

    expect(rowFor('alpha')!.getAttribute('role')).toBe('treeitem')
    expect(rowFor('alpha')!.getAttribute('aria-expanded')).toBe('false')
    expect(rowFor('alpha')!.getAttribute('aria-level')).toBe('2')
    expect(document.querySelectorAll('[role="group"]').length).toBeGreaterThan(0)
  })

  // A row that can never open must say nothing about expansion, or a screen
  // reader announces the file as a collapsed directory.
  it('leaves aria-expanded off a file row', async () => {
    await renderReady()
    rootRow().focus()
    press('ArrowDown')
    press('ArrowRight')
    await waitFor(() => expect(rowFor('inner.txt')).toBeTruthy())

    expect(rowFor('inner.txt')!.hasAttribute('aria-expanded')).toBe(false)
    expect(rowFor('inner.txt')!.getAttribute('aria-level')).toBe('3')
  })

  // A tree is ONE stop in the page's tab order.
  it('holds exactly one tab stop, on the root until a row takes focus', async () => {
    await renderReady()

    let stops = document.querySelectorAll('[role="treeitem"][tabindex="0"]')
    expect(stops).toHaveLength(1)
    expect(stops[0]).toBe(rootRow())

    rootRow().focus()
    press('ArrowDown')

    stops = document.querySelectorAll('[role="treeitem"][tabindex="0"]')
    expect(stops).toHaveLength(1)
    expect(stops[0]).toBe(rowFor('alpha'))
  })

  it('steps through the VISIBLE rows, not the rendered ones', async () => {
    await renderReady()
    rootRow().focus()

    press('ArrowDown')
    expect(document.activeElement).toBe(rowFor('alpha'))

    // Open it and close it again, so its child stays RENDERED, hidden, while
    // the close animates.
    // That is the row a walk of the DOM would step into: it sits between
    // `alpha` and `beta` there, and nowhere on screen.
    press('ArrowRight')
    await waitFor(() => expect(rowFor('inner.txt')).toBeTruthy())
    press('ArrowLeft')
    await waitFor(() => expect(rowFor('alpha')!.getAttribute('aria-expanded')).toBe('false'))
    expect(rowFor('inner.txt')).toBeTruthy()

    press('ArrowDown')
    expect(document.activeElement).toBe(rowFor('beta'))

    // The last row: the focus stays rather than wrapping.
    press('ArrowDown')
    expect(document.activeElement).toBe(rowFor('beta'))

    press('ArrowUp')
    expect(document.activeElement).toBe(rowFor('alpha'))
  })

  it('opens with ArrowRight, descends on the second press, and closes with ArrowLeft', async () => {
    await renderReady()
    rootRow().focus()
    press('ArrowDown')

    // The first press opens the directory and does NOT move the focus.
    press('ArrowRight')
    await waitFor(() => expect(rowFor('alpha')!.getAttribute('aria-expanded')).toBe('true'))
    expect(document.activeElement).toBe(rowFor('alpha'))

    // The listing lands after the expansion, so the child row is not there
    // the moment the directory reports itself open.
    await waitFor(() => expect(rowFor('inner.txt')).toBeTruthy())
    press('ArrowRight')
    expect(document.activeElement).toBe(rowFor('inner.txt'))

    // A row that cannot close moves to its parent instead.
    press('ArrowLeft')
    expect(document.activeElement).toBe(rowFor('alpha'))

    press('ArrowLeft')
    expect(rowFor('alpha')!.getAttribute('aria-expanded')).toBe('false')
    expect(document.activeElement).toBe(rowFor('alpha'))
  })

  it('jumps to the first and last visible rows with Home and End', async () => {
    await renderReady()
    rootRow().focus()

    press('End')
    expect(document.activeElement).toBe(rowFor('beta'))

    press('Home')
    expect(document.activeElement).toBe(rootRow())
  })

  it('activates the focused row on Enter, exactly as a click does', async () => {
    const onSelect = vi.fn()
    await renderReady({ onSelect })
    rootRow().focus()
    press('ArrowDown')

    press('Enter')

    // A click both selects and toggles, so Enter must do both too. It awaits
    // the listing before it selects, so this cannot be a synchronous check.
    await waitFor(() => expect(onSelect).toHaveBeenCalledWith(`${root}/alpha`))
    expect(rowFor('alpha')!.getAttribute('aria-expanded')).toBe('true')
  })

  // The tab stop cannot name a row that is no longer on screen, or the tree
  // drops out of the tab order entirely.
  it('returns the tab stop to the root when the focused row is collapsed away', async () => {
    let handle!: DirectoryTreeHandle
    const captureHandle = (h: DirectoryTreeHandle) => {
      handle = h
    }
    await renderReady({ ref: captureHandle })
    rootRow().focus()
    press('ArrowDown')
    press('ArrowRight')
    await waitFor(() => expect(rowFor('inner.txt')).toBeTruthy())
    press('ArrowRight')
    expect(document.activeElement).toBe(rowFor('inner.txt'))

    // Collapse All, which is the one way to close the directory under the
    // focused row: ArrowLeft would move to the parent before it closed it.
    handle.collapseAll()
    await settle()

    const stops = document.querySelectorAll('[role="treeitem"][tabindex="0"]')
    expect(stops).toHaveLength(1)
    expect(stops[0]).toBe(rootRow())
  })
})

describe('DirectoryTree child groups', () => {
  const root = '/groups'

  /**
   * `src` holds a directory and a file. The file is the row a selection
   * scrolls to: the scroll skips a closed directory.
   */
  function mockTree() {
    listDirectory.mockImplementation(async (_workerId: string, req: { path: string }) => {
      if (req.path === root) {
        return oneListing([
          dirEntry(root, 'src'),
        ])
      }
      if (req.path === `${root}/src`) {
        return oneListing([
          dirEntry(`${root}/src`, 'lib'),
          entry(`${root}/src`, 'main.ts'),
        ])
      }
      return oneListing([])
    })
  }

  /** Stores a tree state in which `src` is open, so `src` mounts open. */
  function seedOpenSrc() {
    sessionStorageSet(`${PREFIX_DIRECTORY_TREE}w1:${root}:files`, openSrcState(root))
  }

  // The finished close is what unmounts the rows. Unmounting them at the
  // collapse itself would cut the close animation off.
  it('keeps the rows of a directory that mounted open until its first close ends', async () => {
    mockTree()
    seedOpenSrc()
    renderTree({ rootPath: root, showFiles: true })
    await waitFor(() => expect(rowFor('lib')).toBeTruthy())
    const lib = rowFor('lib')!

    fireEvent.click(rowFor('src')!)
    await settleUntil(() => rowFor('src')!.getAttribute('aria-expanded') === 'false')

    expect(lib.isConnected).toBe(true)
    expect(groupOf(rowFor('src')!).classList.contains(childrenWrapperExpanded)).toBe(false)
    fireEvent.transitionEnd(groupOf(rowFor('src')!))
    expect(lib.isConnected).toBe(false)
    expect(rowFor('lib')).toBeUndefined()
  })

  it('mounts no row when a directory closes', async () => {
    mockTree()
    renderTree({ rootPath: root })
    await waitFor(() => expect(rowFor('src')).toBeTruthy())
    fireEvent.click(rowFor('src')!)
    await waitFor(() => expect(rowFor('lib')).toBeTruthy())
    const lib = rowFor('lib')!
    const addedRows = recordAddedRows()

    fireEvent.click(rowFor('src')!)
    await waitFor(() => expect(rowFor('lib')).toBeUndefined())

    expect(addedRows()).toEqual([])
    expect(lib.isConnected).toBe(false)
  })

  // A transition that never runs fires no `transitionend`, so a timer ends the
  // close instead.
  it('unmounts the rows of a closed directory without a transitionend', async () => {
    mockTree()
    seedOpenSrc()
    renderTree({ rootPath: root, showFiles: true })
    await waitFor(() => expect(rowFor('lib')).toBeTruthy())

    fireEvent.click(rowFor('src')!)

    await waitFor(() => expect(rowFor('lib')).toBeUndefined())
  })

  // The rows inside the group and their chevrons run transitions of their own,
  // and each `transitionend` of those bubbles up to the group.
  it('keeps the rows of a closing directory through a transitionend that bubbles up from inside its group', async () => {
    mockTree()
    seedOpenSrc()
    renderTree({ rootPath: root, showFiles: true })
    await waitFor(() => expect(rowFor('lib')).toBeTruthy())
    const lib = rowFor('lib')!

    await withFakeTimeouts(async () => {
      fireEvent.click(rowFor('src')!)
      await settleUntil(() => rowFor('src')!.getAttribute('aria-expanded') === 'false')
      lib.dispatchEvent(new Event('transitionend', { bubbles: true }))
      await settle()

      expect(lib.isConnected).toBe(true)
      groupOf(rowFor('src')!).dispatchEvent(new Event('transitionend', { bubbles: true }))
      expect(lib.isConnected).toBe(false)
    })
  })

  // The transition starts at the next style recalculation, which can come a
  // frame or more after the class changes, so the wait outlasts the transition.
  it('keeps the rows of a closing directory past the length of the transition, then unmounts them without a transitionend', async () => {
    mockTree()
    seedOpenSrc()
    renderTree({ rootPath: root, showFiles: true })
    await waitFor(() => expect(rowFor('lib')).toBeTruthy())
    const lib = rowFor('lib')!

    await withFakeTimeouts(async () => {
      fireEvent.click(rowFor('src')!)
      await settleUntil(() => rowFor('src')!.getAttribute('aria-expanded') === 'false')
      vi.advanceTimersByTime(CHILDREN_TRANSITION_MS)
      await settle()

      expect(lib.isConnected).toBe(true)
      vi.advanceTimersByTime(1000)
      await settle()
      expect(lib.isConnected).toBe(false)
    })
  })

  // Under reduced motion the style has no transition, so no `transitionend`
  // comes, and the close is complete when the class changes.
  it('unmounts the rows of a closed directory at once under reduced motion', async () => {
    reducedMotion.on = true
    mockTree()
    seedOpenSrc()
    renderTree({ rootPath: root, showFiles: true })
    await waitFor(() => expect(rowFor('lib')).toBeTruthy())
    const lib = rowFor('lib')!

    fireEvent.click(rowFor('src')!)

    // Microtasks only: no timer runs, and no `transitionend` fires.
    await settleUntil(() => !lib.isConnected)
    expect(rowFor('lib')).toBeUndefined()
  })

  // A reopen during the close keeps the rows it was about to unmount.
  it('keeps the rows when a directory reopens before its close ends', async () => {
    mockTree()
    seedOpenSrc()
    renderTree({ rootPath: root, showFiles: true })
    await waitFor(() => expect(rowFor('lib')).toBeTruthy())
    const lib = rowFor('lib')!
    const addedRows = recordAddedRows()

    fireEvent.click(rowFor('src')!)
    await settleUntil(() => rowFor('src')!.getAttribute('aria-expanded') === 'false')
    fireEvent.click(rowFor('src')!)
    await settleUntil(() => rowFor('src')!.getAttribute('aria-expanded') === 'true')
    // A `transitionend` of the reopen, and the timer of the abandoned close,
    // must both leave the rows in place.
    fireEvent.transitionEnd(groupOf(rowFor('src')!))
    await settle()

    expect(lib.isConnected).toBe(true)
    expect(addedRows()).toEqual([])
  })

  // An element that the browser has not styled yet takes the open class as its
  // first style, so its open would not animate and would fire no
  // `transitionend`. jsdom computes no transitions, so this pins the order.
  it('reads the style of a group that mounted closed before it opens', async () => {
    mockTree()
    renderTree({ rootPath: root })
    await waitFor(() => expect(rowFor('src')).toBeTruthy())
    const realGetComputedStyle = window.getComputedStyle.bind(window)
    const reads: { el: Element, open: boolean }[] = []
    const spy = vi.spyOn(window, 'getComputedStyle').mockImplementation((el, pseudo) => {
      reads.push({
        el,
        open: el.classList.contains(childrenWrapperExpanded),
      })
      return realGetComputedStyle(el, pseudo)
    })
    try {
      // The first open of an uncached directory: its group mounts closed when
      // the listing lands, and opens a microtask later.
      fireEvent.click(rowFor('src')!)
      await waitFor(() => expect(rowFor('lib')).toBeTruthy())

      const group = groupOf(rowFor('src')!)
      expect(group.classList.contains(childrenWrapperExpanded)).toBe(true)
      expect(reads.filter(read => read.el === group)).toEqual([
        { el: group, open: false },
      ])
    }
    finally {
      spy.mockRestore()
    }
  })

  // A group that mounts open does not animate, so it has nothing to read.
  it('reads no style for a group that mounted open', async () => {
    mockTree()
    seedOpenSrc()
    const spy = vi.spyOn(window, 'getComputedStyle')
    try {
      renderTree({ rootPath: root, showFiles: true })
      await waitFor(() => expect(rowFor('lib')).toBeTruthy())

      const group = groupOf(rowFor('src')!)
      expect(spy.mock.calls.filter(call => call[0] === group)).toEqual([])
    }
    finally {
      spy.mockRestore()
    }
  })

  describe('scrolling', () => {
    /**
     * Lays the tree out so that a scroll is due: the container shows 100px,
     * the node of `src` ends below that, and so does the row of `main.ts`.
     */
    function layOut() {
      return mockLayout((el) => {
        if (el === scrollContainer())
          return { top: 0, bottom: 100 }
        const src = rowFor('src')
        if (src && el === src.parentElement)
          return { top: 80, bottom: 200 }
        if (el === src)
          return { top: 80, bottom: 100 }
        if (el === rowFor('main.ts'))
          return { top: 500, bottom: 520 }
        return undefined
      })
    }

    // A transition that never runs fires no `transitionend`, and the scroll
    // that waits for it must not wait forever.
    it('scrolls an opened directory into view without a transitionend', async () => {
      mockTree()
      renderTree({ rootPath: root })
      await waitFor(() => expect(rowFor('src')).toBeTruthy())
      const layout = layOut()
      try {
        fireEvent.click(rowFor('src')!)
        await settleUntil(() => rowFor('lib') !== undefined)
        // The open has not ended, so the children are not at their full height.
        expect(scrollContainer().scrollTop).toBe(0)

        // 100px of the node overflow, and the clamp keeps its 20px row in
        // view: 80 - 0 - 20.
        await waitFor(() => expect(scrollContainer().scrollTop).toBe(60))
      }
      finally {
        layout.mockRestore()
      }
    })

    it('scrolls an opened directory into view at once under reduced motion', async () => {
      reducedMotion.on = true
      mockTree()
      renderTree({ rootPath: root })
      await waitFor(() => expect(rowFor('src')).toBeTruthy())
      const layout = layOut()
      try {
        fireEvent.click(rowFor('src')!)

        // Microtasks only: no timer runs, and no `transitionend` fires.
        await settleUntil(() => scrollContainer().scrollTop === 60)
      }
      finally {
        layout.mockRestore()
      }
    })

    // The scroll waits for the open to end. A close before that leaves no
    // children to bring into view.
    it('drops the scroll of an open that a close ends first', async () => {
      mockTree()
      renderTree({ rootPath: root })
      await waitFor(() => expect(rowFor('src')).toBeTruthy())
      const layout = layOut()
      try {
        await withFakeTimeouts(async () => {
          fireEvent.click(rowFor('src')!)
          await settleUntil(() => rowFor('lib') !== undefined)
          fireEvent.click(rowFor('src')!)
          await settleUntil(() => rowFor('src')!.getAttribute('aria-expanded') === 'false')

          groupOf(rowFor('src')!).dispatchEvent(new Event('transitionend', { bubbles: true }))
          vi.advanceTimersByTime(1000)
          await settle()

          expect(rowFor('lib')).toBeUndefined()
          expect(scrollContainer().scrollTop).toBe(0)
        })
      }
      finally {
        layout.mockRestore()
      }
    })

    it('scrolls an opened directory into view when its open ends', async () => {
      mockTree()
      renderTree({ rootPath: root })
      await waitFor(() => expect(rowFor('src')).toBeTruthy())
      const layout = layOut()
      try {
        fireEvent.click(rowFor('src')!)
        await settleUntil(() => rowFor('lib') !== undefined)
        expect(scrollContainer().scrollTop).toBe(0)

        fireEvent.transitionEnd(groupOf(rowFor('src')!))

        expect(scrollContainer().scrollTop).toBe(60)
      }
      finally {
        layout.mockRestore()
      }
    })

    // While a group above the row opens, the rows it clips add no scrollable
    // height, so a scroll to the row then stops short of it.
    it('scrolls to a selected row once the groups above it finish opening', async () => {
      mockTree()
      const [selected, setSelected] = createSignal('')
      render(() => (
        <DirectoryTree
          workerId="w1"
          gitStatusStore={gitStatusStore}
          showFiles
          rootPath={root}
          selectedPath={selected()}
          onSelect={() => {}}
        />
      ))
      await waitFor(() => expect(rowFor('src')).toBeTruthy())
      // Cache the listing of `src`, then close it again.
      fireEvent.click(rowFor('src')!)
      await waitFor(() => expect(rowFor('main.ts')).toBeTruthy())
      fireEvent.click(rowFor('src')!)
      await waitFor(() => expect(rowFor('main.ts')).toBeUndefined())
      const layout = layOut()
      try {
        // The reveal opens `src` from its cache, in the same update.
        setSelected(`${root}/src/main.ts`)
        expect(rowFor('main.ts')).toBeTruthy()
        expect(scrollContainer().scrollTop).toBe(0)

        fireEvent.transitionEnd(groupOf(rowFor('src')!))

        expect(scrollContainer().scrollTop).toBe(500)
      }
      finally {
        layout.mockRestore()
      }
    })
  })
})

describe('DirectoryTree closed directories and persistence', () => {
  const root = '/closing'
  const storeKey = `${PREFIX_DIRECTORY_TREE}w1:${root}:dirs`

  function mockTree() {
    listDirectory.mockImplementation(async (_workerId: string, req: { path: string }) => {
      if (req.path === root) {
        return oneListing([
          dirEntry(root, 'src'),
        ])
      }
      if (req.path === `${root}/src`) {
        return oneListing([
          dirEntry(`${root}/src`, 'lib'),
        ])
      }
      return oneListing([])
    })
  }

  function renderWithHandle() {
    let handle!: DirectoryTreeHandle
    const result = renderTree({
      rootPath: root,
      ref: (h: DirectoryTreeHandle) => {
        handle = h
      },
    })
    return {
      ...result,
      handle: () => handle,
    }
  }

  it('unmounts the rows of a closed directory once it closes, and reopens it from the cache', async () => {
    mockTree()
    renderTree({ rootPath: root })
    await waitFor(() => expect(rowFor('src')).toBeTruthy())
    fireEvent.click(rowFor('src')!)
    await waitFor(() => expect(rowFor('lib')).toBeTruthy())
    const requests = listDirectory.mock.calls.length

    fireEvent.click(rowFor('src')!)
    await waitFor(() => expect(rowFor('lib')).toBeUndefined())

    fireEvent.click(rowFor('src')!)
    await waitFor(() => expect(rowFor('lib')).toBeTruthy())
    expect(listDirectory.mock.calls.length).toBe(requests)
  })

  it('writes its state in an idle period, not in the change itself', async () => {
    mockTree()
    const { handle } = renderWithHandle()
    await waitFor(() => expect(storedExpandedPaths(storeKey)).toEqual([root]))

    handle().expandPath(`${root}/src`)

    expect(storedExpandedPaths(storeKey)).toEqual([root])
    await waitFor(() => expect(storedExpandedPaths(storeKey)).toEqual([root, `${root}/src`]))
  })

  it('writes a waiting state at once when it unmounts', async () => {
    mockTree()
    const { handle, unmount } = renderWithHandle()
    await waitFor(() => expect(storedExpandedPaths(storeKey)).toEqual([root]))

    handle().expandPath(`${root}/src`)
    unmount()

    expect(storedExpandedPaths(storeKey)).toEqual([root, `${root}/src`])
  })

  // A reload or a tab close runs no Solid cleanup, so the unmount flush never
  // runs then. `pagehide` does fire.
  it('writes a waiting state at once when the page hides', async () => {
    mockTree()
    const { handle } = renderWithHandle()
    await waitFor(() => expect(storedExpandedPaths(storeKey)).toEqual([root]))

    handle().expandPath(`${root}/src`)
    window.dispatchEvent(new Event('pagehide'))

    expect(storedExpandedPaths(storeKey)).toEqual([root, `${root}/src`])
  })

  it('stops listening for pagehide when it unmounts', () => {
    mockTree()
    const add = vi.spyOn(window, 'addEventListener')
    const remove = vi.spyOn(window, 'removeEventListener')
    try {
      const { unmount } = renderTree({ rootPath: root })
      unmount()

      const added = add.mock.calls.filter(call => call[0] === 'pagehide').map(call => call[1])
      const removed = remove.mock.calls.filter(call => call[0] === 'pagehide').map(call => call[1])
      expect(added).toHaveLength(1)
      expect(removed).toEqual(added)
    }
    finally {
      add.mockRestore()
      remove.mockRestore()
    }
  })

  it('persists the listings of open directories only', async () => {
    mockTree()
    renderTree({ rootPath: root })
    await waitFor(() => expect(rowFor('src')).toBeTruthy())
    fireEvent.click(rowFor('src')!)
    await waitFor(() => expect(storedListingPaths(storeKey)).toEqual([root, `${root}/src`]))

    fireEvent.click(rowFor('src')!)

    await waitFor(() => expect(storedExpandedPaths(storeKey)).toEqual([root]))
    expect(storedListingPaths(storeKey)).toEqual([root])
  })

  // The payload keeps the listing of the reveal target, so a selection change
  // alone changes what it holds, although the store does not change.
  it('persists the listing of a newly selected directory that the cache holds', async () => {
    mockTree()
    const [selected, setSelected] = createSignal('')
    render(() => (
      <DirectoryTree
        workerId="w1"
        gitStatusStore={gitStatusStore}
        rootPath={root}
        selectedPath={selected()}
        onSelect={() => {}}
      />
    ))
    await waitFor(() => expect(rowFor('src')).toBeTruthy())
    fireEvent.click(rowFor('src')!)
    await waitFor(() => expect(storedListingPaths(storeKey)).toEqual([root, `${root}/src`]))
    fireEvent.click(rowFor('src')!)
    await waitFor(() => expect(storedListingPaths(storeKey)).toEqual([root]))

    setSelected(`${root}/src`)

    await waitFor(() => expect(storedListingPaths(storeKey)).toEqual([root, `${root}/src`]))
  })

  /**
   * The new root's restore must read the new root's own state.
   *
   * A memo that reads the root and runs again alone, such as the visible-row
   * walk on a toggle of hidden files, reorders the computations that the root
   * notifies. A persist effect that reads the key can then run before the
   * restore effect. The restore then writes the old root's state under the new
   * key, and reads it straight back.
   */
  it('restores a new root from its own state after a hidden-files toggle', async () => {
    listDirectory.mockImplementation(async (_workerId: string, req: { path: string }) => {
      if (req.path === '/r1') {
        return oneListing([
          dirEntry('/r1', 'a'),
        ])
      }
      if (req.path === '/r2') {
        return oneListing([
          dirEntry('/r2', 'b'),
        ])
      }
      return oneListing([])
    })
    const [treeRoot, setTreeRoot] = createSignal('/r1')
    const [showHidden, setShowHidden] = createSignal(true)
    let handle!: DirectoryTreeHandle
    render(() => (
      <DirectoryTree
        workerId="w1"
        gitStatusStore={gitStatusStore}
        rootPath={treeRoot()}
        showHiddenFiles={showHidden()}
        selectedPath=""
        onSelect={() => {}}
        ref={(h) => {
          handle = h
        }}
      />
    ))
    await waitFor(() => expect(rowFor('a')).toBeTruthy())
    handle.expandPath('/r1/a')
    await waitFor(() => expect(storedExpandedPaths(`${PREFIX_DIRECTORY_TREE}w1:/r1:dirs`)).toEqual(['/r1', '/r1/a']))

    setShowHidden(false)
    setTreeRoot('/r2')

    await waitFor(() => expect(rowFor('b')).toBeTruthy())
    await waitFor(() => expect(storedListingPaths(`${PREFIX_DIRECTORY_TREE}w1:/r2:dirs`)).toEqual(['/r2']))
    expect(storedExpandedPaths(`${PREFIX_DIRECTORY_TREE}w1:/r2:dirs`)).toEqual(['/r2'])
    expect(storedExpandedPaths(`${PREFIX_DIRECTORY_TREE}w1:/r1:dirs`)).toEqual(['/r1', '/r1/a'])
  })

  // The restore of the new root replaces the state in the store. A waiting write
  // of the old root that ran after the restore would store the state of the new
  // root under the key of the old one, and nothing would store it under its own.
  it('writes a waiting state under the root that it came from when the root changes', async () => {
    listDirectory.mockImplementation(async (_workerId: string, req: { path: string }) => {
      if (req.path === '/r1') {
        return oneListing([
          dirEntry('/r1', 'a'),
        ])
      }
      if (req.path === '/r2') {
        return oneListing([
          dirEntry('/r2', 'b'),
        ])
      }
      return oneListing([])
    })
    const r1Key = `${PREFIX_DIRECTORY_TREE}w1:/r1:dirs`
    const r2Key = `${PREFIX_DIRECTORY_TREE}w1:/r2:dirs`
    const [treeRoot, setTreeRoot] = createSignal('/r1')
    let handle!: DirectoryTreeHandle
    render(() => (
      <DirectoryTree
        workerId="w1"
        gitStatusStore={gitStatusStore}
        rootPath={treeRoot()}
        selectedPath=""
        onSelect={() => {}}
        ref={(h) => {
          handle = h
        }}
      />
    ))
    await waitFor(() => expect(storedExpandedPaths(r1Key)).toEqual(['/r1']))

    handle.expandPath('/r1/a')
    setTreeRoot('/r2')

    // At once, before any idle period.
    expect(storedExpandedPaths(r1Key)).toEqual(['/r1', '/r1/a'])
    await waitFor(() => expect(storedExpandedPaths(r2Key)).toEqual(['/r2']))
    expect(storedExpandedPaths(r1Key)).toEqual(['/r1', '/r1/a'])
  })

  describe('an account switch', () => {
    // The namespace moves before any listener runs, so a write of the waiting
    // state would store this account's tree under the next account's key.
    it('writes no waiting state into the namespace of the next account', async () => {
      mockTree()
      const { handle, unmount } = renderWithHandle()
      await waitFor(() => expect(storedExpandedPaths(storeKey)).toEqual([root]))

      handle().expandPath(`${root}/src`)
      setStorageAccountForTests('user-b')
      unmount()

      expect(storedExpandedPaths(storeKey)).not.toContain(`${root}/src`)
    })

    it('writes no state of the previous account at idle time', async () => {
      mockTree()
      const { handle } = renderWithHandle()
      await waitFor(() => expect(storedExpandedPaths(storeKey)).toEqual([root]))

      handle().expandPath(`${root}/src`)
      setStorageAccountForTests('user-b')

      await waitFor(() => expect(storedListingPaths(storeKey)).toEqual([root]))
      expect(storedExpandedPaths(storeKey)).toEqual([root])
    })

    // The tree mirrors an account-scoped key, so it follows the account.
    it('restores the tree that the next account stored', async () => {
      mockTree()
      setStorageAccountForTests('user-b')
      sessionStorageSet(storeKey, openSrcState(root))
      setStorageAccountForTests('user-a')
      renderTree({ rootPath: root })
      await waitFor(() => expect(rowFor('src')).toBeTruthy())
      expect(rowFor('src')!.getAttribute('aria-expanded')).toBe('false')

      setStorageAccountForTests('user-b')

      await waitFor(() => expect(rowFor('lib')).toBeTruthy())
      expect(rowFor('src')!.getAttribute('aria-expanded')).toBe('true')
    })
  })
})
