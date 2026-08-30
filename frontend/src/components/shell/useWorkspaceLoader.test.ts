/// <reference types="vitest/globals" />
import type { Workspace } from '~/generated/proto/leapmux/v1/workspace_pb'
import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { WorkspaceSchema } from '~/generated/proto/leapmux/v1/workspace_pb'
import { ChannelError } from '~/lib/channelError'
import { createSectionStore } from '~/stores/section.store'
import { createWorkspaceStore } from '~/stores/workspace.store'
import { useWorkspaceLoader } from './useWorkspaceLoader'

const mockListWorkspaces = vi.fn()
const mockListSections = vi.fn()
vi.mock('~/api/clients', () => ({
  workspaceClient: { listWorkspaces: (...args: unknown[]) => mockListWorkspaces(...args) },
  sectionClient: {
    listSections: (...args: unknown[]) => mockListSections(...args),
    moveSection: vi.fn(),
  },
}))

const mockShowWarnToast = vi.fn()
vi.mock('~/components/common/Toast', async () => {
  const { isDisconnectError } = await import('~/api/workerErrors')
  return {
    showWarnToast: (...args: unknown[]) => mockShowWarnToast(...args),
    showWarnToastUnlessDisconnected: (message: string, err: unknown) => {
      if (!isDisconnectError(err))
        mockShowWarnToast(message, err)
    },
  }
})

interface Deferred<T> {
  promise: Promise<T>
  resolve: (value: T) => void
  reject: (err: unknown) => void
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  let reject!: (err: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function ws(id: string): Workspace {
  return create(WorkspaceSchema, { id, title: id })
}

function mount() {
  return createRoot((dispose) => {
    const workspaceStore = createWorkspaceStore()
    const sectionStore = createSectionStore()
    const loader = useWorkspaceLoader({ workspaceStore, sectionStore })
    return { workspaceStore, sectionStore, loader, dispose }
  })
}

// Let every already-scheduled microtask (the hook's own `await` continuations)
// run before asserting.
async function settle() {
  await Promise.resolve()
  await Promise.resolve()
  await Promise.resolve()
}

describe('useWorkspaceLoader', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockListWorkspaces.mockResolvedValue({ workspaces: [] })
    mockListSections.mockResolvedValue({ sections: [], items: [] })
  })

  // A failed load used to write `workspaceStore.state.error` and stop -- state
  // nothing reads. The user saw an empty sidebar, or worse, a "this workspace
  // doesn't exist or you don't have access" page for a workspace they own.
  it('surfaces a failed workspace load as a warning toast', async () => {
    mockListWorkspaces.mockRejectedValue(new Error('hub unavailable'))

    const { workspaceStore } = mount()
    await settle()

    expect(mockShowWarnToast).toHaveBeenCalledWith('Failed to load workspaces', expect.any(Error))
    expect(workspaceStore.state.error).toBe('hub unavailable')
  })

  it('surfaces a failed section load as a warning toast', async () => {
    mockListSections.mockRejectedValue(new Error('hub unavailable'))

    const { sectionStore } = mount()
    await settle()

    expect(mockShowWarnToast).toHaveBeenCalledWith('Failed to load sections', expect.any(Error))
    expect(sectionStore.state.error).toBe('hub unavailable')
  })

  it('does not toast a workspace load that failed because the link dropped', async () => {
    mockListWorkspaces.mockRejectedValue(new ChannelError('transport', 'channel disconnected'))

    const { workspaceStore } = mount()
    await settle()

    expect(mockShowWarnToast).not.toHaveBeenCalled()
    expect(workspaceStore.state.error).toBe('channel disconnected')
  })

  it('clears a recorded workspace error once a load succeeds', async () => {
    mockListWorkspaces.mockRejectedValueOnce(new Error('hub unavailable'))
    const { workspaceStore, loader } = mount()
    await settle()
    expect(workspaceStore.state.error).toBe('hub unavailable')

    mockListWorkspaces.mockResolvedValueOnce({ workspaces: [ws('w1')] })
    await loader.loadWorkspaces()

    expect(workspaceStore.state.error).toBeNull()
    expect(workspaceStore.state.workspaces.map(w => w.id)).toEqual(['w1'])
  })

  // loadWorkspaces is fired from at least four places (mount, the workspace
  // lifecycle stream, the create/delete dialogs, useWorkspaceOperations), so
  // two are routinely in flight at once. Whichever answers LAST used to win,
  // regardless of which was asked last -- resurrecting a just-deleted
  // workspace or dropping a just-created one until the next lifecycle event.
  it('keeps the newer list when an older load answers last', async () => {
    const first = deferred<{ workspaces: Workspace[] }>()
    const second = deferred<{ workspaces: Workspace[] }>()
    mockListWorkspaces.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)

    const { workspaceStore, loader } = mount()
    const secondLoad = loader.loadWorkspaces()

    second.resolve({ workspaces: [ws('new')] })
    await secondLoad
    first.resolve({ workspaces: [ws('old')] })
    await settle()

    expect(workspaceStore.state.workspaces.map(w => w.id)).toEqual(['new'])
  })

  it('keeps loading true while a newer load is still in flight', async () => {
    const first = deferred<{ workspaces: Workspace[] }>()
    const second = deferred<{ workspaces: Workspace[] }>()
    mockListWorkspaces.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)

    const { workspaceStore, loader } = mount()
    const secondLoad = loader.loadWorkspaces()

    first.resolve({ workspaces: [ws('old')] })
    await settle()
    expect(workspaceStore.state.loading).toBe(true)

    second.resolve({ workspaces: [ws('new')] })
    await secondLoad
    expect(workspaceStore.state.loading).toBe(false)
    expect(workspaceStore.state.workspaces.map(w => w.id)).toEqual(['new'])
  })

  it('keeps the newer section list when an older load answers last', async () => {
    const first = deferred<{ sections: never[], items: never[] }>()
    const second = deferred<{ sections: never[], items: never[] }>()
    mockListSections.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)

    const { sectionStore, loader } = mount()
    const secondLoad = loader.loadSections()

    second.resolve({ sections: [], items: [{ sectionId: 's1', workspaceId: 'new' }] as never[] })
    await secondLoad
    first.resolve({ sections: [], items: [{ sectionId: 's1', workspaceId: 'old' }] as never[] })
    await settle()

    expect(sectionStore.state.items.map(i => i.workspaceId)).toEqual(['new'])
  })
})
