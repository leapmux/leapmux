import type { SidebarSectionDef } from './CollapsibleSidebar'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import Folder from 'lucide-solid/icons/folder'
import { describe, expect, it, vi } from 'vitest'
import { CollapsibleSidebar } from './CollapsibleSidebar'

/** Helper to build a minimal section definition. */
function makeSection(overrides: Partial<SidebarSectionDef> & { id: string, title: string }): SidebarSectionDef {
  return {
    railIcon: Folder,
    content: () => (
      <div data-testid={`content-${overrides.id}`}>
        {overrides.title}
        {' '}
        content
      </div>
    ),
    ...overrides,
  }
}

/** Renders CollapsibleSidebar in expanded (non-collapsed) state. */
function renderSidebar(props: {
  sections: SidebarSectionDef[]
  initialOpenSections?: Record<string, boolean>
  initialSectionSizes?: Record<string, number>
  onStateChange?: (openSections: Record<string, boolean>, sectionSizes: Record<string, number>) => void
}) {
  return render(() => (
    <CollapsibleSidebar
      sections={props.sections}
      side="left"
      isCollapsed={false}
      onExpand={() => {}}
      initialOpenSections={props.initialOpenSections}
      initialSectionSizes={props.initialSectionSizes}
      onStateChange={props.onStateChange}
    />
  ))
}

describe('collapsibleSidebar', () => {
  it('renders section headers', () => {
    renderSidebar({
      sections: [
        makeSection({ id: 'a', title: 'Section A' }),
        makeSection({ id: 'b', title: 'Section B' }),
      ],
    })

    expect(screen.getByText('Section A')).toBeTruthy()
    expect(screen.getByText('Section B')).toBeTruthy()
  })

  it('shows resize handle between 2 expanded sections', () => {
    renderSidebar({
      sections: [
        makeSection({ id: 'a', title: 'Section A' }),
        makeSection({ id: 'b', title: 'Section B' }),
      ],
      initialOpenSections: { a: true, b: true },
    })

    const handles = screen.getAllByTestId('pane-resize-handle')
    expect(handles).toHaveLength(1)
  })

  it('does not show resize handle when only 1 section is expanded', () => {
    renderSidebar({
      sections: [
        makeSection({ id: 'a', title: 'Section A' }),
        makeSection({ id: 'b', title: 'Section B' }),
      ],
      initialOpenSections: { a: true, b: false },
    })

    expect(screen.queryAllByTestId('pane-resize-handle')).toHaveLength(0)
  })

  it('does not show resize handle for railOnly sections', () => {
    renderSidebar({
      sections: [
        makeSection({ id: 'a', title: 'Section A' }),
        makeSection({ id: 'b', title: 'Section B', railOnly: true }),
      ],
      initialOpenSections: { a: true },
    })

    expect(screen.queryAllByTestId('pane-resize-handle')).toHaveLength(0)
  })

  it('shows N-1 resize handles for N expanded sections', () => {
    renderSidebar({
      sections: [
        makeSection({ id: 'a', title: 'A' }),
        makeSection({ id: 'b', title: 'B' }),
        makeSection({ id: 'c', title: 'C' }),
      ],
      initialOpenSections: { a: true, b: true, c: true },
    })

    const handles = screen.getAllByTestId('pane-resize-handle')
    expect(handles).toHaveLength(2)
  })

  it('calls onStateChange when a section is toggled', () => {
    const onStateChange = vi.fn()
    renderSidebar({
      sections: [
        makeSection({ id: 'a', title: 'Section A' }),
        makeSection({ id: 'b', title: 'Section B' }),
      ],
      initialOpenSections: { a: true, b: true },
      onStateChange,
    })

    // Click the summary of section B to collapse it
    const summaries = screen.getAllByText('Section B')
    const summary = summaries[0].closest('summary')
    if (summary) {
      fireEvent.click(summary)
    }

    expect(onStateChange).toHaveBeenCalled()
    const [openSections] = onStateChange.mock.calls[onStateChange.mock.calls.length - 1]
    expect(openSections.b).toBe(false)
  })

  it('calls onStateChange after resize drag', async () => {
    const onStateChange = vi.fn()
    renderSidebar({
      sections: [
        makeSection({ id: 'a', title: 'Section A' }),
        makeSection({ id: 'b', title: 'Section B' }),
      ],
      initialOpenSections: { a: true, b: true },
      onStateChange,
    })

    const handle = screen.getByTestId('pane-resize-handle')

    // Simulate drag: mousedown, mousemove, mouseup
    fireEvent.mouseDown(handle, { clientY: 100 })
    fireEvent.mouseMove(document, { clientY: 150 })
    fireEvent.mouseUp(document)

    // onStateChange should have been called (at least for mouseup)
    expect(onStateChange).toHaveBeenCalled()
    const lastCall = onStateChange.mock.calls[onStateChange.mock.calls.length - 1]
    const [, sectionSizes] = lastCall
    // Both sections should have sizes
    expect(sectionSizes.a).toBeDefined()
    expect(sectionSizes.b).toBeDefined()
  })

  it('resets to equal sizes on double-click', () => {
    const onStateChange = vi.fn()
    renderSidebar({
      sections: [
        makeSection({ id: 'a', title: 'Section A' }),
        makeSection({ id: 'b', title: 'Section B' }),
      ],
      initialOpenSections: { a: true, b: true },
      initialSectionSizes: { a: 0.7, b: 0.3 },
      onStateChange,
    })

    const handle = screen.getByTestId('pane-resize-handle')
    fireEvent.dblClick(handle)

    expect(onStateChange).toHaveBeenCalled()
    const lastCall = onStateChange.mock.calls[onStateChange.mock.calls.length - 1]
    const [, sectionSizes] = lastCall
    expect(sectionSizes.a).toBe(0.5)
    expect(sectionSizes.b).toBe(0.5)
  })

  it('resets all N sections to equal sizes on double-click', () => {
    const onStateChange = vi.fn()
    renderSidebar({
      sections: [
        makeSection({ id: 'a', title: 'A' }),
        makeSection({ id: 'b', title: 'B' }),
        makeSection({ id: 'c', title: 'C' }),
      ],
      initialOpenSections: { a: true, b: true, c: true },
      initialSectionSizes: { a: 0.5, b: 0.3, c: 0.2 },
      onStateChange,
    })

    // Double-click the first handle
    const handles = screen.getAllByTestId('pane-resize-handle')
    fireEvent.dblClick(handles[0])

    expect(onStateChange).toHaveBeenCalled()
    const lastCall = onStateChange.mock.calls[onStateChange.mock.calls.length - 1]
    const [, sectionSizes] = lastCall
    const third = 1 / 3
    expect(sectionSizes.a).toBeCloseTo(third)
    expect(sectionSizes.b).toBeCloseTo(third)
    expect(sectionSizes.c).toBeCloseTo(third)
  })

  it('removes resize handle when collapsing a section', () => {
    renderSidebar({
      sections: [
        makeSection({ id: 'a', title: 'Section A' }),
        makeSection({ id: 'b', title: 'Section B' }),
        makeSection({ id: 'c', title: 'Section C' }),
      ],
      initialOpenSections: { a: true, b: true, c: true },
    })

    // Initially 2 handles
    expect(screen.getAllByTestId('pane-resize-handle')).toHaveLength(2)

    // Collapse section B by clicking its summary
    const summary = screen.getByText('Section B').closest('summary')
    if (summary) {
      fireEvent.click(summary)
    }

    // Should now have 1 handle (between A and C)
    expect(screen.getAllByTestId('pane-resize-handle')).toHaveLength(1)
  })

  it('renders collapsed rail when isCollapsed is true', () => {
    render(() => (
      <CollapsibleSidebar
        sections={[makeSection({ id: 'a', title: 'Section A' })]}
        side="left"
        isCollapsed={true}
        onExpand={() => {}}
      />
    ))

    // Section header should not be visible in rail
    expect(screen.queryByText('Section A')).toBeNull()
    // Expand button should be visible
    expect(screen.getByTitle('Expand left sidebar')).toBeTruthy()
  })
})
