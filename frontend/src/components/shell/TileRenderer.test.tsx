import type { Tab } from '~/stores/tab.types'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createImperativeRef } from '~/lib/imperativeRef'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import { createLayoutStore } from '~/stores/layout.store'
import { createTabStore } from '~/stores/tab.store'
import { createTileRenderer } from './TileRenderer'

vi.mock('~/context/PreferencesContext', () => ({
  usePreferences: () => ({
    expandAgentThoughts: () => true,
    setExpandAgentThoughts: () => {},
    showHiddenMessages: () => false,
    setShowHiddenMessages: () => {},
  }),
}))

vi.mock('~/components/terminal/TerminalView', () => ({
  TerminalView: (props: { terminals: Array<{ id: string }> }) => (
    <div data-testid="terminal-view">
      {props.terminals.map(t => t.id).join(',')}
    </div>
  ),
  getTerminalInstance: () => undefined,
}))

interface RendererSetup {
  tabStore: ReturnType<typeof createTabStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  floatingWindowStore: ReturnType<typeof createFloatingWindowStore>
  handleTabClose: ReturnType<typeof vi.fn>
}

function renderRenderer(s: RendererSetup, focusedTileId: string) {
  return render(() => {
    const r = createTileRenderer({
      stores: {
        tabStore: s.tabStore,
        chatStore: createChatStore(),
        controlStore: createControlStore(),
        layoutStore: s.layoutStore,
        agentSessionStore: createAgentSessionStore(),
      },
      ops: {
        agentOps: {
          availableProviders: () => [],
          handleOpenAgent: () => {},
          handleRetryMessage: () => {},
          handleDeleteMessage: () => {},
          handleControlResponse: () => {},
          handleAgentSettingChange: () => {},
          handlePermissionModeChange: () => {},
          handleInterrupt: () => {},
        } as any,
        termOps: {
          availableShells: () => [],
          defaultShell: () => '',
          handleOpenTerminal: () => {},
          handleOpenTerminalWithShell: () => {},
          handleTerminalInput: () => {},
          handleTerminalResize: () => {},
          handleTerminalTitleChange: () => {},
          handleTerminalBell: () => {},
        } as any,
      },
      workspace: {
        isActiveWorkspaceMutatable: () => true,
        isActiveWorkspaceArchived: () => false,
        activeWorkspace: () => ({ id: 'workspace-1' }),
        getCurrentTabContext: () => ({ workerId: 'worker-1', workingDir: '/repo', homeDir: '/home/me', gitToplevel: '/repo' }),
        getMruAgentContext: () => ({ workingDir: '/repo', homeDir: '/home/me' }),
      },
      tab: {
        handleTabSelect: () => {},
        handleTabClose: s.handleTabClose as (tab: Tab) => Promise<boolean>,
        setIsTabEditing: () => {},
        closingTabKeys: () => new Set(),
      },
      newTab: {
        newAgentLoadingProvider: () => null,
        newTerminalLoading: () => false,
        newShellLoading: () => false,
        newAgentDialog: { open: () => {}, close: () => {}, isOpen: () => false },
        newTerminalDialog: { open: () => {}, close: () => {}, isOpen: () => false },
      },
      chrome: {
        isMobileLayout: () => false,
        toggleLeftSidebar: () => {},
        toggleRightSidebar: () => {},
      },
      refs: {
        focusEditorRef: createImperativeRef(),
        getScrollStateRef: createImperativeRef(),
        forceScrollToBottomRef: createImperativeRef(),
      },
      floatingWindow: {
        store: s.floatingWindowStore,
      },
      settingsLoading: { loading: () => false } as any,
    })
    return (
      <>
        {r.renderTile(focusedTileId)}
        {r.CloseDialogs()}
      </>
    )
  })
}

function createSetup(): RendererSetup {
  return {
    tabStore: createTabStore(),
    layoutStore: createLayoutStore(),
    floatingWindowStore: createFloatingWindowStore(),
    handleTabClose: vi.fn(async (_tab: Tab) => true),
  }
}

describe('tileRenderer close-tile flow', () => {
  it('opens the CloseTileDialog when closing a tile that has tabs', async () => {
    const s = createSetup()
    const leftTileId = s.layoutStore.focusedTileId()
    const rightTileId = s.layoutStore.splitTile(leftTileId, 'horizontal')!
    const terminalTab: Tab = {
      type: TabType.TERMINAL,
      id: 'term-right',
      title: 'Terminal',
      tileId: rightTileId,
      workerId: 'worker-1',
      workingDir: '/repo',
      status: TerminalStatus.READY,
    }
    s.tabStore.addTab(terminalTab)
    s.tabStore.setActiveTabForTile(rightTileId, TabType.TERMINAL, terminalTab.id)

    renderRenderer(s, rightTileId)

    fireEvent.click(screen.getByTestId('close-tile'))

    await waitFor(() => {
      expect(screen.getByTestId('close-tile-dialog')).toBeInTheDocument()
    })
  })

  it('moves tabs to the heir tile and removes the closed tile when the user picks "Move tabs"', async () => {
    const s = createSetup()
    const preSplitTileId = s.layoutStore.focusedTileId()
    const rightTileId = s.layoutStore.splitTile(preSplitTileId, 'horizontal')!
    // Under the projection-driven CRDT model, splitTile flips the
    // pre-split tile's kind from LEAF to SPLIT in place; the original
    // pre-split id is now a SPLIT (not a leaf), and TWO new leaf ids
    // are minted (childA where original tabs land, childB = rightTileId).
    // The heir of rightTileId is therefore childA, not the pre-split id.
    const heirTileId = s.layoutStore.owner().findHeirTile(rightTileId)
    expect(heirTileId).toBeTruthy()
    const terminalTab: Tab = {
      type: TabType.TERMINAL,
      id: 'term-right',
      title: 'Terminal',
      tileId: rightTileId,
      workerId: 'worker-1',
      workingDir: '/repo',
      status: TerminalStatus.READY,
    }
    s.tabStore.addTab(terminalTab)
    s.tabStore.setActiveTabForTile(rightTileId, TabType.TERMINAL, terminalTab.id)

    renderRenderer(s, rightTileId)

    fireEvent.click(screen.getByTestId('close-tile'))
    await waitFor(() => screen.getByTestId('close-tile-dialog'))
    fireEvent.click(screen.getByTestId('close-tile-move'))

    await waitFor(() => {
      expect(s.layoutStore.getAllTileIds()).not.toContain(rightTileId)
    })
    expect(s.handleTabClose).not.toHaveBeenCalled()
    const moved = s.tabStore.state.tabs.find(t => t.id === terminalTab.id)
    expect(moved?.tileId).toBe(heirTileId)
  })

  it('closes tabs and removes the tile when the user confirms "Close all tabs"', async () => {
    const s = createSetup()
    const leftTileId = s.layoutStore.focusedTileId()
    const rightTileId = s.layoutStore.splitTile(leftTileId, 'horizontal')!
    const terminalTab: Tab = {
      type: TabType.TERMINAL,
      id: 'term-right',
      title: 'Terminal',
      tileId: rightTileId,
      workerId: 'worker-1',
      workingDir: '/repo',
      status: TerminalStatus.READY,
    }
    s.tabStore.addTab(terminalTab)
    s.tabStore.setActiveTabForTile(rightTileId, TabType.TERMINAL, terminalTab.id)

    renderRenderer(s, rightTileId)

    fireEvent.click(screen.getByTestId('close-tile'))
    await waitFor(() => screen.getByTestId('close-tile-dialog'))
    // ConfirmButton needs two clicks.
    const closeAllBtn = screen.getByTestId('close-tile-close-all')
    fireEvent.click(closeAllBtn)
    fireEvent.click(closeAllBtn)

    await waitFor(() => {
      expect(s.handleTabClose).toHaveBeenCalledTimes(1)
      expect(s.layoutStore.getAllTileIds()).not.toContain(rightTileId)
    })
    const closedTab = s.handleTabClose.mock.calls[0]?.[0]
    expect(closedTab).toMatchObject({
      type: TabType.TERMINAL,
      id: terminalTab.id,
      tileId: rightTileId,
    })
  })

  it('removes an empty tile silently with no dialog', async () => {
    const s = createSetup()
    const leftTileId = s.layoutStore.focusedTileId()
    const rightTileId = s.layoutStore.splitTile(leftTileId, 'horizontal')!
    // No tabs on rightTileId.

    renderRenderer(s, rightTileId)

    fireEvent.click(screen.getByTestId('close-tile'))

    await waitFor(() => {
      expect(s.layoutStore.getAllTileIds()).not.toContain(rightTileId)
    })
    expect(screen.queryByTestId('close-tile-dialog')).not.toBeInTheDocument()
    expect(s.handleTabClose).not.toHaveBeenCalled()
  })

  it('predicate updates propagate to a surviving tile when its sibling closes (reactive actions)', async () => {
    // Regression for the prior `actions = buildTileActions(tileId)` snapshot:
    // when a sibling closes and the surviving leaf keeps its identity (the
    // parent split collapses to that leaf via the projection's single-
    // child SPLIT collapse), the survivor's closeMode should flip from
    // 'tile' to 'none'. Without reactive actions the close button would
    // linger on the dead snapshot.
    //
    // Under the projection-driven CRDT model, splitTile flips T's kind
    // LEAF → SPLIT in place; the original T id becomes the SPLIT, with
    // two new leaf children A and B. The "surviving leaf" after closing
    // B is A (whose nodeId we look up via owner.findHeirTile).
    const s = createSetup()
    const preSplitTileId = s.layoutStore.focusedTileId()
    const rightTileId = s.layoutStore.splitTile(preSplitTileId, 'horizontal')!
    const survivorTileId = s.layoutStore.owner().findHeirTile(rightTileId)!

    renderRenderer(s, survivorTileId)

    // multiTile is true → close-tile button is visible on the survivor.
    expect(screen.getByTestId('close-tile')).toBeInTheDocument()

    // Close the sibling. The split collapses to a single leaf and the
    // survivor's closeMode flips from 'tile' to 'none'. The projection's
    // single-child collapse re-keys the rendered leaf to the SPLIT's
    // node_id (preSplitTileId), but the test mounted the Tile keyed on
    // survivorTileId — under the new id mapping the originally-mounted
    // tile is re-keyed to preSplitTileId, so the close button on it
    // disappears via predicate change.
    s.layoutStore.closeTile(rightTileId)

    await waitFor(() => {
      expect(screen.queryByTestId('close-tile')).toBeNull()
    })
  })

  it('close-tile on a multi-tile floating window cleans up cleanly even after the close-all loop fires removeEmptyFloatingWindow per tab', async () => {
    // Regression for the simplification of `closeTileFlow.finalize`: it no
    // longer pre-checks `windowGone` and instead trusts
    // `removeTileFromWindow` to be idempotent against an auto-disposed
    // window. Each per-tab close in the close-all loop calls
    // `removeEmptyFloatingWindow`, which is a no-op on multi-tile windows
    // (the only configuration where close-tile is reachable on a floating
    // window — single-tile windows render `closeMode='none'`). This test
    // pins that no-op behavior end-to-end through the dialog.
    const s = createSetup()
    const created = s.floatingWindowStore.addWindow()
    if (!created)
      throw new Error('addWindow returned null — vitest setup should wire a default CRDT bridge')
    const { windowId, tileId: leftTileId } = created
    const rightTileId = s.floatingWindowStore.splitTile(windowId, leftTileId, 'horizontal')!
    s.layoutStore.setFocusedTile(rightTileId)
    s.floatingWindowStore.setFocusedTile(windowId, rightTileId)

    const tabA: Tab = {
      type: TabType.TERMINAL,
      id: 'term-a',
      title: 'A',
      tileId: rightTileId,
      workerId: 'worker-1',
      workingDir: '/repo',
      status: TerminalStatus.READY,
    }
    const tabB: Tab = { ...tabA, id: 'term-b', title: 'B' }
    s.tabStore.addTab(tabA)
    s.tabStore.addTab(tabB)
    s.tabStore.setActiveTabForTile(rightTileId, TabType.TERMINAL, tabA.id)

    s.handleTabClose.mockImplementation(async (tab: Tab) => {
      s.tabStore.removeTab(tab.type, tab.id)
      // Mirror `useTabOperations.handleTabClose`: try to auto-dispose if the
      // window is now single-tile-and-empty. Always a no-op here (the window
      // still has the left tile) — but a future regression that flips
      // `removeIfEmpty` semantics to "drop on first empty tile" would
      // surface as a finalize crash.
      s.floatingWindowStore.removeIfEmpty(
        windowId,
        tId => s.tabStore.getTabsForTile(tId),
        (removedTileId) => { s.tabStore.cleanupTile(removedTileId) },
      )
      return true
    })

    renderRenderer(s, rightTileId)

    fireEvent.click(screen.getByTestId('close-tile'))
    await waitFor(() => screen.getByTestId('close-tile-dialog'))
    const closeAllBtn = screen.getByTestId('close-tile-close-all')
    fireEvent.click(closeAllBtn)
    fireEvent.click(closeAllBtn)

    await waitFor(() => {
      expect(s.handleTabClose).toHaveBeenCalledTimes(2)
    })
    // Right tile is gone; window survives with only the left tile.
    expect(s.floatingWindowStore.getWindow(windowId)).toBeDefined()
    expect([...s.floatingWindowStore.getWindowTileIdSet(windowId) ?? []]).toEqual([leftTileId])
    expect(s.tabStore.getTabsForTile(rightTileId)).toEqual([])
  })
})
