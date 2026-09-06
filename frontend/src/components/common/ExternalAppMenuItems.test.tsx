import type { ExternalApp } from '~/api/platformBridge'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { editorApp, fileManagerApp } from '~/test-support/externalAppFixtures'
import { ExternalAppMenuItems } from './ExternalAppMenuItems'

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
 * Reads the label SPAN that `DropdownMenuCheckableItem` marks with its own
 * `-label` test id, rather than the row's text: a brand mark carries its own
 * `<title>`, so the row's textContent reads "CursorCursor".
 */
function labels(): string[] {
  return Array.from(
    screen.getByTestId('host').querySelectorAll<HTMLElement>('[data-testid^="apps-item-"][data-testid$="-label"]'),
  ).map(el => el.textContent?.trim() ?? '')
}

/** The id of every row the accessibility tree reports as checked. */
function checkedIds(): string[] {
  return screen.getAllByRole('menuitemradio', { hidden: true })
    .filter(el => el.getAttribute('aria-checked') === 'true')
    .map(el => el.getAttribute('data-testid') ?? '')
}

describe('externalAppMenuItems', () => {
  // The file manager is a different KIND of target from an editor. Sorting it
  // in among them by name would file "Finder" between "Cursor" and "Visual
  // Studio Code", where nothing tells the reader why it is there.
  it('puts the file manager first, ahead of every editor', () => {
    renderList([
      editorApp('cursor', 'Cursor'),
      fileManagerApp(),
      editorApp('vscode', 'Visual Studio Code'),
    ])

    expect(labels()).toEqual(['Finder', 'Cursor', 'Visual Studio Code'])
  })

  it('separates the two groups with one rule', () => {
    const { container } = renderList([editorApp('cursor', 'Cursor'), fileManagerApp()])

    // One between the groups, one before the refresh action.
    expect(container.querySelectorAll('hr')).toHaveLength(2)
  })

  // A rule above an empty list reads as a menu that lost an item.
  it('draws no group rule when there is no file manager', () => {
    const { container } = renderList([editorApp('cursor', 'Cursor')])

    expect(container.querySelectorAll('hr')).toHaveLength(1)
    expect(labels()).toEqual(['Cursor'])
  })

  it('draws no group rule when there is no editor', () => {
    const { container } = renderList([fileManagerApp()])

    expect(container.querySelectorAll('hr')).toHaveLength(1)
    expect(labels()).toEqual(['Finder'])
  })

  it('renders only the refresh action for an empty list', () => {
    renderList([])

    expect(labels()).toEqual([])
    expect(screen.getByTestId('apps-refresh')).toBeInTheDocument()
  })

  it('reports the picked application by id', () => {
    const { onSelect } = renderList([editorApp('zed', 'Zed'), fileManagerApp()])

    fireEvent.click(screen.getByTestId('apps-item-zed'))
    expect(onSelect).toHaveBeenCalledWith('zed')

    fireEvent.click(screen.getByTestId('apps-item-file-manager'))
    expect(onSelect).toHaveBeenCalledWith('file-manager')
  })

  // Asserted through `aria-checked`, which is what a screen reader reads. A
  // background fill and a check glyph are both invisible to the accessibility
  // tree, so a row that only LOOKS marked announces no state at all.
  it('marks the remembered application, and only that one', () => {
    renderList(
      [editorApp('zed', 'Zed'), editorApp('cursor', 'Cursor')],
      { preferredId: 'zed' },
    )

    expect(checkedIds()).toEqual(['apps-item-zed'])
  })

  it('announces every row as a radio, so the marked one carries a state', () => {
    renderList([editorApp('zed', 'Zed'), fileManagerApp()], { preferredId: 'zed' })

    const rows = screen.getAllByRole('menuitemradio', { hidden: true })
    expect(rows).toHaveLength(2)
    for (const row of rows)
      expect(row.getAttribute('aria-checked')).toMatch(/^(?:true|false)$/)
  })

  // The file manager is a first-class default, not a second-class row.
  it('can mark the file manager as the remembered application', () => {
    renderList([fileManagerApp(), editorApp('zed', 'Zed')], { preferredId: 'file-manager' })

    expect(checkedIds()).toEqual(['apps-item-file-manager'])
  })

  it('marks nothing when the remembered id is no longer detected', () => {
    renderList([editorApp('zed', 'Zed')], { preferredId: 'goland' })

    expect(checkedIds()).toEqual([])
  })

  // The refresh action RUNS something rather than naming a state, so it must
  // not join the radio group -- a screen reader would otherwise offer it as a
  // fourth application to remember.
  it('leaves the refresh action a plain menu item', () => {
    renderList([editorApp('zed', 'Zed')])

    expect(screen.getByTestId('apps-refresh').getAttribute('role')).toBe('menuitem')
    expect(screen.getByTestId('apps-refresh').hasAttribute('aria-checked')).toBe(false)
  })

  it('asks for a re-probe from the refresh action', () => {
    const { onRefresh } = renderList([editorApp('zed', 'Zed')])

    fireEvent.click(screen.getByTestId('apps-refresh'))
    expect(onRefresh).toHaveBeenCalledTimes(1)
  })

  it('disables the refresh action while one is already in flight', () => {
    const { onRefresh } = renderList([editorApp('zed', 'Zed')], { refreshing: true })

    const refresh = screen.getByTestId('apps-refresh') as HTMLButtonElement
    expect(refresh).toBeDisabled()
    fireEvent.click(refresh)
    expect(onRefresh).not.toHaveBeenCalled()
  })
})
