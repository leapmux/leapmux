import type { Section, SectionItem } from '~/generated/proto/leapmux/v1/section_pb'
import type { Workspace } from '~/generated/proto/leapmux/v1/workspace_pb'
import type { Tab } from '~/stores/tab.types'
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useWorkspaceOperations } from '~/components/shell/useWorkspaceOperations'
import {
  resetWorkspaceListStateForTests,
  setSectionFilterQuery,
  setWorkspaceSortOrder,
  toggleSectionFilter,
} from '~/components/workspace/workspaceListState'
import { SectionType } from '~/generated/proto/leapmux/v1/section_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { localStorageClearForTests, setStorageAccount } from '~/lib/browserStorage'
import { createSectionStore } from '~/stores/section.store'

interface TabRefLike { tabType: TabType, tabId: string }
interface WorkerTabsLike { workerId: string, tabs: TabRefLike[] }

const mockDeleteWorkspace = vi.fn<(req: { workspaceId: string }) => Promise<{ workerTabs: WorkerTabsLike[] }>>()
const mockRenameWorkspace = vi.fn<(req: { workspaceId: string, title: string }) => Promise<unknown>>()
const mockCleanupWorkspace = vi.fn<(workerId: string, req: { tabs: TabRefLike[] }) => Promise<unknown>>()
const mockMoveWorkspace = vi.fn<(req: { workspaceId: string, sectionId: string, position: string }) => Promise<unknown>>()
const mockShowWarnToast = vi.fn()

vi.mock('~/api/clients', () => ({
  workspaceClient: {
    deleteWorkspace: (...args: unknown[]) => mockDeleteWorkspace(...args as [{ workspaceId: string }]),
    renameWorkspace: (...args: unknown[]) => mockRenameWorkspace(...args as [{ workspaceId: string, title: string }]),
  },
  sectionClient: {
    // Stubbed, and its ABSENCE used to hide a bug: `moveWorkspace` threw on
    // `undefined.moveWorkspace`, the hook's own catch swallowed it into a
    // toast, and every test that moved a workspace passed for the wrong
    // reason.
    moveWorkspace: (...args: unknown[]) => mockMoveWorkspace(...args as [{ workspaceId: string, sectionId: string, position: string }]),
  },
}))

vi.mock('~/api/workerRpc', () => ({
  cleanupWorkspace: (...args: unknown[]) =>
    mockCleanupWorkspace(...args as [string, { tabs: TabRefLike[] }]),
}))

vi.mock('~/components/common/Toast', () => ({
  showWarnToast: (...args: unknown[]) => mockShowWarnToast(...args),
}))

interface HarnessOpts {
  onDeleteWorkspace?: (deletedId: string, nextId: string | null) => void
}

/**
 * The smallest wiring `deleteWorkspace` needs: a section store (consulted only
 * for the post-delete "which workspace next" walk) plus the callbacks it awaits.
 *
 * Deliberately does NOT stub `deleteWorkspace` -- each test owns that, because
 * the worker/tab grouping it answers with is the input under test. There is no
 * tab accessor to configure any more: the Hub reads the tabs inside the delete
 * transaction and returns them, so the response IS the whole input.
 */
function harness(opts: HarnessOpts = {}) {
  return createRoot(dispose => ({
    dispose,
    ops: useWorkspaceOperations({
      workspaces: () => [] as Workspace[],
      activeWorkspaceId: () => null,
      sectionStore: createSectionStore(),
      loadSections: async () => {},
      onSelectWorkspace: () => {},
      onRefreshWorkspaces: () => {},
      onDeleteWorkspace: opts.onDeleteWorkspace ?? (() => {}),
    }),
  }))
}

/**
 * Every tab each worker was handed, as `type:id` pairs keyed by worker id.
 *
 * The TYPE is part of the key on purpose. An earlier version of this helper
 * projected it away and compared id lists only, which made the suite blind to
 * the field the Worker actually switches on: `handleCleanupWorkspace` routes on
 * tab_type and falls through to `default:` for an unrecognized one, closing
 * NOTHING. Sending the right ids with the wrong types would have passed.
 */
function cleanupCalls(): Map<string, string[]> {
  const out = new Map<string, string[]>()
  for (const [workerId, req] of mockCleanupWorkspace.mock.calls)
    out.set(workerId, req.tabs.map(t => `${TabType[t.tabType]}:${t.tabId}`))
  return out
}

describe('useWorkspaceOperations deleteWorkspace', () => {
  beforeEach(() => {
    mockDeleteWorkspace.mockReset()
    mockCleanupWorkspace.mockReset()
    mockCleanupWorkspace.mockResolvedValue({})
    mockShowWarnToast.mockReset()
  })

  it('hands each worker only the tabs it hosts, with their types intact', async () => {
    mockDeleteWorkspace.mockResolvedValue({
      workerTabs: [
        { workerId: 'w1', tabs: [
          { tabType: TabType.AGENT, tabId: 'a-1' },
          { tabType: TabType.TERMINAL, tabId: 't-1' },
        ] },
        { workerId: 'w2', tabs: [{ tabType: TabType.FILE, tabId: 'f-2' }] },
      ],
    })
    const h = harness()
    try {
      await h.ops.deleteWorkspace('ws-1')

      const calls = cleanupCalls()
      expect(calls.get('w1')).toEqual(['AGENT:a-1', 'TERMINAL:t-1'])
      // Tab ids are unique per user, not per worker, so a leaked id is a live
      // tab on the wrong machine that CleanupWorkspace would close.
      expect(calls.get('w2')).toEqual(['FILE:f-2'])
    }
    finally {
      h.dispose()
    }
  })

  /**
   * The ordering problem this design removed. The client used to snapshot the
   * tab list from its own CRDT projection BEFORE calling delete, because nothing
   * could answer afterwards. That was three bugs at once: a tab a peer opened in
   * between was missed, the accessor was optional so a caller that omitted it
   * silently asked every worker to close nothing, and the projection it read is
   * a strict SUBSET of the owned tabs, so a projection-hidden tab was
   * structurally unreachable rather than merely reclaimed late.
   *
   * Now the Hub reads the authoritative rows inside the delete transaction. The
   * assertion is that the client sends what the RESPONSE said, with no local
   * snapshot involved -- so a projection that is empty, stale, or absent cannot
   * change what gets torn down.
   */
  it('tears down exactly what the delete response named', async () => {
    mockDeleteWorkspace.mockResolvedValue({
      workerTabs: [{ workerId: 'w1', tabs: [{ tabType: TabType.AGENT, tabId: 'a-hidden' }] }],
    })
    const h = harness()
    try {
      await h.ops.deleteWorkspace('ws-1')
      expect(cleanupCalls().get('w1')).toEqual(['AGENT:a-hidden'])
    }
    finally {
      h.dispose()
    }
  })

  /**
   * The wire shape permits an empty group, and such a worker must still be
   * called rather than skipped: the call is a harmless no-op its orphan
   * reconciler covers, and skipping it would silently narrow the fan-out.
   */
  it('calls a worker whose tab group is empty', async () => {
    mockDeleteWorkspace.mockResolvedValue({ workerTabs: [{ workerId: 'w-empty', tabs: [] }] })
    const h = harness()
    try {
      await h.ops.deleteWorkspace('ws-1')
      expect(cleanupCalls().get('w-empty')).toEqual([])
    }
    finally {
      h.dispose()
    }
  })

  it('makes no cleanup calls when the workspace held no tabs', async () => {
    mockDeleteWorkspace.mockResolvedValue({ workerTabs: [] })
    const h = harness()
    try {
      await h.ops.deleteWorkspace('ws-1')

      expect(mockDeleteWorkspace).toHaveBeenCalledWith({ workspaceId: 'ws-1' })
      expect(mockCleanupWorkspace).not.toHaveBeenCalled()
      expect(mockShowWarnToast).not.toHaveBeenCalled()
    }
    finally {
      h.dispose()
    }
  })

  /**
   * The per-worker cleanup is best effort by design: an offline worker's RPC
   * rejects, and the delete has already committed on the hub. Surfacing that as
   * "Failed to delete workspace" would be a lie, and awaiting it unguarded
   * would abort the refresh/selection work that follows.
   */
  it('completes the delete when a worker cleanup rejects', async () => {
    mockCleanupWorkspace.mockRejectedValue(new Error('worker offline'))
    mockDeleteWorkspace.mockResolvedValue({
      workerTabs: [{ workerId: 'w1', tabs: [{ tabType: TabType.AGENT, tabId: 'a-1' }] }],
    })
    const onDeleteWorkspace = vi.fn()
    const h = harness({ onDeleteWorkspace })
    try {
      await h.ops.deleteWorkspace('ws-1')

      expect(onDeleteWorkspace).toHaveBeenCalledWith('ws-1', null)
      expect(mockShowWarnToast).not.toHaveBeenCalled()
    }
    finally {
      h.dispose()
    }
  })
})

// ---------------------------------------------------------------------------
// buildSectionGroups
// ---------------------------------------------------------------------------

function ws(id: string): Workspace {
  return { id, title: id } as Workspace
}

function section(id: string, sectionType: SectionType): Section {
  return { id, sectionType } as Section
}

function item(workspaceId: string, sectionId: string, position: string): SectionItem {
  return { workspaceId, sectionId, position } as SectionItem
}

/**
 * Builds the harness with a populated section store and workspace list, and
 * returns the grouped result keyed by section id.
 */
function groupsFor(workspaces: Workspace[], sections: Section[], items: SectionItem[]) {
  return createRoot((dispose) => {
    const sectionStore = createSectionStore()
    sectionStore.setSections(sections)
    sectionStore.setItems(items)
    const ops = useWorkspaceOperations({
      workspaces: () => workspaces,
      activeWorkspaceId: () => null,
      sectionStore,
      loadSections: async () => {},
      onSelectWorkspace: () => {},
      onRefreshWorkspaces: () => {},
      onDeleteWorkspace: () => {},
    })
    const built = ops.buildSectionGroups(sections)
    dispose()
    return new Map(built.map(g => [g.section.id, g.workspaces.map(w => w.id)]))
  })
}

describe('useWorkspaceOperations buildSectionGroups', () => {
  const inProgress = section('s-progress', SectionType.WORKSPACES_IN_PROGRESS)
  const archived = section('s-archived', SectionType.WORKSPACES_ARCHIVED)

  it('lists each section\'s workspaces in position order', () => {
    const groups = groupsFor(
      [ws('w1'), ws('w2')],
      [inProgress, archived],
      [item('w2', 's-progress', 'b'), item('w1', 's-progress', 'a')],
    )
    expect(groups.get('s-progress')).toEqual(['w1', 'w2'])
    expect(groups.get('s-archived')).toEqual([])
  })

  it('renders a workspace once when the item table carries it twice', () => {
    // A CLI- or cross-worker-created workspace reaches the client through both
    // the CRDT projection and the lifecycle event, so the item table can hold
    // two rows for it until they reconcile. Rendering per item put two nodes
    // with the same `workspace-item-<id>` test id on screen -- a duplicated row
    // for the user and a Playwright strict-mode violation for any spec that
    // addresses the row by id.
    const groups = groupsFor(
      [ws('w1')],
      [inProgress],
      [item('w1', 's-progress', 'a'), item('w1', 's-progress', 'b')],
    )
    expect(groups.get('s-progress')).toEqual(['w1'])
  })

  it('renders a workspace once when it is assigned to two sections', () => {
    // Mid-move the same workspace can hold an item in both the old and the new
    // section. It must stay in ONE of them rather than appear in both.
    const groups = groupsFor(
      [ws('w1')],
      [inProgress, archived],
      [item('w1', 's-progress', 'a'), item('w1', 's-archived', 'a')],
    )
    const all = [...groups.values()].flat()
    expect(all).toEqual(['w1'])
  })

  it('appends unassigned workspaces to the in-progress section exactly once', () => {
    const groups = groupsFor(
      [ws('w1'), ws('w2')],
      [inProgress, archived],
      [item('w1', 's-progress', 'a')],
    )
    expect(groups.get('s-progress')).toEqual(['w1', 'w2'])
  })

  it('does not append an unassigned workspace that a section already placed', () => {
    // `unassigned` is computed from the whole item table, so a workspace whose
    // only item lives in ARCHIVED is not unassigned -- but a workspace placed
    // by a section during this very build must not be re-appended either.
    const groups = groupsFor(
      [ws('w1')],
      [inProgress, archived],
      [item('w1', 's-archived', 'a')],
    )
    expect(groups.get('s-archived')).toEqual(['w1'])
    expect(groups.get('s-progress')).toEqual([])
  })
})

/**
 * `commitRename` sends the CLEANED title, not the raw one.
 *
 * The hub applies `validate.SanitizeName` to whatever arrives, so a raw send
 * left the sidebar showing one name while the hub stored another until the
 * next refresh overwrote it. The gap widened when the rule started to FOLD: a
 * plain double space is a far more common typo than a control character was.
 */
describe('useWorkspaceOperations commitRename', () => {
  beforeEach(() => {
    mockRenameWorkspace.mockReset()
    mockRenameWorkspace.mockResolvedValue({})
    mockShowWarnToast.mockReset()
  })

  async function rename(typed: string) {
    const h = harness()
    try {
      h.ops.startRename({ id: 'ws-1', title: 'Old' } as Workspace)
      h.ops.onRenameInput(typed)
      await h.ops.commitRename()
    }
    finally {
      h.dispose()
    }
  }

  it.each([
    ['a repeated space', 'Auth  fix', 'Auth fix'],
    ['a tab', 'Auth\tfix', 'Auth fix'],
    ['surrounding whitespace', '  Auth fix  ', 'Auth fix'],
    ['a newline', 'Auth\nfix', 'Auth fix'],
    ['a no-break space', 'Auth\u00A0fix', 'Auth fix'],
    ['an invisible format character', 'Auth\u200Bfix', 'Authfix'],
    ['a control character', 'Auth\u0000fix', 'Authfix'],
  ])('sends the cleaned title when the input carries %s', async (_label, typed, stored) => {
    await rename(typed)
    expect(mockRenameWorkspace).toHaveBeenCalledWith({ workspaceId: 'ws-1', title: stored })
  })

  // The punctuation the rule now KEEPS must reach the hub untouched, so the
  // clean does not become a second, stricter character ban on this side.
  it('sends visible punctuation unchanged', async () => {
    await rename('100% of $HOME "quoted"')
    expect(mockRenameWorkspace).toHaveBeenCalledWith({
      workspaceId: 'ws-1',
      title: '100% of $HOME "quoted"',
    })
  })

  // A title that cleans to nothing is not a rename. It takes the same answer
  // an empty input takes: cancel, and send no RPC.
  it.each([
    ['an empty input', ''],
    ['only whitespace', '   '],
    ['only invisible characters', '\u200B\uFEFF\u00AD'],
  ])('sends nothing for %s', async (_label, typed) => {
    await rename(typed)
    expect(mockRenameWorkspace).not.toHaveBeenCalled()
  })
})

// ----- Bulk archive operations -----

/**
 * The hook wired to a populated section store, for the two bulk operations.
 *
 * They read the ARCHIVED section's items at the start of each run, so the store
 * has to be real rather than empty.
 */
function bulkHarness(archivedIds: readonly string[], opts: {
  onConfirmEmptyArchive?: (count: number) => Promise<boolean>
  onConfirmDelete?: (workspaceId: string) => Promise<boolean>
  onRefreshWorkspaces?: () => void
} = {}) {
  const inProgress = section('s-progress', SectionType.WORKSPACES_IN_PROGRESS)
  const archived = section('s-archived', SectionType.WORKSPACES_ARCHIVED)
  return createRoot((dispose) => {
    const sectionStore = createSectionStore()
    sectionStore.setSections([inProgress, archived])
    sectionStore.setItems(archivedIds.map((id, i) => item(id, 's-archived', `p${i}`)))
    const ops = useWorkspaceOperations({
      workspaces: () => archivedIds.map(ws),
      activeWorkspaceId: () => null,
      sectionStore,
      loadSections: async () => {},
      onSelectWorkspace: () => {},
      onRefreshWorkspaces: opts.onRefreshWorkspaces ?? (() => {}),
      onDeleteWorkspace: () => {},
      onConfirmDelete: opts.onConfirmDelete,
      onConfirmEmptyArchive: opts.onConfirmEmptyArchive,
    })
    return { dispose, ops, sectionStore }
  })
}

describe('useWorkspaceOperations unarchiveAll', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockMoveWorkspace.mockResolvedValue({})
  })

  it('moves every archived workspace to In progress', async () => {
    const h = bulkHarness(['w1', 'w2', 'w3'])
    await h.ops.unarchiveAll()
    expect(mockMoveWorkspace.mock.calls.map(c => c[0].workspaceId)).toEqual(['w1', 'w2', 'w3'])
    expect(mockMoveWorkspace.mock.calls.every(c => c[0].sectionId === 's-progress')).toBe(true)
    h.dispose()
  })

  it('gives every move a DISTINCT position', async () => {
    // This is what separates the sequential loop from `Promise.all`.
    // `moveWorkspace` computes `appendPosition(getItemsForSection(...))`, which
    // reads `items.at(-1)` BEFORE its await while the store write lands after
    // it -- so a parallel fan-out hands every call the identical lexorank and
    // the sidebar shuffles the whole set on the next refresh.
    const h = bulkHarness(['w1', 'w2', 'w3'])
    await h.ops.unarchiveAll()
    const positions = mockMoveWorkspace.mock.calls.map(c => c[0].position)
    expect(new Set(positions).size).toBe(positions.length)
    h.dispose()
  })

  it('does nothing for an empty archive', async () => {
    const h = bulkHarness([])
    await h.ops.unarchiveAll()
    expect(mockMoveWorkspace).not.toHaveBeenCalled()
    h.dispose()
  })

  it('warns for the workspace that failed and still moves the rest', async () => {
    // Both callees catch and toast, so "the loop continued" is true for every
    // loop shape including none. The TOAST is what says which one failed.
    mockMoveWorkspace.mockImplementation(async (req) => {
      if (req.workspaceId === 'w2')
        throw new Error('nope')
      return {}
    })
    const h = bulkHarness(['w1', 'w2', 'w3'])
    await h.ops.unarchiveAll()
    expect(mockMoveWorkspace).toHaveBeenCalledTimes(3)
    expect(mockShowWarnToast).toHaveBeenCalledTimes(1)
    expect(mockShowWarnToast.mock.calls[0][0]).toBe('Failed to move workspace')
    h.dispose()
  })
})

describe('useWorkspaceOperations emptyArchive', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockDeleteWorkspace.mockResolvedValue({ workerTabs: [] })
  })

  it('asks ONCE, naming the count, and never asks per workspace', async () => {
    // The per-workspace confirm is not merely noisy: `confirmDeleteWsDialog` is
    // a single-slot dialog state, so N concurrent opens drop N-1 resolvers and
    // those awaits never settle.
    const onConfirmEmptyArchive = vi.fn(async () => true)
    const onConfirmDelete = vi.fn(async () => true)
    const h = bulkHarness(['w1', 'w2', 'w3'], { onConfirmEmptyArchive, onConfirmDelete })

    await h.ops.emptyArchive()

    expect(onConfirmEmptyArchive).toHaveBeenCalledTimes(1)
    expect(onConfirmEmptyArchive).toHaveBeenCalledWith(3)
    expect(onConfirmDelete).not.toHaveBeenCalled()
    expect(mockDeleteWorkspace.mock.calls.map(c => c[0].workspaceId)).toEqual(['w1', 'w2', 'w3'])
    h.dispose()
  })

  it('deletes nothing when the confirm is declined', async () => {
    const h = bulkHarness(['w1', 'w2'], { onConfirmEmptyArchive: async () => false })
    await h.ops.emptyArchive()
    expect(mockDeleteWorkspace).not.toHaveBeenCalled()
    h.dispose()
  })

  it('does not ask at all for an empty archive', async () => {
    // "Delete 0 archived workspaces?" is a prompt with no answer worth giving.
    const onConfirmEmptyArchive = vi.fn(async () => true)
    const h = bulkHarness([], { onConfirmEmptyArchive })
    await h.ops.emptyArchive()
    expect(onConfirmEmptyArchive).not.toHaveBeenCalled()
    expect(mockDeleteWorkspace).not.toHaveBeenCalled()
    h.dispose()
  })

  // The confirm is an unbounded await, and any workspace lifecycle event
  // reloads the sections under it. Deleting the pre-confirm snapshot would
  // destroy a workspace somebody had just taken OUT of the archive.
  it('deletes only what is still archived when the archive changed under the confirm', async () => {
    let store: ReturnType<typeof createSectionStore> | undefined
    const h = bulkHarness(['w1', 'w2', 'w3'], {
      onConfirmEmptyArchive: async () => {
        // w2 was unarchived on another device while the prompt was up.
        store!.setItems([item('w1', 's-archived', 'p0'), item('w3', 's-archived', 'p2')])
        return true
      },
    })
    store = h.sectionStore

    await h.ops.emptyArchive()

    expect(mockDeleteWorkspace.mock.calls.map(c => c[0].workspaceId)).toEqual(['w1', 'w3'])
    h.dispose()
  })

  // ...and the mirror: a workspace archived DURING the prompt was never part of
  // the count the user agreed to, so it survives.
  it('deletes no more than the count the confirm named', async () => {
    let store: ReturnType<typeof createSectionStore> | undefined
    const h = bulkHarness(['w1'], {
      onConfirmEmptyArchive: async () => {
        store!.setItems([item('w1', 's-archived', 'p0'), item('late', 's-archived', 'p1')])
        return true
      },
    })
    store = h.sectionStore

    await h.ops.emptyArchive()

    expect(mockDeleteWorkspace.mock.calls.map(c => c[0].workspaceId)).toEqual(['w1'])
    h.dispose()
  })

  // Both refresh calls are full-collection RPCs, so a per-item refresh fetches
  // N-1 states nobody ever sees.
  it('refreshes the lists ONCE for the whole batch, not once per workspace', async () => {
    const onRefreshWorkspaces = vi.fn()
    const h = bulkHarness(['w1', 'w2', 'w3'], {
      onConfirmEmptyArchive: async () => true,
      onRefreshWorkspaces,
    })

    await h.ops.emptyArchive()

    expect(mockDeleteWorkspace).toHaveBeenCalledTimes(3)
    expect(onRefreshWorkspaces).toHaveBeenCalledTimes(1)
    h.dispose()
  })

  // A single delete keeps its own refresh: only the bulk caller opts out.
  it('still refreshes once for a single delete', async () => {
    const onRefreshWorkspaces = vi.fn()
    const h = bulkHarness(['w1'], { onConfirmDelete: async () => true, onRefreshWorkspaces })

    await h.ops.deleteWorkspace('w1')

    expect(onRefreshWorkspaces).toHaveBeenCalledTimes(1)
    h.dispose()
  })

  it('warns for the workspace that failed and still deletes the rest', async () => {
    mockDeleteWorkspace.mockImplementation(async (req) => {
      if (req.workspaceId === 'w2')
        throw new Error('nope')
      return { workerTabs: [] }
    })
    const h = bulkHarness(['w1', 'w2', 'w3'], { onConfirmEmptyArchive: async () => true })
    await h.ops.emptyArchive()
    expect(mockDeleteWorkspace).toHaveBeenCalledTimes(3)
    expect(mockShowWarnToast).toHaveBeenCalledTimes(1)
    expect(mockShowWarnToast.mock.calls[0][0]).toBe('Failed to delete workspace')
    h.dispose()
  })
})

// ----- The view order: filter, then sort -----
//
// `getWorkspacesForGroup` is the ONE place the row list is produced, so every
// consumer -- the rows, the header menu's Collapse all, the repository list --
// sees the same order.

function viewOrder(
  titles: readonly string[],
  opts: { mru?: Record<string, number> } = {},
): { ids: () => string[], dispose: () => void } {
  const inProgress = section('s-progress', SectionType.WORKSPACES_IN_PROGRESS)
  const workspaces = titles.map((title, i) => ({ id: `w${i}`, title, createdAt: `2026-0${i + 1}-01` } as Workspace))
  return createRoot((dispose) => {
    const sectionStore = createSectionStore()
    sectionStore.setSections([inProgress])
    sectionStore.setItems(workspaces.map((w, i) => item(w.id, 's-progress', `p${i}`)))
    const ops = useWorkspaceOperations({
      workspaces: () => workspaces,
      activeWorkspaceId: () => null,
      sectionStore,
      loadSections: async () => {},
      onSelectWorkspace: () => {},
      onRefreshWorkspaces: () => {},
      onDeleteWorkspace: () => {},
      getTabsForWorkspace: (wsId: string) => {
        const mru = opts.mru?.[wsId]
        return mru === undefined ? [] : [{ mru } as Tab]
      },
    })
    return {
      dispose,
      ids: () => ops.getWorkspacesForGroup('s-progress', ops.buildSectionGroups([inProgress])).map(w => w.id),
    }
  })
}

describe('useWorkspaceOperations getWorkspacesForGroup', () => {
  beforeEach(() => {
    localStorageClearForTests()
    setStorageAccount('u-1')
    resetWorkspaceListStateForTests()
  })

  it('returns the lexorank order under the default manual sort', () => {
    const v = viewOrder(['Charlie', 'alpha', 'Bravo'])
    expect(v.ids()).toEqual(['w0', 'w1', 'w2'])
    v.dispose()
  })

  it('applies the global sort', () => {
    const v = viewOrder(['Charlie', 'alpha', 'Bravo'])
    setWorkspaceSortOrder({ key: 'name', direction: 'asc' })
    expect(v.ids()).toEqual(['w1', 'w2', 'w0'])
    v.dispose()
  })

  it('ranks by the most recently activated TAB under the recent sort', () => {
    const v = viewOrder(['a', 'b', 'c'], { mru: { w0: 1, w1: 9, w2: 5 } })
    setWorkspaceSortOrder({ key: 'recent', direction: 'desc' })
    expect(v.ids()).toEqual(['w1', 'w2', 'w0'])
    v.dispose()
  })

  it('applies the section filter', () => {
    const v = viewOrder(['gentle-amber-fox', 'Bold Blue Bear', 'amber tooling'])
    toggleSectionFilter('s-progress')
    setSectionFilterQuery('s-progress', 'amber')
    expect(v.ids()).toEqual(['w0', 'w2'])
    v.dispose()
  })

  it('ignores a filter aimed at a DIFFERENT section', () => {
    const v = viewOrder(['gentle-amber-fox', 'Bold Blue Bear'])
    toggleSectionFilter('s-other')
    setSectionFilterQuery('s-other', 'amber')
    expect(v.ids()).toEqual(['w0', 'w1'])
    v.dispose()
  })

  it('filters BEFORE it sorts, so the sort sees the narrowed list', () => {
    const v = viewOrder(['zeta amber', 'Bold Blue Bear', 'alpha amber'])
    toggleSectionFilter('s-progress')
    setSectionFilterQuery('s-progress', 'amber')
    setWorkspaceSortOrder({ key: 'name', direction: 'asc' })
    expect(v.ids()).toEqual(['w2', 'w0'])
    v.dispose()
  })

  it('answers empty for a section that does not exist', () => {
    const v = viewOrder(['a'])
    expect(v.ids()).toEqual(['w0'])
    v.dispose()
  })
})

// ----- Archived workspaces refuse a rename -----
//
// The hub answers `RenameWorkspace` on an archived workspace with
// FailedPrecondition. The row menu hides its Rename item, but the row also
// renames on DOUBLE-CLICK, which reaches `startRename` directly -- so the guard
// belongs to the operation, not to the item.

describe('useWorkspaceOperations startRename', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('opens the input for a workspace that is not archived', () => {
    const h = bulkHarness([])
    h.sectionStore.setItems([item('ws-live', 's-progress', 'p0')])
    h.ops.startRename(ws('ws-live'))
    expect(h.ops.renamingWorkspaceId()).toBe('ws-live')
    h.dispose()
  })

  it('refuses an archived workspace, so no input opens', () => {
    const h = bulkHarness(['ws-archived'])
    h.ops.startRename(ws('ws-archived'))
    expect(h.ops.renamingWorkspaceId()).toBeNull()
    h.dispose()
  })

  // The row's grip stays draggable while the input is open, so the workspace
  // can be archived between opening the input and committing it.
  it('sends no rename when the workspace was archived while the input was open', async () => {
    const h = bulkHarness([])
    h.sectionStore.setItems([item('ws-1', 's-progress', 'p0')])
    h.ops.startRename(ws('ws-1'))
    h.ops.onRenameInput('a new name')

    h.sectionStore.setItems([item('ws-1', 's-archived', 'p0')])
    await h.ops.commitRename()

    expect(mockRenameWorkspace).not.toHaveBeenCalled()
    expect(h.ops.renamingWorkspaceId()).toBeNull()
    h.dispose()
  })
})

// ----- Same-section reordering follows the view order -----
//
// A row drop resolves through the row's own `ws-<id>` droppable, which every
// row registers whatever the sort is. Suppressing the reorder is the HANDLER's
// job: dropping the sortable primitive instead also removed the droppable a
// CROSS-section drop needs to find a row.

describe('useWorkspaceOperations handleWorkspaceDragEnd', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorageClearForTests()
    setStorageAccount('u-1')
    resetWorkspaceListStateForTests()
  })

  function dragHarness() {
    const inProgress = section('s-progress', SectionType.WORKSPACES_IN_PROGRESS)
    const other = section('s-other', SectionType.WORKSPACES_CUSTOM)
    return createRoot((dispose) => {
      const sectionStore = createSectionStore()
      sectionStore.setSections([inProgress, other])
      sectionStore.setItems([
        item('ws-a', 's-progress', 'a'),
        item('ws-b', 's-progress', 'b'),
        item('ws-c', 's-other', 'a'),
      ])
      const ops = useWorkspaceOperations({
        workspaces: () => ['ws-a', 'ws-b', 'ws-c'].map(ws),
        activeWorkspaceId: () => null,
        sectionStore,
        loadSections: async () => {},
        onSelectWorkspace: () => {},
        onRefreshWorkspaces: () => {},
        onDeleteWorkspace: () => {},
      })
      return { dispose, ops, sectionStore }
    })
  }

  /** One row dropped onto another, as solid-dnd reports it. */
  function drop(h: ReturnType<typeof dragHarness>, from: string, fromSection: string, onto: string, ontoSection: string) {
    h.ops.handleWorkspaceDragEnd({
      draggable: { id: `ws-${from}`, data: { sectionId: fromSection } },
      droppable: { id: `ws-${onto}`, data: { sectionId: ontoSection } },
    } as never)
  }

  it('reorders within a section under the default manual sort', () => {
    const h = dragHarness()
    drop(h, 'a', 's-progress', 'b', 's-progress')
    expect(mockMoveWorkspace).toHaveBeenCalledOnce()
    h.dispose()
  })

  it('does not reorder within a section while a non-manual sort is active', () => {
    const h = dragHarness()
    setWorkspaceSortOrder({ key: 'name', direction: 'asc' })
    drop(h, 'a', 's-progress', 'b', 's-progress')
    expect(mockMoveWorkspace).not.toHaveBeenCalled()
    h.dispose()
  })

  it('does not reorder within a section while that section is filtered', () => {
    const h = dragHarness()
    toggleSectionFilter('s-progress')
    setSectionFilterQuery('s-progress', 'a')
    drop(h, 'a', 's-progress', 'b', 's-progress')
    expect(mockMoveWorkspace).not.toHaveBeenCalled()
    h.dispose()
  })

  // The half the primitive swap silently removed: a CROSS-section drop still
  // resolves the target ROW, so the workspace lands before it rather than at
  // the end of whichever section body happened to be nearest.
  it('still moves across sections onto a row while a non-manual sort is active', () => {
    const h = dragHarness()
    setWorkspaceSortOrder({ key: 'name', direction: 'asc' })
    drop(h, 'a', 's-progress', 'c', 's-other')
    expect(mockMoveWorkspace).toHaveBeenCalledOnce()
    expect(mockMoveWorkspace.mock.calls[0][0].sectionId).toBe('s-other')
    h.dispose()
  })

  it('still moves across sections onto a row while the source section is filtered', () => {
    const h = dragHarness()
    toggleSectionFilter('s-progress')
    setSectionFilterQuery('s-progress', 'a')
    drop(h, 'a', 's-progress', 'c', 's-other')
    expect(mockMoveWorkspace).toHaveBeenCalledOnce()
    expect(mockMoveWorkspace.mock.calls[0][0].sectionId).toBe('s-other')
    h.dispose()
  })
})
