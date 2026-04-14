import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { TabType } from '~/stores/tab.store'
import { WorkspaceTabTree } from './WorkspaceTabTree'

vi.mock('@thisbeyond/solid-dnd', () => ({
  createDraggable: () => () => {},
}))

vi.mock('~/components/shell/TabDragContext', () => ({
  SIDEBAR_TAB_PREFIX: 'sidebar-tab:',
}))

vi.mock('~/components/common/AgentProviderIcon', () => ({
  AgentProviderIcon: () => null,
}))

function makeTab(type: TabType, id: string, title?: string) {
  return {
    type,
    id,
    title: title ?? id,
    tileId: 'tile-1',
    position: '0|',
  }
}

describe('workspaceTabTree interactions', () => {
  it('clicking the close button closes without selecting the tab', async () => {
    const onTabClick = vi.fn()
    const onTabClose = vi.fn()
    render(() => (
      <WorkspaceTabTree
        tabs={[makeTab(TabType.AGENT, 'a1', 'Agent 1')]}
        activeTabKey={null}
        onTabClick={onTabClick}
        onTabClose={onTabClose}
        workspaceId="ws-1"
      />
    ))

    await fireEvent.click(screen.getByTestId('workspace-tab-close'))

    expect(onTabClose).toHaveBeenCalledTimes(1)
    expect(onTabClose.mock.calls[0][0]).toMatchObject({ type: TabType.AGENT, id: 'a1' })
    expect(onTabClick).not.toHaveBeenCalled()
  })

  it('middle-clicking a tab row closes the tab', async () => {
    const onTabClose = vi.fn()
    render(() => (
      <WorkspaceTabTree
        tabs={[makeTab(TabType.TERMINAL, 't1', 'Terminal 1')]}
        activeTabKey={null}
        onTabClick={() => {}}
        onTabClose={onTabClose}
        workspaceId="ws-1"
      />
    ))

    const leaf = screen.getByTestId('tab-tree-leaf')
    leaf.dispatchEvent(new MouseEvent('auxclick', { bubbles: true, cancelable: true, button: 1 }))

    expect(onTabClose).toHaveBeenCalledTimes(1)
    expect(onTabClose.mock.calls[0][0]).toMatchObject({ type: TabType.TERMINAL, id: 't1' })
  })

  it('hides close controls for agent and terminal tabs in readOnly mode', () => {
    render(() => (
      <WorkspaceTabTree
        tabs={[
          makeTab(TabType.AGENT, 'a1', 'Agent 1'),
          makeTab(TabType.TERMINAL, 't1', 'Terminal 1'),
        ]}
        activeTabKey={null}
        onTabClick={() => {}}
        readOnly
        workspaceId="ws-1"
      />
    ))

    expect(screen.queryByTestId('workspace-tab-close')).not.toBeInTheDocument()
  })

  it('keeps file tab close control in readOnly mode', () => {
    render(() => (
      <WorkspaceTabTree
        tabs={[makeTab(TabType.FILE, 'f1', 'readme.md')]}
        activeTabKey={null}
        onTabClick={() => {}}
        readOnly
        workspaceId="ws-1"
      />
    ))

    expect(screen.getByTestId('workspace-tab-close')).toBeInTheDocument()
  })

  it('disables the close control while the tab is closing', () => {
    render(() => (
      <WorkspaceTabTree
        tabs={[makeTab(TabType.AGENT, 'a1', 'Agent 1')]}
        activeTabKey={null}
        onTabClick={() => {}}
        onTabClose={() => {}}
        closingTabKeys={new Set([`${TabType.AGENT}:a1`])}
        workspaceId="ws-1"
      />
    ))

    expect(screen.getByTestId('workspace-tab-close')).toBeDisabled()
  })
})
