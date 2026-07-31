import type { Section, SectionItem } from '~/generated/leapmux/v1/section_pb'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useWorkspaceOperations } from '~/components/shell/useWorkspaceOperations'
import { SectionType } from '~/generated/leapmux/v1/section_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createSectionStore } from '~/stores/section.store'

interface TabRefLike { tabType: TabType, tabId: string }
interface WorkerTabsLike { workerId: string, tabs: TabRefLike[] }

const mockDeleteWorkspace = vi.fn<(req: { workspaceId: string }) => Promise<{ workerTabs: WorkerTabsLike[] }>>()
const mockCleanupWorkspace = vi.fn<(workerId: string, req: { tabs: TabRefLike[] }) => Promise<unknown>>()
const mockShowWarnToast = vi.fn()

vi.mock('~/api/clients', () => ({
  workspaceClient: {
    deleteWorkspace: (...args: unknown[]) => mockDeleteWorkspace(...args as [{ workspaceId: string }]),
  },
  sectionClient: {},
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
      onNewWorkspace: () => {},
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

describe('useworkspaceoperations deleteworkspace', () => {
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
      onNewWorkspace: () => {},
      onRefreshWorkspaces: () => {},
      onDeleteWorkspace: () => {},
    })
    const built = ops.buildSectionGroups(sections)
    dispose()
    return new Map(built.map(g => [g.section.id, g.workspaces.map(w => w.id)]))
  })
}

describe('useworkspaceoperations buildsectiongroups', () => {
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
