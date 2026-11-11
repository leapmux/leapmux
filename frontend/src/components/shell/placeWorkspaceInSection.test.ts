import { create } from '@bufbuild/protobuf'
import { batch, createEffect, createRoot, createSignal } from 'solid-js'
/// <reference types="vitest/globals" />
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { sectionClient, workspaceClient } from '~/api/clients'
import { showWarnToast } from '~/components/common/Toast'
import { WorkspaceArchiveState } from '~/generated/proto/leapmux/v1/common_pb'
import { ListSectionsResponseSchema, SectionType, Sidebar } from '~/generated/proto/leapmux/v1/section_pb'
import { ListWorkspacesResponseSchema } from '~/generated/proto/leapmux/v1/workspace_pb'
import { createSectionStore } from '~/stores/section.store'
import { createWorkspaceStore } from '~/stores/workspace.store'
import { deferred, settle } from '~/test-support/async'
import { placeWorkspaceInSection } from './placeWorkspaceInSection'
import { resolveActiveWorkspace } from './resolveActiveWorkspace'
import { useWorkspaceLoader } from './useWorkspaceLoader'
import { createWorkspaceSwitcher } from './workspaceSwitcher'

vi.mock('~/api/clients', () => ({
  sectionClient: {
    listSections: vi.fn(),
  },
  workspaceClient: {
    moveWorkspace: vi.fn(),
    listWorkspaces: vi.fn(),
  },
}))

vi.mock('~/components/common/Toast', () => ({
  showInfoToast: vi.fn(),
  showWarnToast: vi.fn(),
  showErrorToast: vi.fn(),
}))

function makeDeps(items: { position: string }[] = []) {
  return {
    isCurrent: () => true,
    sectionStore: {
      getItemsForSection: vi.fn(() => items),
    },
    loadWorkspaces: vi.fn().mockResolvedValue(undefined),
    loadSections: vi.fn().mockResolvedValue(undefined),
  }
}

/** Lets a test resolve or reject the in-flight move RPC by hand. */
function deferredMove() {
  let settle!: (err?: Error) => void
  vi.mocked(workspaceClient.moveWorkspace).mockReturnValue(
    new Promise((resolve, reject) => {
      settle = err => (err
        ? reject(err)
        : resolve({} as never))
    }) as never,
  )
  return () => settle
}

describe('placeWorkspaceInSection', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(workspaceClient.moveWorkspace).mockResolvedValue({} as never)
  })

  it('refreshes without an RPC when there is no target section', async () => {
    // The shortcut path: no section was preselected, so the workspace stays
    // where CreateWorkspace put it and there is nothing to move.
    const deps = makeDeps()
    placeWorkspaceInSection(deps, 'ws-1', null)

    expect(workspaceClient.moveWorkspace).not.toHaveBeenCalled()
    expect(deps.loadWorkspaces).toHaveBeenCalledTimes(1)
  })

  it('appends past the section\'s last item so the rank cannot collide', async () => {
    const deps = makeDeps([
      { position: 'n' },
      { position: 'u' },
    ])
    placeWorkspaceInSection(deps, 'ws-1', 'sec-1')

    await vi.waitFor(() => expect(deps.loadWorkspaces).toHaveBeenCalled())
    const arg = vi.mocked(workspaceClient.moveWorkspace).mock.calls[0]?.[0]
    expect(arg).toMatchObject({ workspaceId: 'ws-1', sectionId: 'sec-1' })
    expect((arg?.position ?? '') > 'u', 'the new rank must sort after the last item').toBe(true)
  })

  // The Hub requires the archive state the caller holds, and refuses a move
  // that would cross the archive boundary unseen. CreateWorkspace just made
  // this workspace, and a new workspace is active.
  it('states that the new workspace is active', async () => {
    const deps = makeDeps([])
    placeWorkspaceInSection(deps, 'ws-1', 'sec-1')

    await vi.waitFor(() => expect(deps.loadWorkspaces).toHaveBeenCalled())
    expect(workspaceClient.moveWorkspace).toHaveBeenCalledExactlyOnceWith({
      workspaceId: 'ws-1',
      sectionId: 'sec-1',
      position: expect.any(String),
      expectedArchiveState: WorkspaceArchiveState.ACTIVE,
    })
  })

  it('gives an empty section a valid rank rather than an empty string', async () => {
    const deps = makeDeps([])
    placeWorkspaceInSection(deps, 'ws-1', 'sec-1')

    await vi.waitFor(() => expect(deps.loadWorkspaces).toHaveBeenCalled())
    expect(vi.mocked(workspaceClient.moveWorkspace).mock.calls[0]?.[0].position).not.toBe('')
  })

  it('warns when the move fails, and still refreshes', async () => {
    // Regression: this call used to swallow its rejection with
    // `.catch(() => {})`, so a workspace could land in the default section
    // instead of the one the user clicked "+" in, with nothing on screen to
    // say so. Its two sibling copies in useWorkspaceOperations both warn.
    const settle = deferredMove()
    const deps = makeDeps([])
    placeWorkspaceInSection(deps, 'ws-1', 'sec-1')
    settle()(new Error('move failed'))

    await vi.waitFor(() => expect(showWarnToast).toHaveBeenCalledTimes(1))
    expect(showWarnToast).toHaveBeenCalledWith(
      'Failed to move the workspace into its section',
      expect.any(Error),
    )
    expect(deps.loadWorkspaces).toHaveBeenCalledTimes(2)
    expect(deps.loadSections).toHaveBeenCalledTimes(2)
  })

  it('does not warn when the move succeeds', async () => {
    const deps = makeDeps([])
    placeWorkspaceInSection(deps, 'ws-1', 'sec-1')

    await vi.waitFor(() => expect(deps.loadWorkspaces).toHaveBeenCalled())
    expect(showWarnToast).not.toHaveBeenCalled()
  })
})

describe('placeWorkspaceInSection lifetime', () => {
  it.each([false, true])('ignores a late move completion when failure is %s', async (failure) => {
    const release = deferredMove()
    let current = true
    const deps = {
      ...makeDeps(),
      isCurrent: () => current,
    }
    placeWorkspaceInSection(deps, 'ws-old', 'section-old')
    expect(deps.loadWorkspaces).toHaveBeenCalledOnce()
    expect(deps.loadSections).toHaveBeenCalledOnce()
    deps.loadWorkspaces.mockClear()
    deps.loadSections.mockClear()
    current = false
    release()(failure
      ? new Error('late failure')
      : undefined)
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
    expect(deps.loadWorkspaces).not.toHaveBeenCalled()
    expect(deps.loadSections).not.toHaveBeenCalled()
    expect(showWarnToast).not.toHaveBeenCalled()
  })

  it('ignores an action whose captured lifetime already ended', () => {
    const deps = {
      ...makeDeps(),
      isCurrent: () => false,
    }
    placeWorkspaceInSection(deps, 'ws-old', null)
    expect(deps.loadWorkspaces).not.toHaveBeenCalled()
    expect(deps.loadSections).not.toHaveBeenCalled()
  })
})

function workspaceList(ids: string[]) {
  return create(ListWorkspacesResponseSchema, {
    workspaces: ids.map(id => ({ id, title: id })),
  })
}
function sectionList(workspaceId?: string, sectionId = 'progress') {
  return create(ListSectionsResponseSchema, {
    sections: [
      { id: 'progress', sectionType: SectionType.WORKSPACES_IN_PROGRESS, sidebar: Sidebar.LEFT },
      { id: 'target', sectionType: SectionType.WORKSPACES_CUSTOM, sidebar: Sidebar.LEFT },
    ],
    items: workspaceId
      ? [
          { workspaceId, sectionId, position: 'n' },
        ]
      : [],
  })
}
function mountBookkeeping() {
  return createRoot((dispose) => {
    const workspaceStore = createWorkspaceStore()
    const sectionStore = createSectionStore()
    const loader = useWorkspaceLoader({
      workspaceStore,
      sectionStore,
      userId: () => 'usertest',
    })
    const [active, setActive] = createSignal<string | null>('old')
    const [dialogOpen, setDialogOpen] = createSignal(true)
    const switchWorkspace = createWorkspaceSwitcher({ setActiveWorkspaceId: setActive })
    createEffect(() => {
      if (dialogOpen())
        return
      const decision = resolveActiveWorkspace({
        activeWorkspaceId: active(),
        userId: 'usertest',
        workspaceState: workspaceStore.state,
        savedWorkspaceId: undefined,
      })
      if (decision.kind === 'adopt')
        switchWorkspace(decision.workspaceId)
      if (decision.kind === 'clear')
        switchWorkspace(null)
    })
    return {
      dispose,
      workspaceStore,
      sectionStore,
      loader,
      active,
      setDialogOpen,
      switchWorkspace,
    }
  })
}

describe('placeWorkspaceInSection creation bookkeeping', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(workspaceClient.moveWorkspace).mockReset().mockResolvedValue({} as never)
    vi.mocked(workspaceClient.listWorkspaces).mockReset().mockResolvedValue(workspaceList(['old']))
    vi.mocked(sectionClient.listSections).mockReset().mockResolvedValue(sectionList())
  })

  it('starts loading before activation can discard a newly published workspace', async () => {
    const h = mountBookkeeping()
    const moved = deferred<unknown>()
    const incoming = deferred<ReturnType<typeof workspaceList>>()
    const incomingSections = deferred<ReturnType<typeof sectionList>>()
    try {
      await settle()
      expect(h.workspaceStore.state.loaded).toBe(true)
      vi.mocked(workspaceClient.moveWorkspace).mockReturnValueOnce(moved.promise as never)
      vi.mocked(workspaceClient.listWorkspaces).mockReturnValueOnce(incoming.promise)
      vi.mocked(sectionClient.listSections).mockReturnValueOnce(incomingSections.promise)
      const deps = {
        sectionStore: h.sectionStore,
        loadWorkspaces: h.loader.loadWorkspaces,
        loadSections: h.loader.loadSections,
        isCurrent: () => true,
      }
      placeWorkspaceInSection(deps, 'new', 'target')
      batch(() => {
        h.switchWorkspace('new')
        h.setDialogOpen(false)
      })
      await settle()
      expect(h.workspaceStore.state.loading).toBe(true)
      expect(h.active()).toBe('new')
      incoming.resolve(workspaceList(['old', 'new']))
      incomingSections.resolve(sectionList('new'))
      await settle()
      expect(h.active()).toBe('new')
    }
    finally {
      h.dispose()
      incoming.resolve(workspaceList(['old', 'new']))
      incomingSections.resolve(sectionList('new'))
      moved.resolve({})
    }
  })

  it('refreshes section assignments after the move and rejects older list responses', async () => {
    const h = mountBookkeeping()
    const moved = deferred<unknown>()
    const beforeWorkspaces = deferred<ReturnType<typeof workspaceList>>()
    const beforeSections = deferred<ReturnType<typeof sectionList>>()
    try {
      await settle()
      vi.mocked(workspaceClient.moveWorkspace).mockReturnValueOnce(moved.promise as never)
      vi.mocked(workspaceClient.listWorkspaces).mockReturnValueOnce(beforeWorkspaces.promise).mockResolvedValueOnce(workspaceList(['old', 'new']))
      vi.mocked(sectionClient.listSections).mockReturnValueOnce(beforeSections.promise).mockResolvedValueOnce(sectionList('new', 'target'))
      const deps = {
        sectionStore: h.sectionStore,
        loadWorkspaces: h.loader.loadWorkspaces,
        loadSections: h.loader.loadSections,
        isCurrent: () => true,
      }
      placeWorkspaceInSection(deps, 'new', 'target')
      moved.resolve({})
      await settle()
      expect(h.sectionStore.getSectionForWorkspace('new')).toBe('target')
      expect(h.workspaceStore.state.workspaces.map(workspace => workspace.id)).toEqual(['old', 'new'])
      beforeWorkspaces.resolve(workspaceList(['old']))
      beforeSections.resolve(sectionList('new', 'progress'))
      await settle()
      expect(h.sectionStore.getSectionForWorkspace('new')).toBe('target')
      expect(h.workspaceStore.state.workspaces.map(workspace => workspace.id)).toEqual(['old', 'new'])
    }
    finally {
      h.dispose()
      beforeWorkspaces.resolve(workspaceList(['old']))
      beforeSections.resolve(sectionList('new', 'progress'))
      moved.resolve({})
    }
  })

  it('refreshes workspace and section lists once when no section move is needed', () => {
    const deps = makeDeps()
    placeWorkspaceInSection(deps, 'new', null)
    expect(deps.loadWorkspaces).toHaveBeenCalledOnce()
    expect(deps.loadSections).toHaveBeenCalledOnce()
    expect(workspaceClient.moveWorkspace).not.toHaveBeenCalled()
  })
})
