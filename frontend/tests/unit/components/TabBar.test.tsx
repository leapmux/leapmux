import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { TabBar } from '~/components/shell/TabBar'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { TabType } from '~/stores/tab.store'

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
vi.mock('~/components/common/DropdownMenu', () => ({
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
}))

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
  showAddButton: true,
  onSelect: noop,
  onClose: noop,
  onRename: noop as any,
  onNewAgent: noop,
  onNewTerminal: noop,
}

function makeTab(type: TabType, id: string, title?: string) {
  return { type, id, title, position: '0|' }
}

describe('tabBar readOnly prop', () => {
  it('shows close button for all tab types when readOnly is false', () => {
    const tabs = [
      makeTab(TabType.AGENT, 'a1', 'Agent 1'),
      makeTab(TabType.TERMINAL, 't1', 'Terminal 1'),
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
      makeTab(TabType.AGENT, 'a1', 'Agent 1'),
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
      makeTab(TabType.AGENT, 'a1', 'Agent 1'),
      makeTab(TabType.TERMINAL, 't1', 'Terminal 1'),
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
      makeTab(TabType.AGENT, 'a1', 'Agent 1'),
      makeTab(TabType.TERMINAL, 't1', 'Terminal 1'),
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
          showAddButton={false}
        />
      </PreferencesProvider>
    ))
    expect(screen.queryByTestId('new-agent-button')).not.toBeInTheDocument()
    expect(screen.queryByTestId('new-terminal-button')).not.toBeInTheDocument()
  })

  it('scrolls the tab list horizontally on vertical wheel input when overflowing', () => {
    const tabs = [
      makeTab(TabType.AGENT, 'a1', 'Agent 1'),
      makeTab(TabType.TERMINAL, 't1', 'Terminal 1'),
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
})
