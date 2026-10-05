import type { JSX } from 'solid-js'
import type { Tab } from '~/stores/tab.types'
import { create } from '@bufbuild/protobuf'
import { render, screen, within } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TabType, WorkspaceSchema } from '~/generated/proto/leapmux/v1/workspace_pb'
import { sessionStorageClearForTests, setStorageAccountForTests } from '~/lib/browserStorage'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { resetExpandedWorkspacesForTests, setWorkspacesExpanded } from './expandedWorkspaces'
import { WorkspaceSectionContent } from './WorkspaceSectionContent'

vi.mock('@thisbeyond/solid-dnd', () => ({
  createDroppable: () => Object.assign((_el: HTMLElement) => {}, {
    isActiveDroppable: false,
    ref: undefined,
  }),
  createSortable: () => ({
    ref: () => {},
    dragActivators: {},
    isActiveDraggable: false,
    transform: { x: 0, y: 0 },
  }),
  maybeTransformStyle: () => ({}),
  SortableProvider: (props: { children: JSX.Element }) => <>{props.children}</>,
}))

vi.mock('./WorkspaceContextMenu', () => ({
  WorkspaceContextMenu: () => null,
}))

vi.mock('./WorkspaceTabTree', () => ({
  RolledUpNotificationDot: () => null,
  WorkspaceTabTree: () => null,
}))

function noop() {}

interface RenderOptions {
  renamingWorkspaceId?: string | null
  workspaceIds?: readonly string[]
  activeWorkspaceId?: () => string | null
  getTabsForWorkspace?: (workspaceId: string) => Tab[]
}

function renderContent(options: RenderOptions = {}) {
  const workspaces = (options.workspaceIds ?? ['ws-1']).map((id, index) =>
    create(WorkspaceSchema, { id, title: index === 0 ? 'Workspace One' : `Workspace ${id}` }))
  const activeWorkspaceId = options.activeWorkspaceId ?? (() => null)
  const getTabsForWorkspace = options.getTabsForWorkspace ?? (() => [])

  render(() => (
    <WorkspaceSectionContent
      workspaces={workspaces}
      sectionId="section-1"
      sectionName="In progress"
      activeWorkspaceId={activeWorkspaceId()}
      sections={[]}
      onSelect={noop}
      onRename={noop}
      onMoveTo={noop}
      onArchive={noop}
      onUnarchive={noop}
      onDelete={noop}
      isArchived={() => false}
      renamingWorkspaceId={options.renamingWorkspaceId ?? null}
      renameValue="Renamed workspace"
      onRenameInput={noop}
      onRenameCommit={noop}
      onRenameCancel={noop}
      isWorkspaceLoading={() => false}
      getTabsForWorkspace={getTabsForWorkspace}
      getActiveTabKeyForWorkspace={() => null}
      getTileOrderForWorkspace={() => []}
      onTabClick={noop}
      isLocalWorkerFn={() => false}
      repoGitStore={createRepoGitStore()}
    />
  ))
}

describe('WorkspaceSectionContent', () => {
  it('keeps the chevron at the indent edge and puts the drag grip after the title', () => {
    renderContent()

    const row = screen.getByTestId('workspace-item-ws-1')
    const chevron = within(row).getByTestId('workspace-chevron-ws-1')
    const title = within(row).getByText('Workspace One')
    const grip = within(row).getByTestId('workspace-drag-handle')

    expect(chevron.compareDocumentPosition(title) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0)
    expect(title.compareDocumentPosition(grip) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0)
  })

  it('keeps the drag grip after the rename input', () => {
    renderContent({ renamingWorkspaceId: 'ws-1' })

    const row = screen.getByTestId('workspace-item-ws-1')
    const chevron = within(row).getByTestId('workspace-chevron-ws-1')
    const input = within(row).getByRole('textbox')
    const grip = within(row).getByTestId('workspace-drag-handle')

    expect(chevron.compareDocumentPosition(input) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0)
    expect(input.compareDocumentPosition(grip) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0)
  })
})

function agentTab(id: string, workspaceId: string, title = id): Tab {
  return { type: TabType.AGENT, id, workspaceId, title }
}

/**
 * The tabs of every workspace in ONE signal, as the tab view holds them.
 *
 * `tabView.forWorkspace` reads one memo for all workspaces, so a change to any
 * tab notifies every reader of any workspace's list. A per-workspace signal
 * here would hide exactly the notification that matters.
 */
function createTabStore(initial: Record<string, Tab[]>) {
  const [tabs, setTabs] = createSignal<Record<string, Tab[]>>(initial)
  return {
    getTabsForWorkspace: (workspaceId: string) => tabs()[workspaceId] ?? [],
    replace: (workspaceId: string, next: Tab[]) => setTabs(prev => ({ ...prev, [workspaceId]: next })),
  }
}

function expandedState(workspaceId: string): string | null {
  return screen.getByTestId(`workspace-item-${workspaceId}`).getAttribute('data-expanded')
}

describe('WorkspaceSectionContent active workspace expansion', () => {
  beforeEach(() => {
    sessionStorageClearForTests()
    setStorageAccountForTests('u-1')
    resetExpandedWorkspacesForTests()
  })

  afterEach(() => {
    resetExpandedWorkspacesForTests()
  })

  it('expands the active workspace once it has a tab', () => {
    const store = createTabStore({ 'ws-1': [agentTab('a-1', 'ws-1')] })
    renderContent({ activeWorkspaceId: () => 'ws-1', getTabsForWorkspace: store.getTabsForWorkspace })

    expect(expandedState('ws-1')).toBe('true')
  })

  it('leaves an active workspace with no tab collapsed', () => {
    const store = createTabStore({})
    renderContent({ activeWorkspaceId: () => 'ws-1', getTabsForWorkspace: store.getTabsForWorkspace })

    expect(expandedState('ws-1')).toBe('false')
  })

  it('keeps a collapsed active workspace collapsed when one of its tabs changes', () => {
    const store = createTabStore({ 'ws-1': [agentTab('a-1', 'ws-1')] })
    renderContent({ activeWorkspaceId: () => 'ws-1', getTabsForWorkspace: store.getTabsForWorkspace })
    expect(expandedState('ws-1')).toBe('true')

    // "Collapse all", then the tab's agent reports its startup: a new tab
    // object in a new list of the same length.
    setWorkspacesExpanded(['ws-1'], false)
    expect(expandedState('ws-1')).toBe('false')
    store.replace('ws-1', [agentTab('a-1', 'ws-1', 'Agent started')])

    expect(expandedState('ws-1')).toBe('false')
  })

  it('keeps a collapsed active workspace collapsed when it gains a second tab', () => {
    const store = createTabStore({ 'ws-1': [agentTab('a-1', 'ws-1')] })
    renderContent({ activeWorkspaceId: () => 'ws-1', getTabsForWorkspace: store.getTabsForWorkspace })

    setWorkspacesExpanded(['ws-1'], false)
    store.replace('ws-1', [agentTab('a-1', 'ws-1'), agentTab('a-2', 'ws-1')])

    expect(expandedState('ws-1')).toBe('false')
  })

  it('keeps a collapsed active workspace collapsed when a tab of another workspace changes', () => {
    const store = createTabStore({
      'ws-1': [agentTab('a-1', 'ws-1')],
      'ws-2': [agentTab('b-1', 'ws-2')],
    })
    renderContent({ workspaceIds: ['ws-1', 'ws-2'], activeWorkspaceId: () => 'ws-1', getTabsForWorkspace: store.getTabsForWorkspace })

    setWorkspacesExpanded(['ws-1'], false)
    store.replace('ws-2', [agentTab('b-1', 'ws-2', 'Renamed')])

    expect(expandedState('ws-1')).toBe('false')
  })

  it('expands the active workspace when it gains its first tab', () => {
    const store = createTabStore({})
    renderContent({ activeWorkspaceId: () => 'ws-1', getTabsForWorkspace: store.getTabsForWorkspace })
    expect(expandedState('ws-1')).toBe('false')

    store.replace('ws-1', [agentTab('a-1', 'ws-1')])

    expect(expandedState('ws-1')).toBe('true')
  })

  it('expands the active workspace again after it lost every tab and gained a new one', () => {
    const store = createTabStore({ 'ws-1': [agentTab('a-1', 'ws-1')] })
    renderContent({ activeWorkspaceId: () => 'ws-1', getTabsForWorkspace: store.getTabsForWorkspace })

    setWorkspacesExpanded(['ws-1'], false)
    store.replace('ws-1', [])
    store.replace('ws-1', [agentTab('a-2', 'ws-1')])

    expect(expandedState('ws-1')).toBe('true')
  })

  it('expands each workspace that becomes active, and leaves the previous one as it was', () => {
    const store = createTabStore({
      'ws-1': [agentTab('a-1', 'ws-1')],
      'ws-2': [agentTab('b-1', 'ws-2')],
    })
    const [active, setActive] = createSignal<string | null>('ws-1')
    renderContent({ workspaceIds: ['ws-1', 'ws-2'], activeWorkspaceId: active, getTabsForWorkspace: store.getTabsForWorkspace })
    setWorkspacesExpanded(['ws-1'], false)

    setActive('ws-2')
    expect(expandedState('ws-2')).toBe('true')
    expect(expandedState('ws-1')).toBe('false')

    // Returning to a workspace is a change of the active workspace, so it
    // expands again.
    setActive('ws-1')
    expect(expandedState('ws-1')).toBe('true')
  })

  it('expands nothing when no workspace is active', () => {
    const store = createTabStore({ 'ws-1': [agentTab('a-1', 'ws-1')] })
    renderContent({ activeWorkspaceId: () => null, getTabsForWorkspace: store.getTabsForWorkspace })

    expect(expandedState('ws-1')).toBe('false')
  })
})
