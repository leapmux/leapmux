import type { Tab } from '~/stores/tab.types'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TabBar } from '~/components/shell/TabBar'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { loadBrowserPrefs } from '~/lib/browserStorage'
import { WORKSPACE_KEYBINDINGS } from '~/lib/shortcuts/defaults'
import { activateBindings, unbindAll } from '~/lib/shortcuts/keybindings'
import { tabKey } from '~/stores/tab.helpers'

// Mock solid-dnd to avoid DragDropProvider context requirement
vi.mock('@thisbeyond/solid-dnd', () => ({
  createSortable: () => ({}),
  createDroppable: () => () => {},
  SortableProvider: (props: any) => <>{props.children}</>,
  transformStyle: () => undefined,
}))

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
