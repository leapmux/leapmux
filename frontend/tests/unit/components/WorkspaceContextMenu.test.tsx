import { create } from '@bufbuild/protobuf'
import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { WorkspaceContextMenu } from '~/components/workspace/WorkspaceContextMenu'
import { SectionSchema, SectionType, Sidebar } from '~/generated/leapmux/v1/section_pb'

// Mock DropdownMenu to render children directly (jsdom lacks popover API).
vi.mock('~/components/common/DropdownMenu', () => ({
  DropdownMenu(props: any) {
    // Render trigger (if function, call with dummy props) and children
    const trigger = () => typeof props.trigger === 'function'
      ? props.trigger({
          'aria-expanded': true,
          'ref': () => {},
          'onPointerDown': () => {},
          'onClick': () => {},
        })
      : props.trigger
    return (
      <>
        {trigger()}
        {props.children}
      </>
    )
  },
}))

function makeSection(
  id: string,
  name: string,
  sectionType: SectionType,
) {
  return create(SectionSchema, {
    id,
    name,
    position: '',
    sectionType,
    sidebar: Sidebar.LEFT,
  })
}

function noop() {}

const defaultProps = {
  isOwner: true,
  isArchived: false,
  sections: [] as ReturnType<typeof makeSection>[],
  currentSectionId: 'sec-ip',
  onRename: noop,
  onMoveTo: noop as (sectionId: string) => void,
  onShare: noop,
  onArchive: noop,
  onUnarchive: noop,
  onDelete: noop,
}

describe('workspaceContextMenu', () => {
  it('hides Move-to when isArchived is true', () => {
    const sections = [
      makeSection('sec-ip', 'In Progress', SectionType.WORKSPACES_IN_PROGRESS),
      makeSection('sec-custom', 'My Section', SectionType.WORKSPACES_CUSTOM),
    ]
    render(() => (
      <WorkspaceContextMenu
        {...defaultProps}
        isArchived={true}
        sections={sections}
      />
    ))
    expect(screen.queryByText('Move to')).toBeNull()
    // Unarchive should be visible instead of Archive
    expect(screen.getByText('Unarchive')).toBeTruthy()
    expect(screen.queryByText('Archive')).toBeNull()
  })

  it('hides Move-to when no other target sections exist', () => {
    // Only one workspace section — the current one
    const sections = [
      makeSection('sec-ip', 'In Progress', SectionType.WORKSPACES_IN_PROGRESS),
      makeSection('sec-archived', 'Archived', SectionType.WORKSPACES_ARCHIVED),
      makeSection('sec-files', 'Files', SectionType.FILES),
    ]
    render(() => (
      <WorkspaceContextMenu
        {...defaultProps}
        sections={sections}
        currentSectionId="sec-ip"
      />
    ))
    // Move to should not be visible because the only target sections
    // are the current section, archived (excluded), and files (excluded)
    expect(screen.queryByText('Move to')).toBeNull()
  })

  it('shows Move-to when other target sections exist', () => {
    const sections = [
      makeSection('sec-ip', 'In Progress', SectionType.WORKSPACES_IN_PROGRESS),
      makeSection('sec-custom', 'My Section', SectionType.WORKSPACES_CUSTOM),
    ]
    render(() => (
      <WorkspaceContextMenu
        {...defaultProps}
        sections={sections}
        currentSectionId="sec-ip"
      />
    ))
    expect(screen.getByText('Move to')).toBeTruthy()
    // The submenu should list the custom section but not the current section
    expect(screen.getByText('My Section')).toBeTruthy()
    expect(screen.queryByText('In Progress')).toBeNull()
  })

  it('excludes current section from Move-to list', () => {
    const sections = [
      makeSection('sec-ip', 'In Progress', SectionType.WORKSPACES_IN_PROGRESS),
      makeSection('sec-custom1', 'Alpha', SectionType.WORKSPACES_CUSTOM),
      makeSection('sec-custom2', 'Beta', SectionType.WORKSPACES_CUSTOM),
    ]
    render(() => (
      <WorkspaceContextMenu
        {...defaultProps}
        sections={sections}
        currentSectionId="sec-custom1"
      />
    ))
    // Alpha (current section) should not appear; others should
    expect(screen.queryByText('Alpha')).toBeNull()
    expect(screen.getByText('In Progress')).toBeTruthy()
    expect(screen.getByText('Beta')).toBeTruthy()
  })

  it('shows "Share..." label', () => {
    render(() => (
      <WorkspaceContextMenu
        {...defaultProps}
        sections={[makeSection('sec-ip', 'In Progress', SectionType.WORKSPACES_IN_PROGRESS)]}
      />
    ))
    expect(screen.getByText('Share...')).toBeTruthy()
  })

  it('shows Archive for non-archived workspaces', () => {
    render(() => (
      <WorkspaceContextMenu
        {...defaultProps}
        isArchived={false}
        sections={[makeSection('sec-ip', 'In Progress', SectionType.WORKSPACES_IN_PROGRESS)]}
      />
    ))
    expect(screen.getByText('Archive')).toBeTruthy()
    expect(screen.queryByText('Unarchive')).toBeNull()
  })

  it('shows Unarchive for archived workspaces', () => {
    render(() => (
      <WorkspaceContextMenu
        {...defaultProps}
        isArchived={true}
        sections={[makeSection('sec-archived', 'Archived', SectionType.WORKSPACES_ARCHIVED)]}
        currentSectionId="sec-archived"
      />
    ))
    expect(screen.getByText('Unarchive')).toBeTruthy()
    expect(screen.queryByText('Archive')).toBeNull()
  })

  it('hides owner-only items when not owner', () => {
    render(() => (
      <WorkspaceContextMenu
        {...defaultProps}
        isOwner={false}
        sections={[makeSection('sec-ip', 'In Progress', SectionType.WORKSPACES_IN_PROGRESS)]}
      />
    ))
    expect(screen.queryByText('Rename')).toBeNull()
    expect(screen.queryByText('Share...')).toBeNull()
    expect(screen.queryByText('Delete')).toBeNull()
    // Archive should still be visible (not owner-only)
    expect(screen.getByText('Archive')).toBeTruthy()
  })
})
