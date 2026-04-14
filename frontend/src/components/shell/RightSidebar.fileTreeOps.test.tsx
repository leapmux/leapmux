import { cleanup, render } from '@solidjs/testing-library'
import { createEffect, createSignal, For, onMount } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SectionType, Sidebar } from '~/generated/leapmux/v1/section_pb'
import { refreshFileTree, toggleHiddenFiles } from '~/lib/fileTreeOps'
import { createSectionStore } from '~/stores/section.store'
import { RightSidebar } from './RightSidebar'

const mockBuildSectionDef = vi.fn()

vi.mock('./buildSectionDef', () => ({
  buildSectionDef: (...args: any[]) => mockBuildSectionDef(...args),
}))

vi.mock('./CollapsibleSidebar', () => ({
  CollapsibleSidebar: (props: any) => (
    <div data-testid="mock-sidebar">
      <For each={props.sections}>
        {(section: any) => <div data-testid={`section-${section.id}`}>{section.content?.()}</div>}
      </For>
    </div>
  ),
}))

vi.mock('./useWorkspaceOperations', () => ({
  useWorkspaceOperations: () => ({
    buildSectionGroups: (sections: any[]) => sections.map(section => ({ section })),
    sharingWorkspaceId: () => null,
  }),
}))

vi.mock('~/components/workspace/WorkspaceSharingDialog', () => ({
  WorkspaceSharingDialog: () => null,
}))

afterEach(() => {
  cleanup()
  mockBuildSectionDef.mockReset()
})

function makeProps() {
  const sectionStore = createSectionStore()
  sectionStore.setSections([{
    id: 'files',
    name: 'Files',
    position: 'a',
    sidebar: Sidebar.RIGHT,
    sectionType: SectionType.FILES,
  } as any])

  return {
    sectionStore,
    workspaces: [],
    activeWorkspaceId: null,
    loadSections: vi.fn(),
    onSelectWorkspace: vi.fn(),
    onNewWorkspace: vi.fn(),
    onRefreshWorkspaces: vi.fn(),
    onDeleteWorkspace: vi.fn(),
    onConfirmDelete: vi.fn(),
    onConfirmArchive: vi.fn(),
    onPostArchiveWorkspace: vi.fn(),
    isCollapsed: false,
    onExpand: vi.fn(),
    workerId: 'worker-1',
    workingDir: '/repo',
    homeDir: '/home/user',
    fileTreePath: '/repo',
    onFileSelect: vi.fn(),
    onFileOpen: vi.fn(),
    onFileMention: vi.fn(),
    onOpenTerminal: vi.fn(),
    gitStatusStore: { refresh: vi.fn() },
    activeFilePath: '/repo/file.ts',
    hasActiveFileTab: true,
    showTodos: false,
    activeTodos: [],
    turnEndTrigger: 0,
    tabStore: undefined,
    registry: undefined,
    onTabClick: vi.fn(),
    tabItemOps: undefined,
    onExpandWorkspace: vi.fn(),
    workers: [],
    workerInfoFn: vi.fn(),
    channelStatusFn: vi.fn(),
    currentUserId: 'user-1',
    onAddTunnel: vi.fn(),
    onDeregisterWorker: vi.fn(),
  }
}

describe('rightSidebar file tree shortcut registration', () => {
  it('refreshes git status and the files handle, and toggles hidden files', () => {
    const props = makeProps()
    const handle = {
      refresh: vi.fn(),
      toggleShowHiddenFiles: vi.fn(),
    }

    mockBuildSectionDef.mockImplementation((_section: any, ctx: any) => ({
      id: 'files',
      content: () => {
        ctx.setFilesSectionHandle(handle)
        return <div />
      },
    }))

    render(() => <RightSidebar {...(props as any)} />)

    refreshFileTree()
    toggleHiddenFiles()

    expect(props.gitStatusStore.refresh).toHaveBeenCalledWith('worker-1', '/repo')
    expect(handle.refresh).toHaveBeenCalledOnce()
    expect(handle.toggleShowHiddenFiles).toHaveBeenCalledOnce()
  })

  it('unregisters sidebar ops when the handle is cleared', async () => {
    const props = makeProps()
    const initialHandle = {
      refresh: vi.fn(),
      toggleShowHiddenFiles: vi.fn(),
    }
    let setHandle!: (value: any) => void

    mockBuildSectionDef.mockImplementation((_section: any, ctx: any) => ({
      id: 'files',
      content: () => <HandleSetter ctx={ctx} initialHandle={initialHandle} setHandleRef={value => setHandle = value} />,
    }))

    render(() => <RightSidebar {...(props as any)} />)
    setHandle(undefined)
    await Promise.resolve()

    refreshFileTree()

    expect(props.gitStatusStore.refresh).not.toHaveBeenCalled()
  })

  it('unregisters sidebar ops on unmount', () => {
    const props = makeProps()
    const handle = {
      refresh: vi.fn(),
      toggleShowHiddenFiles: vi.fn(),
    }

    mockBuildSectionDef.mockImplementation((_section: any, ctx: any) => ({
      id: 'files',
      content: () => {
        ctx.setFilesSectionHandle(handle)
        return <div />
      },
    }))

    const view = render(() => <RightSidebar {...(props as any)} />)
    view.unmount()

    refreshFileTree()

    expect(props.gitStatusStore.refresh).not.toHaveBeenCalled()
    expect(handle.refresh).not.toHaveBeenCalled()
  })
})

function HandleSetter(props: {
  ctx: { setFilesSectionHandle: (handle: any) => void }
  initialHandle: any
  setHandleRef: (setter: (value: any) => void) => void
}) {
  const [handle, setHandle] = createSignal(props.initialHandle)
  onMount(() => {
    props.setHandleRef(setHandle)
  })
  createEffect(() => {
    props.ctx.setFilesSectionHandle(handle())
  })
  return <div />
}
