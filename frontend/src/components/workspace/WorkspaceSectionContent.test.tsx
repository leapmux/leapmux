import type { JSX } from 'solid-js'
import { create } from '@bufbuild/protobuf'
import { render, screen, within } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { WorkspaceSchema } from '~/generated/proto/leapmux/v1/workspace_pb'
import { createRepoGitStore } from '~/stores/repoGit.store'
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

function renderContent(renamingWorkspaceId: string | null = null) {
  const workspace = create(WorkspaceSchema, { id: 'ws-1', title: 'Workspace One' })

  render(() => (
    <WorkspaceSectionContent
      workspaces={[workspace]}
      sectionId="section-1"
      sectionName="In progress"
      activeWorkspaceId={null}
      sections={[]}
      onSelect={noop}
      onRename={noop}
      onMoveTo={noop}
      onArchive={noop}
      onUnarchive={noop}
      onDelete={noop}
      isArchived={() => false}
      renamingWorkspaceId={renamingWorkspaceId}
      renameValue="Renamed workspace"
      onRenameInput={noop}
      onRenameCommit={noop}
      onRenameCancel={noop}
      isWorkspaceLoading={() => false}
      getTabsForWorkspace={() => []}
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
    renderContent('ws-1')

    const row = screen.getByTestId('workspace-item-ws-1')
    const chevron = within(row).getByTestId('workspace-chevron-ws-1')
    const input = within(row).getByRole('textbox')
    const grip = within(row).getByTestId('workspace-drag-handle')

    expect(chevron.compareDocumentPosition(input) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0)
    expect(input.compareDocumentPosition(grip) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0)
  })
})
