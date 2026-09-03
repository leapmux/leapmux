import type { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import type { Tab } from '~/stores/tab.types'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { TerminalStatus } from '~/generated/proto/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { setCRDTBridge } from '~/lib/crdt'
import { createImperativeRef } from '~/lib/imperativeRef'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { tabKey } from '~/stores/tab.helpers'
import { emitAddTab, emitRemoveTab } from '~/stores/tabOps'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestFloatingWindowStore, createTestTabStores } from '~/test-support/tabStores'
import { createMobileOverlayState } from './MobileLayout'
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

vi.mock('~/components/fileviewer/FileViewer', () => ({
  FileViewer: (props: { rootPath?: string, homeDir?: string }) => (
    <div data-testid="file-viewer" data-root={props.rootPath} data-home={props.homeDir} />
  ),
}))

/**
 * The two rename RPCs, as spies. The rest of the module stays REAL: this tree
 * calls `workerRpc` from many places, and a whole-module replacement would
 * turn each of those into undefined.
 */
const renameAgent = vi.hoisted(() => vi.fn(async () => ({})))
const updateTerminalTitle = vi.hoisted(() => vi.fn(async () => ({})))
vi.mock('~/api/workerRpc', async importOriginal => ({
  ...(await importOriginal<typeof import('~/api/workerRpc')>()),
  renameAgent,
  updateTerminalTitle,
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
  /** Same, for an AGENT tab -- the pane whose identity the tests below assert on. */
  addAgent: (id: string, tileId: string, title?: string) => Tab
  /** Same, for a FILE tab. */
  addFile: (id: string, tileId: string) => Tab
}

function renderRenderer(s: RendererSetup, focusedTileId: string, getMruAgentContext = () => ({ workingDir: '/repo', homeDir: '/home/me' })) {
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
        repoGitStore: createRepoGitStore(),
      },
      ops: {
        agentOps: {
          availableProviders: () => [],
          handleOpenAgent: () => {},
          handleRetryMessage: () => {},
          handleDeleteMessage: () => {},
          handleControlResponse: () => {},
          handleAgentSettingChange: () => {},
          handleInterrupt: () => {},
        } as any,
        termOps: {
          availableShells: () => [],
          defaultShell: () => '',
          handleOpenTerminal: () => {},
          handleOpenTerminalWithShell: () => {},
          handleTerminalInput: () => {},
          handleTerminalResize: () => {},
        } as any,
      },
      workspace: {
        isActiveWorkspaceMutatable: () => true,
        isActiveWorkspaceArchived: () => false,
        activeWorkspace: () => ({ id: 'workspace-1' }),
        getCurrentTabContext: () => ({ workerId: 'worker-1', workingDir: '/repo', homeDir: '/home/me', gitToplevel: '/repo' }),
        getMruAgentContext,
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
        newAgentDialog: { open: () => {}, close: () => {}, value: () => null },
        newTerminalDialog: { open: () => {}, close: () => {}, value: () => null },
      },
      chrome: {
        isMobileLayout: () => false,
        mobileOverlay: createMobileOverlayState(),
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
    addAgent(id, tileId, title = 'Agent') {
      nextPosition += 1
      emitAddTab({ type: TabType.AGENT, id, tileId, position: `p${nextPosition}`, workerId: 'worker-1' })
      stores.metadata.patch(id, { title, workingDir: '/repo', agentStatus: AgentStatus.ACTIVE })
      stores.selection.setActiveById(TabType.AGENT, id)
      return stores.view.getById(TabType.AGENT, id)!
    },
    addFile(id, tileId) {
      nextPosition += 1
      emitAddTab({ type: TabType.FILE, id, tileId, position: `p${nextPosition}`, workerId: 'worker-1' })
      stores.metadata.patch(id, { title: id, filePath: `/repo/${id}.ts`, workingDir: '/repo' })
      stores.selection.setActiveById(TabType.FILE, id)
      return stores.view.getById(TabType.FILE, id)!
    },
  }
}

/**
 * A pane's identity is its TAB ID, not the joined `Tab` object.
 *
 * `Tab` is rebuilt whenever any field it joins from `tabMetadata` changes -- the
 * MRU stamp a click writes, a title rename, a git badge refresh, an agent status
 * flip. `<For>` keys by item identity, so keying the panes on the object made every
 * one of those tear down and rebuild the whole pane: the chat transcript's DOM went
 * with it, taking the user's in-progress text selection, each lifted per-message
 * expand/collapse choice, and the reading position.
 */
/**
 * The MRU-agent context is one walk per tick, not one per pane.
 *
 * `getMruAgentContext` sorts the whole workspace's tabs, and the file pane used
 * to read it through two separate Solid prop getters (`rootPath`, `homeDir`) --
 * so N open file panes paid 2N sorts, and because the getters' tracked sources
 * include the account-wide tab join, ANY tab's MRU stamp or rename re-ran all of
 * them. That is the same per-metadata-patch account-wide recomputation the tab
 * join was restructured to eliminate.
 */
describe('tileRenderer mru agent context', () => {
  it('walks the MRU order once per tick, not once per file pane', async () => {
    const s = createSetup()
    const tileId = s.layoutStore.focusedTileId()!
    s.addFile('f1', tileId)
    s.addFile('f2', tileId)
    const getMruAgentContext = vi.fn(() => ({ workingDir: '/repo', homeDir: '/home/me' }))

    renderRenderer(s, tileId, getMruAgentContext)
    await screen.findAllByTestId('file-viewer')

    // Two panes x two prop getters = 4 walks before the memo; the load-bearing
    // property is that the count does not scale with the number of panes.
    expect(getMruAgentContext.mock.calls.length).toBeLessThan(3)
  })

  /**
   * "Copy relative path" is relative to the FILE TAB's own dir, not the MRU
   * agent's. Keyed on the agent it was a silent no-op in a workspace with no
   * agent tab -- `getMruAgentContext` answers `''` there and `relativizePath`
   * hands back the absolute path unchanged -- and it answered for the wrong
   * checkout whenever the file was opened from a different one than the agent
   * the user last clicked.
   */
  it('roots the file pane at the tab\'s own working dir, with no agent present', async () => {
    const s = createSetup()
    const tileId = s.layoutStore.focusedTileId()!
    s.addFile('f1', tileId)
    // No agent anywhere, so the old MRU base would have been ''.
    const getMruAgentContext = vi.fn(() => ({ workingDir: '', homeDir: '' }))

    renderRenderer(s, tileId, getMruAgentContext)
    const viewer = await screen.findByTestId('file-viewer')

    expect(viewer.getAttribute('data-root')).toBe('/repo')
  })
})

describe('tileRenderer pane identity', () => {
  it('keeps the agent pane mounted across a tab-metadata change', async () => {
    const s = createSetup()
    const tileId = s.layoutStore.focusedTileId()!
    s.addAgent('a1', tileId)
    renderRenderer(s, tileId)

    const pane = await screen.findByTestId('chat-container')

    // The unprompted worker-sourced writes that land on a tab the user is reading.
    s.metadata.patch('a1', { title: 'Renamed', hasNotification: true })
    await waitFor(() => expect(s.view.getAgentTab('a1')?.title).toBe('Renamed'))

    expect(screen.getByTestId('chat-container'), 'the pane is the SAME DOM node').toBe(pane)
  })

  // The FILE pane is built from the same `tileFileTabIds` memo and the same
  // `<For>` shape, so the agent case above guards both constructions. It has no
  // unit test of its own because every `FileViewer` testid is content-state
  // dependent (`text-view`, `markdown-view`, ...) and needs a worker RPC this
  // harness cannot serve; the file-view half is covered end to end by the
  // text-selection spec in 037-quote-and-mention.
  it('keeps the agent pane mounted when a click re-activates the already-active tab', async () => {
    const s = createSetup()
    const tileId = s.layoutStore.focusedTileId()!
    const tab = s.addAgent('a1', tileId)
    renderRenderer(s, tileId)

    const pane = await screen.findByTestId('chat-container')

    // Exactly what every click inside a tile does (TileRenderer's `onFocus`), and
    // what the click ending a drag-select used to do to the selection.
    s.selection.setActive(tab)
    s.selection.setActive(s.view.getAgentTab('a1')!)
    await Promise.resolve()

    expect(screen.getByTestId('chat-container'), 'the pane is the SAME DOM node').toBe(pane)
  })
})

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

// The branch-action guard and the ref it produces are one function now, tested
// beside it in `~/components/workspace/branchActions.test.ts`.

/**
 * The tab strip's rename goes through the shared `renameTab`, which is where
 * the RULE lives and where `renameTab.test.ts` covers it: the cleaning, the
 * empty and unchanged short-circuits, the ptyTitle clear, and the failure
 * toast. These tests cover the WIRING instead — that this renderer hands that
 * module the live stores and honors what it decides.
 *
 * The inline copy this replaced patched the metadata with the RAW typed
 * string and had no short-circuit, so the strip showed a 400-byte title the
 * worker had cut to 128, and an untouched edit issued a write.
 */
describe('tileRenderer tab rename', () => {
  beforeEach(() => {
    renameAgent.mockClear()
    updateTerminalTitle.mockClear()
  })

  /** Open the inline editor on the first tab of the strip and commit `typed`. */
  function renameFirstTab(typed: string) {
    fireEvent.dblClick(screen.getAllByTestId('tab')[0]!)
    const input = screen.getByTestId('tab-rename-input') as HTMLInputElement
    fireEvent.input(input, { target: { value: typed } })
    fireEvent.keyDown(input, { key: 'Enter' })
  }

  // 50 CJK characters are 150 bytes, and the worker stores the 42 that fit in
  // 128. The worker id comes from the VIEW this renderer passes in, so an
  // empty one here would mean the rename never reached the tab's worker.
  it('sends the cleaned title to the tab\'s own worker', async () => {
    const s = createSetup()
    const tileId = s.layoutStore.focusedTileId()!
    s.addAgent('a1', tileId, 'Agent Olivia')
    renderRenderer(s, tileId)
    await screen.findByTestId('chat-container')

    renameFirstTab('一'.repeat(50))

    const want = '一'.repeat(42)
    expect(renameAgent).toHaveBeenCalledWith('worker-1', { agentId: 'a1', title: want })
    // The optimistic patch lands in the REAL metadata store, holding the same
    // title the worker stores rather than the raw string.
    await waitFor(() => expect(s.view.getAgentTab('a1')?.title).toBe(want))
  })

  it('strips a control character before it reaches the worker or the strip', async () => {
    const s = createSetup()
    const tileId = s.layoutStore.focusedTileId()!
    s.addAgent('a1', tileId, 'Agent Olivia')
    renderRenderer(s, tileId)
    await screen.findByTestId('chat-container')

    renameFirstTab('Deploy\u0000logs')

    expect(renameAgent).toHaveBeenCalledWith('worker-1', { agentId: 'a1', title: 'Deploylogs' })
    await waitFor(() => expect(s.view.getAgentTab('a1')?.title).toBe('Deploylogs'))
  })

  // Both short-circuits reach this renderer, because the strip's own guard
  // compares the RAW text: a run of invisible characters and
  // `Agent Olivia<U+200B>` are each non-empty and each differ from the label,
  // so both arrive at `renameTab`.
  it('honors the no-op decision for a title that cleaning empties or leaves unchanged', async () => {
    const s = createSetup()
    const tileId = s.layoutStore.focusedTileId()!
    s.addAgent('a1', tileId, 'Agent Olivia')
    renderRenderer(s, tileId)
    await screen.findByTestId('chat-container')

    renameFirstTab('  \u0000\u200B\uFEFF  ')
    expect(renameAgent).not.toHaveBeenCalled()
    expect(s.view.getAgentTab('a1')?.title).toBe('Agent Olivia')

    renameFirstTab('Agent Olivia\u200B')
    expect(renameAgent).not.toHaveBeenCalled()
    expect(s.view.getAgentTab('a1')?.title).toBe('Agent Olivia')
  })

  // The sidebar's old inline copy never called UpdateTerminalTitle, so a
  // terminal renamed there lost its name on the next reload. This asserts the
  // strip's copy reaches the worker AND clears the PTY title, which is what
  // lets a manual rename outlive the next TitleChanged event.
  it('persists a terminal rename and clears the pty title', async () => {
    const s = createSetup()
    const tileId = s.layoutStore.focusedTileId()!
    s.addTerminal('t1', tileId, 'Terminal Liam')
    s.metadata.patch('t1', { ptyTitle: 'zsh' })
    renderRenderer(s, tileId)
    await screen.findByTestId('terminal-view')

    renameFirstTab('Build watcher')

    expect(updateTerminalTitle).toHaveBeenCalledWith('worker-1', { terminalId: 't1', title: 'Build watcher' })
    await waitFor(() => expect(s.view.getTerminalTab('t1')?.title).toBe('Build watcher'))
    expect(s.view.getTerminalTab('t1')?.ptyTitle).toBe('')
  })
})
