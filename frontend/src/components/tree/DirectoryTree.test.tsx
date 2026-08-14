import type { DirectoryTreeHandle } from './DirectoryTree'
import type { FileSortOrder } from '~/lib/fileSort'
import { fireEvent, render, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { PREFIX_DIRECTORY_TREE, sessionStorageSet } from '~/lib/browserStorage'
import { DIRECTORY_TREE_STATE_VERSION, DirectoryTree } from './DirectoryTree'

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
  sessionStorage.clear()
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
    listDirectory.mockResolvedValue({
      entries: [entry(root, 'a.txt'), entry(root, 'b.txt')],
      truncated: false,
    })

    let handle!: DirectoryTreeHandle
    render(() => (
      <DirectoryTree
        workerId="w1"
        showFiles
        rootPath={root}
        selectedPath=""
        onSelect={() => {}}
        ref={(h) => { handle = h }}
      />
    ))
    await waitFor(() => expect(rowFor('a.txt')).toBeTruthy())
    const before = rowFor('a.txt')!

    listDirectory.mockResolvedValue({
      entries: [entry(root, 'a.txt'), entry(root, 'b.txt'), entry(root, 'c.txt')],
      truncated: false,
    })
    handle.refresh()
    await waitFor(() => expect(rowFor('c.txt')).toBeTruthy())

    expect(rowFor('a.txt')).toBe(before)
    expect(before.isConnected).toBe(true)
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
    listDirectory.mockResolvedValue({ entries, truncated: false })
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
    sessionStorageSet(`${PREFIX_DIRECTORY_TREE}${root}:files`, JSON.stringify({
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
    sessionStorageSet(`${PREFIX_DIRECTORY_TREE}${root}:files`, JSON.stringify({
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
    sessionStorageSet(`${PREFIX_DIRECTORY_TREE}${root}:files`, JSON.stringify({
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
    sessionStorageSet(`${PREFIX_DIRECTORY_TREE}${root}:files`, JSON.stringify({
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
    listDirectory.mockResolvedValue({ entries, truncated: true, totalEntries: 12043 })
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
    listDirectory.mockResolvedValue({ entries, truncated: true })
    const { container } = renderSorted({ key: 'name', direction: 'asc' })
    await waitFor(() => expect(container.textContent).toContain('listing truncated'))
    expect(container.textContent).toContain('5+ entries')
    expect(container.textContent).not.toContain('of 0 entries')
  })

  it('says the listing was truncated by name when sorting by something else', async () => {
    listDirectory.mockResolvedValue({ entries, truncated: true })
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
    listDirectory.mockResolvedValue({ entries: [entry(root, 'a.txt')], truncated: false })
    statFile.mockResolvedValue({ info: { modTime: '2026-04-01T09:00:00Z' } })

    render(() => (
      <DirectoryTree workerId="w1" showFiles rootPath={root} selectedPath="" onSelect={() => {}} />
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
    listDirectory.mockResolvedValue({ entries: [entry(root, 'a.txt')], truncated: false })
    statFile.mockResolvedValue({ info: { modTime: '2026-04-01T09:00:00Z' } })
    const [enabled, setEnabled] = createSignal(false)

    render(() => (
      <DirectoryTree workerId="w1" showFiles rootPath={root} selectedPath="" onSelect={() => {}} enabled={enabled()} />
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
    listDirectory.mockResolvedValue({ entries: [entry(root, 'a.txt')], truncated: false })
    statFile.mockResolvedValueOnce({ info: { modTime: '2026-04-01T09:00:00Z' } })

    let handle!: DirectoryTreeHandle
    render(() => (
      <DirectoryTree
        workerId="w1"
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
    listDirectory.mockResolvedValue({ entries: [entry(root, 'a.txt')], truncated: false })
    statFile.mockRejectedValue(new Error('permission denied'))

    render(() => (
      <DirectoryTree workerId="w1" showFiles rootPath={root} selectedPath="" onSelect={() => {}} />
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
    listDirectory.mockResolvedValue({ entries, truncated: false })
  })

  function renderFiltered(props: { showHiddenFiles?: boolean, isVisible?: (path: string) => boolean }) {
    return render(() => (
      <DirectoryTree
        workerId="w1"
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
