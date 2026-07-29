import type { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import type { Tab } from '~/stores/tab.types'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { setCRDTBridge } from '~/lib/crdt'
import { createImperativeRef } from '~/lib/imperativeRef'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { createGitFileStatusStore } from '~/stores/gitFileStatus.store'
import { tabKey } from '~/stores/tab.helpers'
import { emitAddTab, emitRemoveTab } from '~/stores/tabOps'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestFloatingWindowStore, createTestTabStores } from '~/test-support/tabStores'
import { mruAgentEditorDeps } from './mruAgentEditorDeps'
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

type RendererSetup = ReturnType<typeof createTestTabStores> & {
  floatingWindowStore: ReturnType<typeof createFloatingWindowStore>
  handleTabClose: ReturnType<typeof vi.fn>
  workspaceId: string
  /**
   * Place a terminal tab through the op path and read the assembled `Tab`
   * back off the join, which is the only shape the renderer ever sees.
   */
  addTerminal: (id: string, tileId: string, title?: string) => Tab
}

function renderRenderer(s: RendererSetup, focusedTileId: string) {
  return render(() => {
    const r = createTileRenderer({
      // Same factory production uses, so the test exercises the real
      // select-and-focus behaviour rather than a bare `setActive`.
      mruEditorDeps: mruAgentEditorDeps({
        view: s.view,
        selection: s.selection,
        layoutStore: s.layoutStore,
        floatingWindowStore: undefined,
        getWorkspaceId: () => s.workspaceId,
      }),
      stores: {
        view: s.view,
        metadata: s.metadata,
        selection: s.selection,
        chatStore: createChatStore(),
        controlStore: createControlStore(),
        layoutStore: s.layoutStore,
        agentSessionStore: createAgentSessionStore(),
        gitFileStatusStore: createGitFileStatusStore(),
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

afterEach(() => setCRDTBridge(null))

let nextPosition = 0

function createSetup(): RendererSetup {
  const harness = installTestBridge()
  const stores = createTestTabStores(harness.workspaceId)
  return {
    ...stores,
    workspaceId: harness.workspaceId,
    floatingWindowStore: createTestFloatingWindowStore(),
    handleTabClose: vi.fn(async (_tab: Tab) => true),
    addTerminal(id, tileId, title = 'Terminal') {
      nextPosition += 1
      emitAddTab({ type: TabType.TERMINAL, id, tileId, position: `p${nextPosition}`, workerId: 'worker-1' })
      stores.metadata.patch(id, {
        title,
        workingDir: '/repo',
        terminalStatus: TerminalStatus.READY,
      })
      stores.selection.setActiveById(TabType.TERMINAL, id)
      return stores.view.getById(TabType.TERMINAL, id)!
    },
  }
}

describe('tileRenderer close-tile flow', () => {
  it('opens the CloseTileDialog when closing a tile that has tabs', async () => {
    const s = createSetup()
    const leftTileId = s.layoutStore.focusedTileId()
    const rightTileId = s.layoutStore.splitTile(leftTileId, 'horizontal')!
    s.addTerminal('term-right', rightTileId)

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
    expect(s.layoutStore.owner().findHeirTile(rightTileId)).toBeTruthy()
    s.addTerminal('term-right', rightTileId)

    renderRenderer(s, rightTileId)

    fireEvent.click(screen.getByTestId('close-tile'))
    await waitFor(() => screen.getByTestId('close-tile-dialog'))
    fireEvent.click(screen.getByTestId('close-tile-move'))

    await waitFor(() => {
      expect(s.layoutStore.getAllTileIds()).not.toContain(rightTileId)
    })
    expect(s.handleTabClose).not.toHaveBeenCalled()

    // The tab survives on the one remaining leaf. Its id is asserted as
    // "whatever is left" rather than the pre-close heir id on purpose: once
    // the split has only one child the projection collapses it, re-keying the
    // surviving leaf to the SPLIT's node id. The heir id read before the close
    // is therefore stale by the time the move lands -- what matters is that the
    // tab moved with it instead of being orphaned on the removed tile.
    const survivors = s.layoutStore.getAllTileIds()
    expect(survivors).toHaveLength(1)
    const moved = s.view.getById(TabType.TERMINAL, 'term-right')
    expect(moved?.tileId).toBe(survivors[0])
    expect(s.view.forTile(rightTileId)).toEqual([])
  })

  /**
   * Closing a tile carries its selection to the heir so "move tabs" lands the
   * user on the tab they were looking at — but ONLY on that tile.
   *
   * `setActiveById` routes through `setActive`, which ALSO claims the
   * workspace-wide pointer. That is wrong here: the close control stops
   * propagation, so the closing tile is never focused on its way out, and the
   * heir is an adjacent sibling unrelated to focus. Handing the workspace
   * pointer to a tab in a background tile badges the tab the user is reading,
   * seeds the next agent from the wrong repo, and is what a reload restores to.
   * The pre-refactor store drew the same line: its `mergeTabsIntoTile` wrote
   * only the per-tile pointer.
   *
   * Asserted on WHICH setter is called rather than on the resulting pointers:
   * in a two-leaf close the heir's id is absorbed when the parent flips back to
   * a LEAF, and the carried tab is momentarily unresolvable — so `setActiveById`
   * happens to no-op and both spellings leave the same state behind. The call is
   * the behaviour; the state is not, in this shape.
   */
  it('carries the closed tile selection with the TILE-scoped setter, not the workspace one', async () => {
    const s = createSetup()
    const preSplitTileId = s.layoutStore.focusedTileId()
    const rightTileId = s.layoutStore.splitTile(preSplitTileId, 'horizontal')!
    s.addTerminal('term-right', rightTileId)
    s.selection.setActiveInTile(s.view.getById(TabType.TERMINAL, 'term-right')!, rightTileId)

    // The workspace pointer BEFORE the merge. It must survive: the user is
    // reading whatever it names, and a tab arriving on a background tile must
    // not badge what they are looking at or seed the next agent from it.
    const workspaceActiveBefore = s.selection.activeKeyForWorkspace(s.workspaceId)

    renderRenderer(s, rightTileId)
    fireEvent.click(screen.getByTestId('close-tile'))
    await waitFor(() => screen.getByTestId('close-tile-dialog'))
    fireEvent.click(screen.getByTestId('close-tile-move'))
    await waitFor(() => {
      expect(s.layoutStore.getAllTileIds()).not.toContain(rightTileId)
    })

    const heirId = s.layoutStore.getAllTileIds()[0]!
    expect(
      s.selection.activeKeyForTile(heirId),
      'the carry lands on the heir TILE',
    ).toBe(tabKey({ type: TabType.TERMINAL, id: 'term-right' }))
    expect(
      s.selection.activeKeyForWorkspace(s.workspaceId),
      'and must never claim the workspace pointer',
    ).toBe(workspaceActiveBefore)
  })

  it('closes tabs and removes the tile when the user confirms "Close all tabs"', async () => {
    const s = createSetup()
    const leftTileId = s.layoutStore.focusedTileId()
    const rightTileId = s.layoutStore.splitTile(leftTileId, 'horizontal')!
    const terminalTab = s.addTerminal('term-right', rightTileId)

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

    const tabA = s.addTerminal('term-a', rightTileId, 'A')
    s.addTerminal('term-b', rightTileId, 'B')
    s.selection.setActiveById(TabType.TERMINAL, tabA.id)

    s.handleTabClose.mockImplementation(async (tab: Tab) => {
      emitRemoveTab(tab.type, tab.id)
      // Mirror `useTabOperations.handleTabClose`: try to auto-dispose if the
      // window is now single-tile-and-empty. Always a no-op here (the window
      // still has the left tile) — but a future regression that flips
      // `removeIfEmpty` semantics to "drop on first empty tile" would
      // surface as a finalize crash.
      s.floatingWindowStore.removeIfEmpty(
        windowId,
        tId => s.view.forTile(tId),
        () => {},
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
    expect(s.view.forTile(rightTileId)).toEqual([])
  })
})
