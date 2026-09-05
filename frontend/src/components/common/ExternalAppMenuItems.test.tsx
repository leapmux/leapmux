import type { ExternalApp } from '~/api/platformBridge'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { ExternalAppKind } from '~/generated/proto/leapmux/desktop/v1/frame_pb'
import { ExternalAppMenuItems } from './ExternalAppMenuItems'

function editor(id: string, displayName: string): ExternalApp {
  return { id, displayName, kind: ExternalAppKind.EDITOR }
}

function fileManager(displayName = 'Finder'): ExternalApp {
  return { id: 'file-manager', displayName, kind: ExternalAppKind.FILE_MANAGER }
}

function renderList(apps: ExternalApp[], overrides: {
  preferredId?: string
  onSelect?: (id: string) => void
  onRefresh?: () => void
  refreshing?: boolean
} = {}) {
  const onSelect = overrides.onSelect ?? vi.fn()
  const onRefresh = overrides.onRefresh ?? vi.fn()
  const result = render(() => (
    <div data-testid="host">
      <ExternalAppMenuItems
        apps={() => apps}
        preferredId={() => overrides.preferredId}
        onSelect={onSelect}
        onRefresh={onRefresh}
        refreshing={() => overrides.refreshing ?? false}
        testIdPrefix="apps"
      />
    </div>
  ))
  return { ...result, onSelect, onRefresh }
}

/**
 * Every row's label, in render order. The refresh action is dropped.
 *
 * Reads the label SPAN rather than the row's text: a brand mark carries its
 * own `<title>`, so the row's textContent reads "CursorCursor".
 */
function labels(): string[] {
  return Array.from(
    screen.getByTestId('host').querySelectorAll<HTMLElement>('[data-testid^="apps-item-"] > span > span'),
  ).map(el => el.textContent?.trim() ?? '')
}

describe('externalAppMenuItems', () => {
  // The file manager is a different KIND of target from an editor. Sorting it
  // in among them by name would file "Finder" between "Cursor" and "Visual
  // Studio Code", where nothing tells the reader why it is there.
  it('puts the file manager first, ahead of every editor', () => {
    renderList([
      editor('cursor', 'Cursor'),
      fileManager(),
      editor('vscode', 'Visual Studio Code'),
    ])

    expect(labels()).toEqual(['Finder', 'Cursor', 'Visual Studio Code'])
  })

  it('separates the two groups with one rule', () => {
    const { container } = renderList([editor('cursor', 'Cursor'), fileManager()])

    // One between the groups, one before the refresh action.
    expect(container.querySelectorAll('hr')).toHaveLength(2)
  })

  // A rule above an empty list reads as a menu that lost an item.
  it('draws no group rule when there is no file manager', () => {
    const { container } = renderList([editor('cursor', 'Cursor')])

    expect(container.querySelectorAll('hr')).toHaveLength(1)
    expect(labels()).toEqual(['Cursor'])
  })

  it('draws no group rule when there is no editor', () => {
    const { container } = renderList([fileManager()])

    expect(container.querySelectorAll('hr')).toHaveLength(1)
    expect(labels()).toEqual(['Finder'])
  })

  it('renders only the refresh action for an empty list', () => {
    renderList([])

    expect(labels()).toEqual([])
    expect(screen.getByTestId('apps-refresh')).toBeInTheDocument()
  })

  it('reports the picked application by id', () => {
    const { onSelect } = renderList([editor('zed', 'Zed'), fileManager()])

    fireEvent.click(screen.getByTestId('apps-item-zed'))
    expect(onSelect).toHaveBeenCalledWith('zed')

    fireEvent.click(screen.getByTestId('apps-item-file-manager'))
    expect(onSelect).toHaveBeenCalledWith('file-manager')
  })

  it('marks the remembered application, and only that one', () => {
    const { container } = renderList(
      [editor('zed', 'Zed'), editor('cursor', 'Cursor')],
      { preferredId: 'zed' },
    )

    // The check is the only SVG inside a row beyond its own icon, so counting
    // marked rows is what says the mark is exclusive.
    expect(screen.getByTestId('apps-item-zed').querySelectorAll('svg')).toHaveLength(2)
    expect(screen.getByTestId('apps-item-cursor').querySelectorAll('svg')).toHaveLength(1)
    expect(container.querySelectorAll('[data-testid^="apps-item-"]')).toHaveLength(2)
  })

  // The file manager is a first-class default, not a second-class row.
  it('can mark the file manager as the remembered application', () => {
    renderList([fileManager(), editor('zed', 'Zed')], { preferredId: 'file-manager' })

    expect(screen.getByTestId('apps-item-file-manager').querySelectorAll('svg')).toHaveLength(2)
  })

  it('marks nothing when the remembered id is no longer detected', () => {
    const { container } = renderList([editor('zed', 'Zed')], { preferredId: 'goland' })

    expect(container.querySelectorAll('[data-testid="apps-item-zed"] svg')).toHaveLength(1)
  })

  it('asks for a re-probe from the refresh action', () => {
    const { onRefresh } = renderList([editor('zed', 'Zed')])

    fireEvent.click(screen.getByTestId('apps-refresh'))
    expect(onRefresh).toHaveBeenCalledTimes(1)
  })

  it('disables the refresh action while one is already in flight', () => {
    const { onRefresh } = renderList([editor('zed', 'Zed')], { refreshing: true })

    const refresh = screen.getByTestId('apps-refresh') as HTMLButtonElement
    expect(refresh).toBeDisabled()
    fireEvent.click(refresh)
    expect(onRefresh).not.toHaveBeenCalled()
  })
})
