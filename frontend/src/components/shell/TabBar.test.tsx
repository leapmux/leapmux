import type { Tab } from '~/stores/tab.types'
import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import * as solidDnd from '@thisbeyond/solid-dnd'
import { createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TabBar } from '~/components/shell/TabBar'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { loadBrowserPrefs } from '~/lib/browserStorage'
import { WORKSPACE_KEYBINDINGS } from '~/lib/shortcuts/defaults'
import { activateBindings, unbindAll } from '~/lib/shortcuts/keybindings'
import { tabKey } from '~/stores/tab.helpers'

// Mock solid-dnd to avoid DragDropProvider context requirement. The sortable
// mock exposes the contract the strip now relies on — `.ref` for node-only
// registration and a `dragActivators` getter — plus one press spy per row, so
// tests can assert WHERE activation reaches: the guarded row body or the raw
// grip. `createdSortables` collects the spies for exactly that.
vi.mock('@thisbeyond/solid-dnd', async () => {
  const { vi } = await import('vitest')
  const createdSortables: Array<{ ref: ReturnType<typeof vi.fn>, onPointerdown: ReturnType<typeof vi.fn> }> = []
  return {
    createdSortables,
    createSortable: () => {
      const onPointerdown = vi.fn()
      const sortable = {
        ref: vi.fn(),
        isActiveDraggable: false,
        transform: undefined,
        onPointerdown,
        get dragActivators() {
          return { onPointerdown }
        },
      }
      createdSortables.push(sortable)
      return sortable
    },
    createDroppable: () => () => {},
    SortableProvider: (props: any) => <>{props.children}</>,
    transformStyle: () => undefined,
  }
})

// Mock TabDragContext
vi.mock('~/components/shell/TabDragContext', () => ({
  TABBAR_ZONE_PREFIX: 'tabbar:',
  useTabDrag: () => {
    throw new Error('not in provider')
  },
}))

// Mock DropdownMenu to render children directly (jsdom lacks popover API)
vi.mock('~/components/common/DropdownMenu', async () => {
  const { createSignal } = await import('solid-js')
  return {
    createContextMenuAnchor: () => {
      const [anchor, setAnchor] = createSignal<HTMLElement>()
      return [anchor, setAnchor]
    },
    DropdownMenu(props: any) {
      const trigger = () => typeof props.trigger === 'function'
        ? props.trigger({
            'aria-expanded': false,
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
    DropdownMenuItemContent(props: any) {
      return (
        <span>
          <span>{props.label}</span>
          {props.shortcut ? <span>{props.shortcut}</span> : null}
        </span>
      )
    },
    DropdownMenuCheckableItem(props: any) {
      return (
        <button
          role={props.kind === 'checkbox' ? 'menuitemcheckbox' : 'menuitemradio'}
          aria-checked={props.checked}
          disabled={props.disabled}
          onClick={() => props.onSelect()}
        >
          <input type={props.kind} checked={props.checked} disabled />
          <span>{props.label}</span>
        </button>
      )
    },
  }
})

// Mock TabBar.css to provide minimal class names
vi.mock('~/components/shell/TabBar.css', () => ({
  tabBar: 'tabBar',
  tabList: 'tabList',
  tabListDropTarget: 'tabListDropTarget',
  tab: 'tab',
  tabDragging: 'tabDragging',
  tabIcon: 'tabIcon',
  tabText: 'tabText',
  tabEditInput: 'tabEditInput',
  tabNotification: 'tabNotification',
  tabClose: 'tabClose',
  tooltipTrigger: 'tooltipTrigger',
  newTabWrapper: 'newTabWrapper',
  collapsedNewTab: 'collapsedNewTab',
  collapsedOverflow: 'collapsedOverflow',
  shellDefault: 'shellDefault',
  tabChip: 'tabChip',
  tabChipLabel: 'tabChipLabel',
  tabChipCount: 'tabChipCount',
  tabChipChevron: 'tabChipChevron',
  mobileNewTab: 'mobileNewTab',
  sheetOverlay: 'sheetOverlay',
  sheetOverlayOpen: 'sheetOverlayOpen',
  sheetPanel: 'sheetPanel',
  sheetPanelOpen: 'sheetPanelOpen',
  sheetPanelClip: 'sheetPanelClip',
  sheetHeader: 'sheetHeader',
  sheetTitle: 'sheetTitle',
  sheetList: 'sheetList',
  sheetRow: 'sheetRow',
  sheetRowLabel: 'sheetRowLabel',
  sheetEmpty: 'sheetEmpty',
}))

function noop() {}

const defaultProps = {
  tileId: 'tile-1',
  tabs: [] as any[],
  activeTabKey: null,
  onSelect: noop,
  onClose: noop,
  onRename: noop as any,
  newTab: {
    showAddButton: true,
    onNewAgent: noop,
    onNewTerminal: noop,
  },
}

function makeTab(type: Tab['type'], id: string, title?: string): Tab {
  return { type, id, title, workspaceId: 'ws-1', position: '0|' } as Tab
}

function getBrowserPrefs() {
  return loadBrowserPrefs() as Record<string, unknown>
}

/**
 * jsdom's PointerEvent constructor ignores the `pointerType` init property in
 * this environment, so the property is defined on the event directly — the
 * same trick as ~/lib/dragActivators.test.ts.
 */
function pointerPress(pointerType: string): PointerEvent {
  const event = new PointerEvent('pointerdown', { bubbles: true })
  Object.defineProperty(event, 'pointerType', { value: pointerType })
  return event
}

/** Solid flushes effects on a microtask; two awaits are enough for that. */
async function flushEffects(): Promise<void> {
  await Promise.resolve()
  await Promise.resolve()
}

interface MockSortable {
  ref: ReturnType<typeof vi.fn>
  onPointerdown: ReturnType<typeof vi.fn>
}

/** The sortable mocks the solid-dnd mock collected from the current render. */
function createdSortables(): MockSortable[] {
  return (solidDnd as unknown as { createdSortables: MockSortable[] }).createdSortables
}

beforeEach(() => {
  localStorage.clear()
})

// `activateBindings` writes into a module singleton and installs a document
// listener, so a test that activates them owes an unbind even when it FAILS.
// Unbinding at the end of the test body only runs on the happy path, which is
// the one case that needed it least: a thrown assertion would leave the
// bindings live for every later test in this worker, turning one real failure
// into a cascade that hides it. An unconditional afterEach is idempotent when
// nothing was bound.
afterEach(() => {
  unbindAll()
})

describe('tabBar readOnly prop', () => {
  it('shows shortcut hints on dialog menu items', () => {
    activateBindings(WORKSPACE_KEYBINDINGS, 'workspace')
    render(() => (
      <PreferencesProvider>
        <TabBar
          {...defaultProps}
          newTab={{ ...defaultProps.newTab, availableProviders: [] }}
        />
      </PreferencesProvider>
    ))

    expect(screen.getAllByRole('menuitem', { name: /New agent\.\.\.Ctrl\+Shift\+N/ }).length).toBeGreaterThan(0)
    expect(screen.getAllByRole('menuitem', { name: /New terminal\.\.\.Ctrl\+Shift\+T/ }).length).toBeGreaterThan(0)
  })

  it('shows close button for all tab types when readOnly is false', () => {
    const tabs = [
      makeTab(TabType.AGENT, 'a1', 'Agent Olivia'),
      makeTab(TabType.TERMINAL, 't1', 'Terminal Liam'),
      makeTab(TabType.FILE, 'f1', 'File 1'),
    ]
    render(() => (
      <PreferencesProvider>
        <TabBar
          {...defaultProps}
          tabs={tabs}
          readOnly={false}
        />
      </PreferencesProvider>
    ))
    const closeButtons = screen.getAllByTestId('tab-close')
    expect(closeButtons).toHaveLength(3)
  })

  it('disables the close button while a persisted tab is closing', () => {
    const tabs = [
      makeTab(TabType.AGENT, 'a1', 'Agent Olivia'),
    ]
    render(() => (
      <PreferencesProvider>
        <TabBar
          {...defaultProps}
          tabs={tabs}
          closingTabKeys={new Set([`${TabType.AGENT}:a1`])}
          readOnly={false}
        />
      </PreferencesProvider>
    ))
    expect(screen.getByTestId('tab-close')).toBeDisabled()
  })

  it('hides close button for agent and terminal tabs when readOnly is true', () => {
    const tabs = [
      makeTab(TabType.AGENT, 'a1', 'Agent Olivia'),
      makeTab(TabType.TERMINAL, 't1', 'Terminal Liam'),
    ]
    render(() => (
      <PreferencesProvider>
        <TabBar
          {...defaultProps}
          tabs={tabs}
          readOnly={true}
        />
      </PreferencesProvider>
    ))
    const closeButtons = screen.queryAllByTestId('tab-close')
    expect(closeButtons).toHaveLength(0)
  })

  it('shows close button for file tabs when readOnly is true', () => {
    const tabs = [
      makeTab(TabType.FILE, 'f1', 'readme.md'),
    ]
    render(() => (
      <PreferencesProvider>
        <TabBar
          {...defaultProps}
          tabs={tabs}
          readOnly={true}
        />
      </PreferencesProvider>
    ))
    const closeButtons = screen.getAllByTestId('tab-close')
    expect(closeButtons).toHaveLength(1)
  })

  it('shows close button for file tab but not agent/terminal when readOnly is true', () => {
    const tabs = [
      makeTab(TabType.AGENT, 'a1', 'Agent Olivia'),
      makeTab(TabType.TERMINAL, 't1', 'Terminal Liam'),
      makeTab(TabType.FILE, 'f1', 'readme.md'),
    ]
    render(() => (
      <PreferencesProvider>
        <TabBar
          {...defaultProps}
          tabs={tabs}
          readOnly={true}
        />
      </PreferencesProvider>
    ))
    // Only the file tab should have a close button
    const closeButtons = screen.getAllByTestId('tab-close')
    expect(closeButtons).toHaveLength(1)
  })

  it('hides add-tab buttons when readOnly is true', () => {
    render(() => (
      <PreferencesProvider>
        <TabBar
          {...defaultProps}
          tabs={[makeTab(TabType.AGENT, 'a1', 'Agent')]}
          readOnly={true}
          newTab={{ ...defaultProps.newTab, showAddButton: false }}
        />
      </PreferencesProvider>
    ))
    expect(screen.queryByTestId('new-agent-button')).not.toBeInTheDocument()
    expect(screen.queryByTestId('new-terminal-button')).not.toBeInTheDocument()
  })

  it('scrolls the tab list horizontally on vertical wheel input when overflowing', () => {
    const tabs = [
      makeTab(TabType.AGENT, 'a1', 'Agent Olivia'),
      makeTab(TabType.TERMINAL, 't1', 'Terminal Liam'),
      makeTab(TabType.FILE, 'f1', 'File 1'),
    ]
    render(() => (
      <PreferencesProvider>
        <TabBar
          {...defaultProps}
          tabs={tabs}
          readOnly={false}
        />
      </PreferencesProvider>
    ))

    const tabList = screen.getByTestId('tab-list') as HTMLDivElement
    Object.defineProperty(tabList, 'clientWidth', { configurable: true, value: 120 })
    Object.defineProperty(tabList, 'scrollWidth', { configurable: true, value: 480 })
    Object.defineProperty(tabList, 'scrollLeft', { configurable: true, writable: true, value: 0 })

    fireEvent.wheel(tabList, { deltaY: 60, deltaX: 0 })

    expect(tabList.scrollLeft).toBe(60)
  })

  it('renders the Advanced section with Expand agent thoughts before Show hidden messages', () => {
    render(() => (
      <PreferencesProvider>
        <TabBar
          {...defaultProps}
          newTab={{ ...defaultProps.newTab, availableProviders: [] }}
        />
      </PreferencesProvider>
    ))

    expect(screen.getAllByText('Advanced').length).toBeGreaterThan(0)
    expect(screen.queryByText('Developer')).not.toBeInTheDocument()

    const expandItems = screen.getAllByRole('menuitemcheckbox', { name: /Expand agent thoughts/ })
    const hiddenItems = screen.getAllByRole('menuitemcheckbox', { name: /Show hidden messages/ })
    expect(expandItems.length).toBeGreaterThan(0)
    expect(hiddenItems.length).toBeGreaterThan(0)
    expect(expandItems[0].compareDocumentPosition(hiddenItems[0]) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0)
  })

  it('toggles Expand agent thoughts and persists the browser preference', () => {
    render(() => (
      <PreferencesProvider>
        <TabBar
          {...defaultProps}
          newTab={{ ...defaultProps.newTab, availableProviders: [] }}
        />
      </PreferencesProvider>
    ))

    // The toggles carry role=menuitemcheckbox, so a screen reader announces
    // their on/off state; the display-only indicator is aria-hidden.
    const menuItem = screen.getAllByRole('menuitemcheckbox', { name: /Expand agent thoughts/ })[0]
    expect(menuItem).toHaveTextContent('Expand agent thoughts')
    expect(menuItem).toHaveAttribute('aria-checked', 'true')
    const checkbox = menuItem.querySelector('input[type="checkbox"]') as HTMLInputElement
    expect(checkbox.checked).toBe(true)
    expect(getBrowserPrefs().expandAgentThoughts).toBeUndefined()

    fireEvent.click(menuItem)
    expect(checkbox.checked).toBe(false)
    expect(menuItem).toHaveAttribute('aria-checked', 'false')
    expect(getBrowserPrefs().expandAgentThoughts).toBe(false)

    fireEvent.click(menuItem)
    expect(checkbox.checked).toBe(true)
    expect(getBrowserPrefs().expandAgentThoughts).toBeUndefined()
  })
})

describe('tabBar tileActions for grid', () => {
  it('shows Make a 2×2 grid in the micro overflow when canMakeGrid is true', () => {
    render(() => (
      <PreferencesProvider>
        <TabBar
          {...defaultProps}
          tileActions={{
            canSplit: true,
            canMakeGrid: true,
            closeMode: { kind: 'tile' },
            onSplit: noop,
            onMakeGrid: noop,
            onClose: noop,
          }}
        />
      </PreferencesProvider>
    ))
    expect(screen.getByTestId('make-grid-menu-item')).toBeInTheDocument()
  })

  it('hides the make-grid menu item when canMakeGrid is false', () => {
    render(() => (
      <PreferencesProvider>
        <TabBar
          {...defaultProps}
          tileActions={{
            canSplit: true,
            canMakeGrid: false,
            closeMode: { kind: 'tile' },
            onSplit: noop,
            onMakeGrid: noop,
            onClose: noop,
          }}
        />
      </PreferencesProvider>
    ))
    expect(screen.queryByTestId('make-grid-menu-item')).toBeNull()
  })

  it('renders Close grid label for closeMode=grid', () => {
    render(() => (
      <PreferencesProvider>
        <TabBar
          {...defaultProps}
          tileActions={{
            canSplit: false,
            canMakeGrid: false,
            closeMode: { kind: 'grid', gridId: 'g1' },
            onSplit: noop,
            onMakeGrid: noop,
            onClose: noop,
          }}
        />
      </PreferencesProvider>
    ))
    expect(screen.getByTestId('close-grid-menu-item')).toHaveTextContent(/Close grid/)
    expect(screen.queryByTestId('close-tile-menu-item')).toBeNull()
  })

  it('hides the tile-close menu item entirely for closeMode=none', () => {
    render(() => (
      <PreferencesProvider>
        <TabBar
          {...defaultProps}
          tileActions={{
            canSplit: false,
            canMakeGrid: false,
            closeMode: { kind: 'none' },
            onSplit: noop,
            onMakeGrid: noop,
            onClose: noop,
          }}
        />
      </PreferencesProvider>
    ))
    expect(screen.queryByTestId('close-grid-menu-item')).toBeNull()
    expect(screen.queryByTestId('close-tile-menu-item')).toBeNull()
  })
})

/**
 * A `Tab` is a join result, rebuilt whenever any field it derives from
 * `tabMetadata` changes. Keying the strip's `<For>` on those objects made every
 * such change dispose the row and mount a fresh one, which is fatal for the two
 * things a row can be holding: the inline rename `<input>` the user is typing
 * into, and `createSortable`'s drag handle.
 */
describe('tabBar row identity', () => {
  it('keeps the same row element when a tab field changes', () => {
    const [tabs, setTabs] = createSignal<Tab[]>([
      makeTab(TabType.AGENT, 'a1', 'Agent Olivia'),
      makeTab(TabType.TERMINAL, 't1', 'Terminal Liam'),
    ])
    render(() => (
      <PreferencesProvider>
        <TabBar {...defaultProps} tabs={tabs()} activeTabKey={`${TabType.AGENT}:a1`} />
      </PreferencesProvider>
    ))

    const before = screen.getAllByTestId('tab')
    expect(before).toHaveLength(2)

    // What a title rename, a git badge, an agent status flip or an MRU stamp
    // all look like from here: the SAME tabs, as freshly built objects.
    setTabs([
      makeTab(TabType.AGENT, 'a1', 'Agent Renamed'),
      makeTab(TabType.TERMINAL, 't1', 'Terminal Liam'),
    ])

    const after = screen.getAllByTestId('tab')
    expect(after[0], 'the row must be updated in place, not replaced').toBe(before[0])
    expect(after[1], 'an untouched sibling must not be remounted either').toBe(before[1])
    // ...and the change still reaches the DOM through the row's own reactivity.
    expect(after[0].textContent).toContain('Agent Renamed')
  })

  it('remounts rows only when a tab is actually added, removed, or reordered', () => {
    const [tabs, setTabs] = createSignal<Tab[]>([
      makeTab(TabType.AGENT, 'a1', 'Agent Olivia'),
      makeTab(TabType.TERMINAL, 't1', 'Terminal Liam'),
    ])
    render(() => (
      <PreferencesProvider>
        <TabBar {...defaultProps} tabs={tabs()} activeTabKey={`${TabType.AGENT}:a1`} />
      </PreferencesProvider>
    ))
    const before = screen.getAllByTestId('tab')

    setTabs([
      makeTab(TabType.AGENT, 'a1', 'Agent Olivia'),
      makeTab(TabType.TERMINAL, 't1', 'Terminal Liam'),
      makeTab(TabType.FILE, 'f1', 'readme.md'),
    ])

    const after = screen.getAllByTestId('tab')
    expect(after).toHaveLength(3)
    expect(after[0], 'adding a tab must not disturb the existing rows').toBe(before[0])
    expect(after[1]).toBe(before[1])
  })

  it('keeps a row being renamed mounted through an unrelated tab\'s change', () => {
    const [tabs, setTabs] = createSignal<Tab[]>([
      makeTab(TabType.AGENT, 'a1', 'Agent Olivia'),
      makeTab(TabType.TERMINAL, 't1', 'Terminal Liam'),
    ])
    render(() => (
      <PreferencesProvider>
        <TabBar {...defaultProps} tabs={tabs()} activeTabKey={`${TabType.AGENT}:a1`} />
      </PreferencesProvider>
    ))

    // Double-click opens the inline rename input on the agent row.
    fireEvent.dblClick(screen.getAllByTestId('tab')[0])
    const input = document.querySelector('input.tabEditInput') as HTMLInputElement
    expect(input, 'double-click must open the rename input').toBeTruthy()
    fireEvent.input(input, { target: { value: 'half-typed name' } })

    // A sibling's status flips while the user is mid-rename.
    setTabs([
      makeTab(TabType.AGENT, 'a1', 'Agent Olivia'),
      makeTab(TabType.TERMINAL, 't1', 'Terminal Renamed Elsewhere'),
    ])

    const stillThere = document.querySelector('input.tabEditInput') as HTMLInputElement
    expect(stillThere, 'the rename input must survive a sibling row updating').toBe(input)
    expect(stillThere.value, 'and keep what the user had typed').toBe('half-typed name')
  })
})

/**
 * A tab's tooltip repeats the label for most tabs, but a terminal's carries its
 * live PTY title, which the label never shows. `showWhen="clipped"` is
 * documented for the repeating case only — it hides the tooltip from screen
 * readers too — so a terminal whose OSC title differs must not depend on the
 * label happening to overflow.
 */
describe('tabBar tooltip mode', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    HTMLElement.prototype.showPopover = vi.fn()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  function renderTabs(tabs: Tab[]) {
    render(() => (
      <PreferencesProvider>
        <TabBar {...defaultProps} tabs={tabs} activeTabKey={tabs[0] ? tabKey(tabs[0]) : null} />
      </PreferencesProvider>
    ))
  }

  function hoverLabel(text: string) {
    // jsdom reports zero widths, so nothing is ever "clipped" here — which is
    // exactly the condition that used to suppress the PTY title.
    fireEvent.mouseEnter(screen.getByText(text))
    vi.advanceTimersByTime(700)
  }

  it('shows a terminal tooltip carrying the live PTY title even when nothing is clipped', () => {
    const tab = { ...makeTab(TabType.TERMINAL, 't1', 'Terminal Liam'), ptyTitle: 'vim src/app.ts' } as Tab
    renderTabs([tab])

    hoverLabel('Terminal Liam')

    expect(screen.getByRole('tooltip', { hidden: true }).textContent).toContain('vim src/app.ts')
  })

  it('keeps the clipped-only mode when the tooltip merely repeats the label', () => {
    renderTabs([makeTab(TabType.AGENT, 'a1', 'Agent Olivia')])

    hoverLabel('Agent Olivia')

    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
  })

  it('keeps the clipped-only mode for a terminal with no PTY title', () => {
    renderTabs([makeTab(TabType.TERMINAL, 't1', 'Terminal Liam')])

    hoverLabel('Terminal Liam')

    expect(screen.queryByRole('tooltip', { hidden: true })).toBeNull()
  })
})

/**
 * The mobile variant replaces the horizontal strip with a chip that names the
 * active tab and opens the tab list dropping from the tab bar. The strip
 * itself must not render — a phone viewport cannot fit it, which is the point.
 */
describe('tabBar mobile variant', () => {
  function renderMobileTabBar(tabs: Tab[], activeTabKey: string | null, opts: { onSelect?: (tab: Tab) => void, onClose?: (tab: Tab) => void, onRename?: (tab: Tab, title: string) => void, onNewAgentAdvanced?: () => void, onCloseSidebars?: () => void } = {}) {
    return render(() => (
      <PreferencesProvider>
        <TabBar
          {...defaultProps}
          tabs={tabs}
          activeTabKey={activeTabKey}
          onSelect={opts.onSelect ?? noop}
          onClose={opts.onClose ?? noop}
          onRename={opts.onRename ?? noop}
          newTab={{ ...defaultProps.newTab, onNewAgentAdvanced: opts.onNewAgentAdvanced }}
          mobile={{
            onToggleLeftSidebar: noop,
            onToggleRightSidebar: noop,
            onCloseSidebars: opts.onCloseSidebars,
          }}
        />
      </PreferencesProvider>
    ))
  }

  const twoTabs = () => [
    makeTab(TabType.AGENT, 'a1', 'Agent Olivia'),
    makeTab(TabType.TERMINAL, 't1', 'Terminal Liam'),
  ]

  it('the sheet header counts tabs, with a singular form for one', () => {
    const two = renderMobileTabBar(twoTabs(), `${TabType.AGENT}:a1`)
    fireEvent.click(screen.getByTestId('tab-chip'))
    expect(screen.getByTestId('tab-sheet-title')).toHaveTextContent('2 Tabs')
    two.unmount()

    renderMobileTabBar([makeTab(TabType.AGENT, 'a1', 'Agent Olivia')], `${TabType.AGENT}:a1`)
    fireEvent.click(screen.getByTestId('tab-chip'))
    expect(screen.getByTestId('tab-sheet-title')).toHaveTextContent('1 Tab')
  })

  it('renders the chip instead of the strip, with the active tab and the count', () => {
    renderMobileTabBar(twoTabs(), `${TabType.AGENT}:a1`)

    expect(screen.queryByTestId('tab-list')).toBeNull()
    const chip = screen.getByTestId('tab-chip')
    expect(chip).toHaveTextContent('Agent Olivia')
    expect(screen.getByTestId('tab-chip-count')).toHaveTextContent('2')
  })

  it('opens the sheet on chip tap, lists every tab, and marks the active one', () => {
    renderMobileTabBar(twoTabs(), `${TabType.AGENT}:a1`)

    fireEvent.click(screen.getByTestId('tab-chip'))

    const rows = screen.getAllByTestId('tab-sheet-row')
    expect(rows).toHaveLength(2)
    expect(rows[0]).toHaveAttribute('aria-selected', 'true')
    expect(rows[1]).toHaveAttribute('aria-selected', 'false')
    expect(rows[1]).toHaveTextContent('Terminal Liam')
  })

  it('tapping a sheet row selects the tab and closes the sheet', () => {
    const onSelect = vi.fn()
    renderMobileTabBar(twoTabs(), `${TabType.AGENT}:a1`, { onSelect })

    fireEvent.click(screen.getByTestId('tab-chip'))
    fireEvent.click(screen.getAllByTestId('tab-sheet-row')[1])

    expect(onSelect).toHaveBeenCalledOnce()
    expect(onSelect.mock.calls[0][0].id).toBe('t1')
    expect(screen.getByTestId('tab-sheet-overlay')).not.toHaveClass('sheetOverlayOpen')
  })

  it('the sheet close button closes the tab without dismissing the sheet first', () => {
    const onClose = vi.fn()
    renderMobileTabBar(twoTabs(), `${TabType.AGENT}:a1`, { onClose })

    fireEvent.click(screen.getByTestId('tab-chip'))
    fireEvent.click(screen.getAllByTestId('tab-close')[0])

    expect(onClose).toHaveBeenCalledOnce()
    expect(onClose.mock.calls[0][0].id).toBe('a1')
    // The sheet stays up: closing one tab is not a reason to hide the rest.
    expect(screen.getByTestId('tab-sheet-overlay')).toHaveClass('sheetOverlayOpen')
  })

  it('opening the sheet closes the drawers', () => {
    const onCloseSidebars = vi.fn()
    renderMobileTabBar(twoTabs(), `${TabType.AGENT}:a1`, { onCloseSidebars })

    fireEvent.click(screen.getByTestId('tab-chip'))

    expect(onCloseSidebars).toHaveBeenCalledOnce()
  })

  it('tapping the overlay or pressing Escape closes the sheet', () => {
    renderMobileTabBar(twoTabs(), `${TabType.AGENT}:a1`)

    fireEvent.click(screen.getByTestId('tab-chip'))
    const panel = screen.getByTestId('tab-sheet')
    expect(panel).toHaveClass('sheetPanelOpen')

    fireEvent.keyDown(panel, { key: 'Escape' })
    expect(panel).not.toHaveClass('sheetPanelOpen')

    fireEvent.click(screen.getByTestId('tab-chip'))
    fireEvent.click(screen.getByTestId('tab-sheet-overlay'))
    expect(panel).not.toHaveClass('sheetPanelOpen')
  })

  it('a chip with no tabs opens the new-agent dialog instead of the sheet', () => {
    const onNewAgentAdvanced = vi.fn()
    renderMobileTabBar([], null, { onNewAgentAdvanced })

    fireEvent.click(screen.getByTestId('tab-chip'))

    expect(onNewAgentAdvanced).toHaveBeenCalledOnce()
    expect(screen.getByTestId('tab-sheet-overlay')).not.toHaveClass('sheetOverlayOpen')
  })

  it('sheet rows carry an always-on drag grip', () => {
    renderMobileTabBar(twoTabs(), `${TabType.AGENT}:a1`)

    fireEvent.click(screen.getByTestId('tab-chip'))
    expect(screen.getAllByTestId('tab-sheet-drag-handle')).toHaveLength(2)
  })

  it('the sheet row menu\'s Rename item renames the tab through onRename', () => {
    const onRename = vi.fn()
    renderMobileTabBar(twoTabs(), `${TabType.AGENT}:a1`, { onRename })

    fireEvent.click(screen.getByTestId('tab-chip'))
    const row = screen.getAllByTestId('tab-sheet-row')[1]
    fireEvent.click(within(row).getByTestId('tab-menu-rename'))

    const input = within(row).getByTestId('tab-rename-input') as HTMLInputElement
    expect(input.value).toBe('Terminal Liam')
    fireEvent.input(input, { target: { value: 'Terminal Liam II' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    expect(onRename).toHaveBeenCalledOnce()
    expect(onRename.mock.calls[0][0].id).toBe('t1')
    expect(onRename.mock.calls[0][1]).toBe('Terminal Liam II')
    // The edit ended: the row shows a label again, not an input.
    expect(within(row).queryByTestId('tab-rename-input')).toBeNull()
  })
})

/**
 * The desktop strip keeps whole-row mouse dragging, but touch activation now
 * lives on the grip alone — see `finePointerOnlyActivators`. The mocked
 * sortable carries a press spy per row, so these tests assert the split
 * itself, not just the affordance's presence.
 */
describe('tabBar strip drag handles', () => {
  beforeEach(() => {
    createdSortables().length = 0
  })

  it('renders a grip on every desktop tab row', () => {
    render(() => (
      <PreferencesProvider>
        <TabBar
          {...defaultProps}
          tabs={[
            makeTab(TabType.AGENT, 'a1', 'Agent Olivia'),
            makeTab(TabType.TERMINAL, 't1', 'Terminal Liam'),
          ]}
          activeTabKey={`${TabType.AGENT}:a1`}
        />
      </PreferencesProvider>
    ))

    expect(screen.getAllByTestId('tab-drag-handle')).toHaveLength(2)
  })

  it('registers rows via .ref and splits activation: body mouse-only, grip raw', async () => {
    render(() => (
      <PreferencesProvider>
        <TabBar
          {...defaultProps}
          tabs={[
            makeTab(TabType.AGENT, 'a1', 'Agent Olivia'),
            makeTab(TabType.TERMINAL, 't1', 'Terminal Liam'),
          ]}
          activeTabKey={`${TabType.AGENT}:a1`}
        />
      </PreferencesProvider>
    ))
    await flushEffects()

    const rows = screen.getAllByTestId('tab')
    const grips = screen.getAllByTestId('tab-drag-handle')
    const sortables = createdSortables()
    expect(sortables).toHaveLength(2)
    // Node registration goes through .ref — the call form would attach the
    // sensor activators wholesale, which is exactly what the split avoids.
    expect(sortables[0].ref).toHaveBeenCalledWith(rows[0])

    const presses = () => sortables.reduce((sum, s) => sum + s.onPointerdown.mock.calls.length, 0)
    // A touch press on the row body never reaches the sensor: it belongs to
    // the scroller and the long-press menu.
    rows[0].dispatchEvent(pointerPress('touch'))
    expect(presses()).toBe(0)
    // The grip carries the raw activators. A touch press forwards from the
    // grip and is blocked again where it BUBBLES into the row body — exactly
    // one activation for a finger.
    grips[0].dispatchEvent(pointerPress('touch'))
    expect(presses()).toBe(1)
    // A mouse press on the grip forwards from the grip and from the guarded
    // body it bubbles into — a fine press may start the drag from either.
    grips[0].dispatchEvent(pointerPress('mouse'))
    expect(presses()).toBe(3)
    // The guarded body forwards the mouse directly: the desktop drag path.
    rows[0].dispatchEvent(pointerPress('mouse'))
    expect(presses()).toBe(4)
  })
})
