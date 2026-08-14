import { create } from '@bufbuild/protobuf'
import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { WorkspaceContextMenu } from '~/components/workspace/WorkspaceContextMenu'
import { SectionSchema, SectionType, Sidebar } from '~/generated/leapmux/v1/section_pb'

/**
 * Records what the mocked DropdownMenu last received for `contextMenuFor`. A
 * holder object rather than a bare `let`: TS cannot see the assignment inside the
 * `vi.mock` factory, so it would narrow a `let` to `undefined` at every read.
 */
const captured: { contextMenuFor?: () => HTMLElement | undefined } = {}

// Mock DropdownMenu to render children directly (jsdom lacks popover API).
vi.mock('~/components/common/DropdownMenu', () => ({
  DropdownMenu(props: any) {
    // eslint-disable-next-line solid/reactivity -- capturing the accessor itself for the assertion, not reading it
    captured.contextMenuFor = props.contextMenuFor
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
  isArchived: false,
  sections: [] as ReturnType<typeof makeSection>[],
  currentSectionId: 'sec-ip',
  onRename: noop,
  onMoveTo: noop as (sectionId: string) => void,
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
    expect(screen.queryByText('Move to')).not.toBeInTheDocument()
    // Unarchive should be visible instead of Archive
    expect(screen.getByText('Unarchive')).toBeInTheDocument()
    expect(screen.queryByText('Archive')).not.toBeInTheDocument()
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
    expect(screen.queryByText('Move to')).not.toBeInTheDocument()
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
    expect(screen.getByText('Move to')).toBeInTheDocument()
    // The submenu should list the custom section but not the current section
    expect(screen.getByText('My Section')).toBeInTheDocument()
    expect(screen.queryByText('In Progress')).not.toBeInTheDocument()
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
    expect(screen.queryByText('Alpha')).not.toBeInTheDocument()
    expect(screen.getByText('In Progress')).toBeInTheDocument()
    expect(screen.getByText('Beta')).toBeInTheDocument()
  })

  it('shows Archive for non-archived workspaces', () => {
    render(() => (
      <WorkspaceContextMenu
        {...defaultProps}
        isArchived={false}
        sections={[makeSection('sec-ip', 'In Progress', SectionType.WORKSPACES_IN_PROGRESS)]}
      />
    ))
    expect(screen.getByText('Archive')).toBeInTheDocument()
    expect(screen.queryByText('Unarchive')).not.toBeInTheDocument()
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
    expect(screen.getByText('Unarchive')).toBeInTheDocument()
    expect(screen.queryByText('Archive')).not.toBeInTheDocument()
  })

  it('always offers rename and delete (owner-only access: every visible workspace is our own)', () => {
    render(() => (
      <WorkspaceContextMenu
        {...defaultProps}
        sections={[makeSection('sec-ip', 'In Progress', SectionType.WORKSPACES_IN_PROGRESS)]}
      />
    ))
    expect(screen.getByText('Rename')).toBeInTheDocument()
    expect(screen.getByText('Delete')).toBeInTheDocument()
    expect(screen.getByText('Archive')).toBeInTheDocument()
  })

  // One representative for the six row menus. The other five wrappers forward the
  // same prop through the same two lines, and `DropdownMenu` owns everything that
  // happens after -- covered in ~/components/common/DropdownMenu.test.tsx.
  it('forwards contextMenuFor so the row itself opens the menu', () => {
    // No reset first: the assertion is identity against a FRESH element, so a
    // stale capture from an earlier render fails rather than passing by accident.
    const row = document.createElement('div')

    render(() => (
      <WorkspaceContextMenu
        {...defaultProps}
        contextMenuFor={() => row}
        sections={[makeSection('sec-ip', 'In Progress', SectionType.WORKSPACES_IN_PROGRESS)]}
      />
    ))

    expect(captured.contextMenuFor?.()).toBe(row)
  })
})
