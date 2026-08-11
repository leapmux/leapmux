import type { Tab } from '~/stores/tab.types'
import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { buildTree, WorkspaceTabTree } from './WorkspaceTabTree'

// Captures the `data` object each row hands solid-dnd, so a test can read it
// back the way `TabDragContext`'s overlay renderer does.
const { draggableData } = vi.hoisted(() => ({ draggableData: [] as { title: string }[] }))

vi.mock('@thisbeyond/solid-dnd', () => ({
  createDraggable: (_id: string, data: { title: string }) => {
    draggableData.push(data)
    return () => {}
  },
}))

vi.mock('~/components/shell/TabDragContext', () => ({
  SIDEBAR_TAB_PREFIX: 'sidebar-tab:',
}))

// Stubbed rather than rendered: the real brand-mark SVGs say nothing a test can
// read without pinning path data. The stub still reports which provider it was
// asked for, so the icon's REACTIVITY is assertable -- an agent tab that reached
// the sidebar before its provider did must pick the icon up, not keep the
// generic bot fallback. `createRenderEffect` (not a JSX child) because a
// `vi.mock` factory is hoisted above this module's Solid template constants.
vi.mock('~/components/common/AgentProviderIcon', async () => {
  const { createRenderEffect } = await import('solid-js')
  return {
    AgentProviderIcon: (props: { provider?: number }) => {
      const el = document.createElement('span')
      el.setAttribute('data-testid', 'agent-provider-icon')
      createRenderEffect(() => el.setAttribute('data-provider', String(props.provider ?? '')))
      return el
    },
  }
})

function makeTab(type: TabType, id: string, title?: string): Tab {
  // The wide `TabType` parameter is narrowed to the union's literal
  // members via the explicit return-type cast; callers pass one of the
  // three discriminants so the runtime value is always a valid variant.
  return {
    type,
    id,
    title: title ?? id,
    workspaceId: 'ws-1',
    tileId: 'tile-1',
    position: '0|',
  } as Tab
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
        tabItemOps={{ onClose: onTabClose }}
        workspaceId="ws-1"
      />
    ))

    await fireEvent.click(screen.getByTestId('workspace-tab-close'))

    expect(onTabClose).toHaveBeenCalledTimes(1)
    expect(onTabClose.mock.calls[0][0]).toMatchObject({ type: TabType.AGENT, id: 'a1', workspaceId: 'ws-1' })
    expect(onTabClick).not.toHaveBeenCalled()
  })

  it('middle-clicking a tab row closes the tab', async () => {
    const onTabClose = vi.fn()
    render(() => (
      <WorkspaceTabTree
        tabs={[makeTab(TabType.TERMINAL, 't1', 'Terminal 1')]}
        activeTabKey={null}
        onTabClick={() => {}}
        tabItemOps={{ onClose: onTabClose }}
        workspaceId="ws-1"
      />
    ))

    const leaf = screen.getByTestId('tab-tree-leaf')
    leaf.dispatchEvent(new MouseEvent('auxclick', { bubbles: true, cancelable: true, button: 1 }))

    expect(onTabClose).toHaveBeenCalledTimes(1)
    expect(onTabClose.mock.calls[0][0]).toMatchObject({ type: TabType.TERMINAL, id: 't1', workspaceId: 'ws-1' })
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
        tabItemOps={{ onClose: () => {}, closingKeys: new Set([`${TabType.AGENT}:a1`]) }}
        workspaceId="ws-1"
      />
    ))

    expect(screen.getByTestId('workspace-tab-close')).toBeDisabled()
  })

  it('renames non-file tabs when tabItemOps.onRename is provided', async () => {
    const onRename = vi.fn()
    render(() => (
      <WorkspaceTabTree
        tabs={[makeTab(TabType.AGENT, 'a1', 'Agent 1')]}
        activeTabKey={null}
        onTabClick={() => {}}
        tabItemOps={{ onRename }}
        workspaceId="ws-1"
      />
    ))

    await fireEvent.dblClick(screen.getByTestId('tab-tree-leaf'))
    const input = screen.getByDisplayValue('Agent 1')
    await fireEvent.input(input, { target: { value: 'Renamed Agent' } })
    await fireEvent.keyDown(input, { key: 'Enter' })

    expect(onRename).toHaveBeenCalledTimes(1)
    expect(onRename).toHaveBeenCalledWith(expect.objectContaining({ type: TabType.AGENT, id: 'a1', workspaceId: 'ws-1' }), 'Renamed Agent')
  })

  it('does not enter rename mode without tabItemOps.onRename', async () => {
    render(() => (
      <WorkspaceTabTree
        tabs={[makeTab(TabType.AGENT, 'a1', 'Agent 1')]}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
      />
    ))

    await fireEvent.dblClick(screen.getByTestId('tab-tree-leaf'))

    expect(screen.queryByDisplayValue('Agent 1')).not.toBeInTheDocument()
  })

  it('keeps file tabs non-renamable even when onRename is provided', async () => {
    render(() => (
      <WorkspaceTabTree
        tabs={[makeTab(TabType.FILE, 'f1', 'readme.md')]}
        activeTabKey={null}
        onTabClick={() => {}}
        tabItemOps={{ onRename: vi.fn() }}
        workspaceId="ws-1"
      />
    ))

    await fireEvent.dblClick(screen.getByTestId('tab-tree-leaf'))

    expect(screen.queryByDisplayValue('readme.md')).not.toBeInTheDocument()
  })

  // ----- BranchContextMenu integration -----------------------------------

  function gitTab(id: string): Tab {
    return {
      type: TabType.AGENT,
      workspaceId: 'ws-1',
      id,
      title: id,
      tileId: 'tile-1',
      position: '0',
      workerId: 'w1',
      gitOriginUrl: 'https://github.com/o/r.git',
      gitBranch: 'feature',
      gitToplevel: '/home/user/Workspaces/r',
    } as Tab
  }

  it('opens the branch menu and fires onChangeBranch with the row identity', async () => {
    const onChangeBranch = vi.fn()
    const onDeleteBranch = vi.fn()
    render(() => (
      <WorkspaceTabTree
        tabs={[gitTab('a1')]}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
        onChangeBranch={onChangeBranch}
        onDeleteBranch={onDeleteBranch}
      />
    ))

    // Branch row's icon button is the only button inside the row.
    const branchRow = screen.getByTestId('tab-tree-branch-group')
    const trigger = branchRow.querySelector('button') as HTMLButtonElement
    await fireEvent.click(trigger)

    await fireEvent.click(screen.getByText('Change branch...'))
    expect(onChangeBranch).toHaveBeenCalledTimes(1)
    expect(onChangeBranch.mock.calls[0][0]).toMatchObject({
      workspaceId: 'ws-1',
      workerId: 'w1',
      gitToplevel: '/home/user/Workspaces/r',
      branchName: 'feature',
    })
    expect(onDeleteBranch).not.toHaveBeenCalled()
  })

  it('fires onDeleteBranch with the tabs in the branch group', async () => {
    const onChangeBranch = vi.fn()
    const onDeleteBranch = vi.fn()
    render(() => (
      <WorkspaceTabTree
        tabs={[gitTab('a1'), gitTab('a2')]}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
        onChangeBranch={onChangeBranch}
        onDeleteBranch={onDeleteBranch}
      />
    ))

    const branchRow = screen.getByTestId('tab-tree-branch-group')
    await fireEvent.click(branchRow.querySelector('button') as HTMLButtonElement)
    await fireEvent.click(screen.getByText('Delete branch...'))

    expect(onDeleteBranch).toHaveBeenCalledTimes(1)
    const ref = onDeleteBranch.mock.calls[0][0]
    expect(ref).toMatchObject({
      workerId: 'w1',
      gitToplevel: '/home/user/Workspaces/r',
      branchName: 'feature',
    })
    expect(ref.tabs.map((t: Tab) => t.id).toSorted()).toEqual(['a1', 'a2'])
    expect(onChangeBranch).not.toHaveBeenCalled()
  })

  it('disables both branch actions when the row\'s worker is known offline', async () => {
    const onChangeBranch = vi.fn()
    const onDeleteBranch = vi.fn()
    render(() => (
      <WorkspaceTabTree
        tabs={[gitTab('a1')]}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
        isWorkerKnownOnline={() => false}
        onChangeBranch={onChangeBranch}
        onDeleteBranch={onDeleteBranch}
      />
    ))

    const branchRow = screen.getByTestId('tab-tree-branch-group')
    await fireEvent.click(branchRow.querySelector('button') as HTMLButtonElement)

    // Disabled, not hidden: the row keeps its menu so the absence of the
    // action is explained rather than looking like a missing feature.
    const change = screen.getByText('Change branch...') as HTMLButtonElement
    const del = screen.getByText('Delete branch...') as HTMLButtonElement
    expect(change.disabled).toBe(true)
    expect(del.disabled).toBe(true)
    expect(change.title).toContain('offline')
    expect(del.title).toContain('offline')

    // And a click cannot get through to the dialog.
    await fireEvent.click(change)
    await fireEvent.click(del)
    expect(onChangeBranch).not.toHaveBeenCalled()
    expect(onDeleteBranch).not.toHaveBeenCalled()
  })

  it('leaves both branch actions enabled when the row\'s worker is online', async () => {
    const onChangeBranch = vi.fn()
    render(() => (
      <WorkspaceTabTree
        tabs={[gitTab('a1')]}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
        isWorkerKnownOnline={workerId => workerId === 'w1'}
        onChangeBranch={onChangeBranch}
        onDeleteBranch={() => {}}
      />
    ))

    const branchRow = screen.getByTestId('tab-tree-branch-group')
    await fireEvent.click(branchRow.querySelector('button') as HTMLButtonElement)

    const change = screen.getByText('Change branch...') as HTMLButtonElement
    expect(change.disabled).toBe(false)
    expect(change.title).toBe('')
    await fireEvent.click(change)
    expect(onChangeBranch).toHaveBeenCalledTimes(1)
  })

  /**
   * Fail OPEN, not closed. The accessor is optional, and the Worker list it
   * reads is empty on first paint, so "no answer" must not read as "offline" --
   * greying out a working action is worse than letting one fail with an error
   * the user can act on.
   */
  it('leaves the branch actions enabled when worker liveness is unknown', async () => {
    const onChangeBranch = vi.fn()
    render(() => (
      <WorkspaceTabTree
        tabs={[gitTab('a1')]}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
        onChangeBranch={onChangeBranch}
        onDeleteBranch={() => {}}
      />
    ))

    const branchRow = screen.getByTestId('tab-tree-branch-group')
    await fireEvent.click(branchRow.querySelector('button') as HTMLButtonElement)
    await fireEvent.click(screen.getByText('Change branch...'))
    expect(onChangeBranch).toHaveBeenCalledTimes(1)
  })

  /**
   * The gate has to track, not just read once. Its whole premise is that the
   * menu renders from the last Worker state the Hub pushed, so when a
   * WORKERS_CHANGED frame flips a Worker back online the already-open menu must
   * become usable without a remount.
   */
  it('re-enables the branch actions when the worker comes back online', async () => {
    const [online, setOnline] = createSignal(false)
    const onChangeBranch = vi.fn()
    render(() => (
      <WorkspaceTabTree
        tabs={[gitTab('a1')]}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
        isWorkerKnownOnline={() => online()}
        onChangeBranch={onChangeBranch}
        onDeleteBranch={() => {}}
      />
    ))

    const branchRow = screen.getByTestId('tab-tree-branch-group')
    await fireEvent.click(branchRow.querySelector('button') as HTMLButtonElement)
    expect((screen.getByText('Change branch...') as HTMLButtonElement).disabled).toBe(true)

    setOnline(true)
    expect((screen.getByText('Change branch...') as HTMLButtonElement).disabled).toBe(false)
    await fireEvent.click(screen.getByText('Change branch...'))
    expect(onChangeBranch).toHaveBeenCalledTimes(1)
  })

  /**
   * Gating is per branch row, not per tree. One offline Worker must not disable
   * the actions on a row hosted by a different, reachable one -- a real shape,
   * since a workspace's tabs can be spread across machines.
   */
  it('gates each branch row on its own worker', async () => {
    const offlineTab = { ...gitTab('a2'), workerId: 'w2', gitToplevel: '/home/user/Workspaces/other' } as Tab
    render(() => (
      <WorkspaceTabTree
        tabs={[gitTab('a1'), offlineTab]}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
        isWorkerKnownOnline={workerId => workerId === 'w1'}
        onChangeBranch={() => {}}
        onDeleteBranch={() => {}}
      />
    ))

    const rows = screen.getAllByTestId('tab-tree-branch-group')
    expect(rows).toHaveLength(2)

    // Identified by BRANCH LABEL, not by position, and asserted per row rather
    // than by counting. Counting "one disabled, one enabled" passes an INVERTED
    // gate too, which is the whole property this test claims to check. The
    // earlier `items.find(...) ?? items[0]` fallback made it worse: a DOM change
    // that broke containment silently sampled the first row twice, and the
    // counts still held.
    const disabledByBranch = new Map<string, boolean>()
    for (const row of rows) {
      await fireEvent.click(row.querySelector('button') as HTMLButtonElement)
      const items = screen.getAllByText('Change branch...') as HTMLButtonElement[]
      const own = items.find(i => row.contains(i))
      expect(own, 'each row must render its own menu item').toBeTruthy()
      disabledByBranch.set(row.textContent ?? '', own!.disabled)
    }

    const entries = [...disabledByBranch.entries()]
    expect(entries).toHaveLength(2)
    // w1 is online, w2 is not, so the row whose worktree path names "other"
    // (the w2 tab) is the one that must be gated.
    const offlineRow = entries.find(([label]) => label.includes('other'))
    const onlineRow = entries.find(([label]) => !label.includes('other'))
    expect(offlineRow?.[1], 'the row on the OFFLINE worker must be disabled').toBe(true)
    expect(onlineRow?.[1], 'the row on the ONLINE worker must stay enabled').toBe(false)
  })

  it('hides the branch menu when readOnly is true', () => {
    render(() => (
      <WorkspaceTabTree
        tabs={[gitTab('a1')]}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
        readOnly
        onChangeBranch={vi.fn()}
        onDeleteBranch={vi.fn()}
      />
    ))
    const branchRow = screen.getByTestId('tab-tree-branch-group')
    // No buttons in the row.
    expect(branchRow.querySelector('button')).toBeNull()
  })

  it('hides the branch menu when no menu callbacks are supplied', () => {
    render(() => (
      <WorkspaceTabTree
        tabs={[gitTab('a1')]}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
      />
    ))
    const branchRow = screen.getByTestId('tab-tree-branch-group')
    expect(branchRow.querySelector('button')).toBeNull()
  })

  it('hides the branch menu when only onChangeBranch is supplied', () => {
    // BranchContextMenu renders both items unconditionally — gating the
    // wrapper Show on `onChangeBranch && onDeleteBranch` is what makes
    // a partial-callback caller a no-show rather than a half-broken
    // menu where one item silently no-ops.
    render(() => (
      <WorkspaceTabTree
        tabs={[gitTab('a1')]}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
        onChangeBranch={vi.fn()}
      />
    ))
    const branchRow = screen.getByTestId('tab-tree-branch-group')
    expect(branchRow.querySelector('button')).toBeNull()
  })

  it('hides the branch menu when only onDeleteBranch is supplied', () => {
    render(() => (
      <WorkspaceTabTree
        tabs={[gitTab('a1')]}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
        onDeleteBranch={vi.fn()}
      />
    ))
    const branchRow = screen.getByTestId('tab-tree-branch-group')
    expect(branchRow.querySelector('button')).toBeNull()
  })

  it('passes the unified BranchRef shape (workspaceId + tabs + isWorktree) to both handlers', async () => {
    // Pin the BranchRef unification contract: both onChangeBranch and
    // onDeleteBranch receive the same full shape. AppShell forwards only
    // the fields each dialog state actually needs, so the ref must carry
    // `workspaceId` (Change's requirement), `tabs` (Delete's requirement),
    // AND `isWorktree` (ChangeBranchDialog reads this to seed its
    // path-info shape) regardless of which handler fired.
    const onChangeBranch = vi.fn()
    const onDeleteBranch = vi.fn()
    render(() => (
      <WorkspaceTabTree
        tabs={[gitTab('a1'), gitTab('a2')]}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
        onChangeBranch={onChangeBranch}
        onDeleteBranch={onDeleteBranch}
      />
    ))

    const branchRow = screen.getByTestId('tab-tree-branch-group')
    await fireEvent.click(branchRow.querySelector('button') as HTMLButtonElement)
    await fireEvent.click(screen.getByText('Change branch...'))
    const changeRef = onChangeBranch.mock.calls[0][0]
    expect(changeRef.workspaceId).toBe('ws-1')
    expect(changeRef.tabs.map((t: Tab) => t.id).toSorted()).toEqual(['a1', 'a2'])
    expect(changeRef.isWorktree).toBe(false)

    await fireEvent.click(branchRow.querySelector('button') as HTMLButtonElement)
    await fireEvent.click(screen.getByText('Delete branch...'))
    const deleteRef = onDeleteBranch.mock.calls[0][0]
    expect(deleteRef.workspaceId).toBe('ws-1')
    expect(deleteRef.tabs.map((t: Tab) => t.id).toSorted()).toEqual(['a1', 'a2'])
    expect(deleteRef.isWorktree).toBe(false)
  })

  it('propagates gitIsWorktree from tab fields onto the BranchRef', async () => {
    // The whole point of plumbing gitIsWorktree onto Tab is so the
    // branch-row context menu can hand the disposition to
    // ChangeBranchDialog (it seeds isRepoRoot/isWorktreeRoot pre-RPC).
    // Use a worktree tab (gitIsWorktree=true) and verify the ref
    // carries it.
    const wtTab: Tab = {
      ...gitTab('wt-a1'),
      gitIsWorktree: true,
    } as Tab
    const onDeleteBranch = vi.fn()
    render(() => (
      <WorkspaceTabTree
        tabs={[wtTab]}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
        onChangeBranch={vi.fn()}
        onDeleteBranch={onDeleteBranch}
      />
    ))
    const branchRow = screen.getByTestId('tab-tree-branch-group')
    await fireEvent.click(branchRow.querySelector('button') as HTMLButtonElement)
    await fireEvent.click(screen.getByText('Delete branch...'))
    expect(onDeleteBranch.mock.calls[0][0].isWorktree).toBe(true)
  })

  // ----- Per-row DropdownMenu mount invariants --------------------------

  /**
   * Distinct gitTab variant: each call produces a tab in a separate
   * branch group inside the same repo (same gitOriginUrl + workerId,
   * different gitBranch + gitToplevel). buildTree groups by
   * (branchName, workerId, gitToplevel), so two distinct branches yield
   * two branch rows under one repo header.
   */
  function gitTabOnBranch(id: string, branchName: string): Tab {
    return {
      type: TabType.AGENT,
      workspaceId: 'ws-1',
      id,
      title: id,
      tileId: 'tile-1',
      position: '0',
      workerId: 'w1',
      gitOriginUrl: 'https://github.com/o/r.git',
      gitBranch: branchName,
      gitToplevel: `/home/user/Workspaces/r-${branchName}`,
    } as Tab
  }

  it('mounts one BranchContextMenu per branch row', () => {
    // Each branch row owns its own DropdownMenu, so N rows = N menu
    // instances. The trade-off vs. the prior hoisted-singleton design:
    // a handful of extra <menu popover> elements (one per row, empty
    // markup when closed) in exchange for no shared menuRow signal,
    // no controlled-overlay API on BranchContextMenu, and no custom
    // toggle dance per row.
    render(() => (
      <WorkspaceTabTree
        tabs={[
          gitTabOnBranch('a1', 'feature-1'),
          gitTabOnBranch('a2', 'feature-2'),
          gitTabOnBranch('a3', 'feature-3'),
          gitTabOnBranch('a4', 'feature-4'),
        ]}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
        onChangeBranch={vi.fn()}
        onDeleteBranch={vi.fn()}
      />
    ))
    expect(screen.getAllByTestId('tab-tree-branch-group')).toHaveLength(4)
    expect(screen.getAllByText('Change branch...')).toHaveLength(4)
    expect(screen.getAllByText('Delete branch...')).toHaveLength(4)
    expect(document.querySelectorAll('menu[popover]')).toHaveLength(4)
  })

  it('does not mount a row menu when neither callback is supplied', () => {
    // The per-row <Show when={!readOnly && (onChangeBranch || onDeleteBranch)}>
    // gate keeps the BranchContextMenu out of the DOM when neither
    // action is wired.
    render(() => (
      <WorkspaceTabTree
        tabs={[gitTabOnBranch('a1', 'feature-1')]}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
      />
    ))
    expect(document.querySelectorAll('menu[popover]')).toHaveLength(0)
    expect(screen.queryByText('Change branch...')).toBeNull()
  })

  it('hides the row menu on the synthetic "(no branch)" group (detached HEAD)', () => {
    // The "(no branch)" bucket has branchName=null. Both Change and
    // Delete actions would fail at the worker — InspectBranchDeletion
    // returns the short SHA as the branch label, then DeleteBranch
    // tries `git branch -D <short-sha>` and git refuses. Gate the menu
    // out so the user never sees an action that's guaranteed to error.
    const detachedTab: Tab = {
      $typeName: 'leapmux.v1.Tab',
      type: TabType.TERMINAL,
      id: 't-detached',
      title: 'detached',
      workspaceId: 'ws-1',
      tileId: 'tile-1',
      position: '0',
      workerId: 'w1',
      gitOriginUrl: '',
      gitBranch: '', // detached HEAD: no branch name on the tab
      gitToplevel: '/home/user/Workspaces/r',
    } as Tab
    render(() => (
      <WorkspaceTabTree
        tabs={[detachedTab]}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
        onChangeBranch={vi.fn()}
        onDeleteBranch={vi.fn()}
      />
    ))
    expect(screen.getAllByTestId('tab-tree-branch-group')).toHaveLength(1)
    expect(document.querySelectorAll('menu[popover]')).toHaveLength(0)
    expect(screen.queryByText('Change branch...')).toBeNull()
    expect(screen.queryByText('Delete branch...')).toBeNull()
  })

  it('does not mount any row menu in readOnly mode', () => {
    render(() => (
      <WorkspaceTabTree
        tabs={[gitTabOnBranch('a1', 'feature-1'), gitTabOnBranch('a2', 'feature-2')]}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
        readOnly
        onChangeBranch={vi.fn()}
        onDeleteBranch={vi.fn()}
      />
    ))
    expect(document.querySelectorAll('menu[popover]')).toHaveLength(0)
  })

  it('dispatches with each row’s own identity (closure capture, not shared state)', async () => {
    // Per-row menus close over their row's branch data via the <For>
    // loop's closure. Picking the same action from different rows must
    // dispatch with that row's gitToplevel — no shared menuRow signal
    // to misroute across rows.
    const onChangeBranch = vi.fn()
    render(() => (
      <WorkspaceTabTree
        tabs={[
          gitTabOnBranch('a1', 'feature-1'),
          gitTabOnBranch('a2', 'feature-2'),
        ]}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
        onChangeBranch={onChangeBranch}
        onDeleteBranch={vi.fn()}
      />
    ))
    const [rowA, rowB] = screen.getAllByTestId('tab-tree-branch-group')
    const dropdownA = rowA.querySelector('ot-dropdown') as HTMLElement
    const dropdownB = rowB.querySelector('ot-dropdown') as HTMLElement

    await fireEvent.click(within(dropdownB).getByRole('button'))
    await fireEvent.click(within(dropdownB).getByText('Change branch...'))

    expect(onChangeBranch).toHaveBeenCalledTimes(1)
    expect(onChangeBranch.mock.calls[0][0]).toMatchObject({
      workspaceId: 'ws-1',
      workerId: 'w1',
      gitToplevel: '/home/user/Workspaces/r-feature-2',
      branchName: 'feature-2',
    })

    // And row A's menu items, untouched, never fire B's handler.
    await fireEvent.click(within(dropdownA).getByRole('button'))
    await fireEvent.click(within(dropdownA).getByText('Change branch...'))
    expect(onChangeBranch).toHaveBeenCalledTimes(2)
    expect(onChangeBranch.mock.calls[1][0]).toMatchObject({
      gitToplevel: '/home/user/Workspaces/r-feature-1',
      branchName: 'feature-1',
    })
  })

  // ----- Branch collapse-key independence --------------------------------

  /**
   * Two branch groups under the same repo whose composite keys would
   * collide under the legacy colon-joined format
   * (`${repoKey}:${branchName}:${workerId}:${gitToplevel}`):
   *   A: workerId="a:b", gitToplevel="/p"   → suffix `feature:a:b:/p`
   *   B: workerId="a",   gitToplevel="b:/p" → suffix `feature:a:b:/p`
   * Branch names can't contain ':' (gitutil rejects it), but worker ids
   * and POSIX paths can. The null-byte composite key keeps the two
   * groups independent — collapsing one must not toggle the other.
   */
  function collisionPairTabs(): [Tab, Tab] {
    const base = {
      type: TabType.AGENT,
      workspaceId: 'ws-1',
      title: 'agent',
      tileId: 'tile-1',
      position: '0',
      gitOriginUrl: 'https://github.com/o/r.git',
      gitBranch: 'feature',
    }
    return [
      { ...base, id: 'a1', workerId: 'a:b', gitToplevel: '/p' } as Tab,
      { ...base, id: 'a2', workerId: 'a', gitToplevel: 'b:/p' } as Tab,
    ]
  }

  // ----- Row identity stability across tab updates -----------------------

  // Both branches live under one repo so the test exercises the inner
  // (branch) For's reconciliation; one tab per branch keeps the per-row
  // assertion uncluttered.
  function gitTabWithBranch(id: string, branch: string, diffAdded = 0): Tab {
    return {
      type: TabType.AGENT,
      workspaceId: 'ws-1',
      id,
      title: id,
      tileId: 'tile-1',
      position: '0',
      workerId: 'w1',
      gitOriginUrl: 'https://github.com/o/r.git',
      gitBranch: branch,
      gitToplevel: '/repo',
      gitDiffAdded: diffAdded,
    } as Tab
  }

  it('reuses unaffected branch and tab DOM when one tab\'s git fields update', async () => {
    // Regression guard for the stable-key restructure: outer For keys by
    // repoKey strings and inner For keys by composite branch-key strings,
    // so a fresh Tab object for one branch must not remount the sibling
    // branch's row, the repo row, or the unaffected tab leaf.
    const [tabs, setTabs] = createSignal<Tab[]>([
      gitTabWithBranch('a1', 'main'),
      gitTabWithBranch('a2', 'feature'),
    ])
    render(() => (
      <WorkspaceTabTree
        tabs={tabs()}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
      />
    ))

    const branchRows = screen.getAllByTestId('tab-tree-branch-group')
    expect(branchRows).toHaveLength(2)
    // Order is sorted by branch name: "feature" before "main".
    const [featureBefore, mainBefore] = branchRows
    const repoRowBefore = screen.getByTestId('tab-tree-repo-group')
    const tabLeavesBefore = screen.getAllByTestId('tab-tree-leaf')
    expect(tabLeavesBefore).toHaveLength(2)

    // Push a fresh Tab object for the "main" branch with an updated diff
    // stat — this mimics a WatchEvents push that replaces one tab's
    // reference while every other tab keeps its identity.
    setTabs(prev => [
      gitTabWithBranch('a1', 'main', 5),
      prev[1], // same reference as before
    ])

    const branchRowsAfter = screen.getAllByTestId('tab-tree-branch-group')
    expect(branchRowsAfter).toHaveLength(2)
    const [featureAfter, mainAfter] = branchRowsAfter

    // Repo row is reused — only its stats memo re-runs.
    expect(screen.getByTestId('tab-tree-repo-group')).toBe(repoRowBefore)
    // The unchanged branch row keeps its DOM identity.
    expect(featureAfter).toBe(featureBefore)
    // The affected branch row may keep its DOM identity too (its stable
    // string key matched), but its stats memo will have re-run. We don't
    // assert remount-vs-reuse here — only that the sibling stayed.
    expect(mainAfter).toBe(mainBefore)

    // The unaffected tab leaf (a2, in the feature branch) keeps its DOM
    // identity since its Tab reference didn't change.
    const featureLeafBefore = tabLeavesBefore.find(el => el.getAttribute('data-tab-id') === 'a2')
    const tabLeavesAfter = screen.getAllByTestId('tab-tree-leaf')
    expect(tabLeavesAfter).toHaveLength(2)
    const featureLeafAfter = tabLeavesAfter.find(el => el.getAttribute('data-tab-id') === 'a2')
    expect(featureLeafBefore).toBeDefined()
    expect(featureLeafAfter).toBe(featureLeafBefore)
  })

  it('keeps unrelated repo group DOM mounted when a tab in another repo updates', async () => {
    // Two repos, one tab each. Updating a tab in repo A must not
    // disturb repo B's row identity — the outer For keys by repoKey
    // strings so unrelated rows stay mounted across rebuilds.
    function repoTab(id: string, originUrl: string): Tab {
      return {
        type: TabType.AGENT,
        workspaceId: 'ws-1',
        id,
        title: id,
        tileId: 'tile-1',
        position: '0',
        workerId: 'w1',
        gitOriginUrl: originUrl,
        gitBranch: 'main',
        gitToplevel: `/repos/${id}`,
      } as Tab
    }

    const [tabs, setTabs] = createSignal<Tab[]>([
      repoTab('a1', 'https://github.com/o/alpha.git'),
      repoTab('b1', 'https://github.com/o/beta.git'),
    ])
    render(() => (
      <WorkspaceTabTree
        tabs={tabs()}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
      />
    ))

    const repoRowsBefore = screen.getAllByTestId('tab-tree-repo-group')
    expect(repoRowsBefore).toHaveLength(2)
    const [alphaBefore, betaBefore] = repoRowsBefore

    // Replace alpha's tab reference (e.g. its gitDiffAdded changed);
    // beta's tab keeps its identity.
    setTabs(prev => [
      { ...prev[0], gitDiffAdded: 7 } as Tab,
      prev[1],
    ])

    const repoRowsAfter = screen.getAllByTestId('tab-tree-repo-group')
    expect(repoRowsAfter).toHaveLength(2)
    const [alphaAfter, betaAfter] = repoRowsAfter
    expect(alphaAfter).toBe(alphaBefore)
    expect(betaAfter).toBe(betaBefore)
  })

  // ----- Fingerprint short-circuit ---------------------------------------

  /**
   * The inner `tree()` memo is gated by a fingerprint over the tree-
   * relevant tab fields. A WatchEvents push that mutates a non-tree
   * field (e.g. `title`) must NOT cause buildTree to rerun — verified
   * indirectly by keeping the rendered DOM nodes stable: Solid's `<For>`
   * keyed reconciliation preserves the same element when its parent
   * memo returns the same reference, so if buildTree had rerun the
   * branch row's DOM node would be a fresh element.
   *
   * Asserted together with the label update it must not cost, because the two
   * are one property: skipping the rebuild is only correct while the rows still
   * resolve their tab LIVE. Pinning the short-circuit alone is what let the
   * leaves render a frozen `Tab` — see the live-row block below.
   */
  it('does not re-reconcile branch rows when only non-tree fields change', async () => {
    const initial: Tab = {
      type: TabType.AGENT,
      workspaceId: 'ws-1',
      id: 'a1',
      title: 'Agent original',
      tileId: 'tile-1',
      position: '0|',
      workerId: 'w-1',
      gitToplevel: '/repo',
      gitOriginUrl: 'https://github.com/o/r.git',
      gitBranch: 'main',
    }
    const [tabs, setTabs] = createSignal<Tab[]>([initial])

    render(() => (
      <WorkspaceTabTree
        tabs={tabs()}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
      />
    ))

    const branchRowBefore = screen.getByTestId('tab-tree-branch-group')

    // Replace the tab list with a NEW array containing a NEW tab object
    // that only differs in `title`. Tree fields (workerId/branch/diff
    // counters/etc.) are unchanged so the fingerprint is identical.
    setTabs([{ ...initial, title: 'Agent renamed' }])

    const branchRowAfter = screen.getByTestId('tab-tree-branch-group')
    // Same DOM node ⇒ no reconciliation ⇒ buildTree's fingerprint gate
    // skipped the rerun.
    expect(branchRowAfter).toBe(branchRowBefore)
    // …and the skipped rebuild cost the leaf nothing.
    expect(screen.getByTestId('tab-tree-leaf').textContent).toContain('Agent renamed')
  })

  /**
   * Companion to the no-op test: a real tree-field change (here a diff
   * stat) must propagate. Verifies the fingerprint includes diff
   * counters — otherwise the stats badge in the sidebar would stay
   * stale.
   */
  it('rebuilds the branch group when a tree-relevant field (diffAdded) changes', () => {
    const base: Tab = {
      type: TabType.AGENT,
      workspaceId: 'ws-1',
      id: 'a1',
      title: 'Agent',
      tileId: 'tile-1',
      position: '0|',
      workerId: 'w-1',
      gitToplevel: '/repo',
      gitOriginUrl: 'https://github.com/o/r.git',
      gitBranch: 'main',
      gitDiffAdded: 0,
      gitDiffDeleted: 0,
      gitDiffUntracked: 0,
    }
    const before = buildTree([base])
    const after = buildTree([{ ...base, gitDiffAdded: 5 }])
    expect(before.groups[0].branches[0].diffAdded).toBe(0)
    expect(after.groups[0].branches[0].diffAdded).toBe(5)
  })

  // Regression: the inner / outer For row bodies used to do
  // `props.group().branchByKey.get(bKey)!` and
  // `groupByKey().get(repoKey)!` non-null assertions, so a tabs array
  // that re-emits with a different key set could let a row's memo read
  // through `undefined` until reconciliation finished. The reactive
  // signal driving the rebuild here mirrors the WatchEvents push that
  // empties (or repopulates) a branch group.
  it('survives a tabs swap that empties every branch group without crashing', () => {
    const before: Tab[] = [{
      type: TabType.AGENT,
      workspaceId: 'ws-1',
      id: 'a1',
      title: 'Agent',
      tileId: 'tile-1',
      position: '0|',
      workerId: 'w-1',
      gitToplevel: '/repo',
      gitOriginUrl: 'https://github.com/o/r.git',
      gitBranch: 'main',
    } as Tab]
    const [tabs, setTabs] = createSignal<Tab[]>(before)
    render(() => (
      <WorkspaceTabTree
        tabs={tabs()}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
      />
    ))
    // Pre-condition: the branch group renders.
    expect(screen.getAllByTestId('tab-tree-branch-group').length).toBe(1)
    // Swap to an empty tabs array — every group disappears in lock-step
    // with the keys list. With the non-null assertion this re-render
    // would TypeError out of the row body; the Show guard drops the row
    // cleanly instead.
    setTabs([])
    expect(screen.queryAllByTestId('tab-tree-branch-group').length).toBe(0)
    expect(screen.queryAllByTestId('tab-tree-repo-group').length).toBe(0)
  })

  // ----- Live rows behind the cached tree --------------------------------

  /**
   * Regression, and the reason the fingerprint short-circuit above needs a
   * companion here. `buildTree` caches the `Tab` OBJECTS in its branch buckets,
   * and the leaf rows used to resolve through a lookup built from those same
   * cached arrays. Every field the fingerprint omits — title, agent provider,
   * terminal status, PTY title, progress — was therefore frozen at whatever it
   * held when the tree last rebuilt.
   *
   * The user-visible shape: a tab that reaches the sidebar before its worker
   * metadata (a peer client's tab, a `leapmux control tab open`, a cold reload,
   * or a hydration reply landing after a status push already settled the git
   * fields) rendered as the bare "Agent" label with the generic bot icon, and
   * stayed that way until an unrelated tab forced a rebuild.
   *
   * `gitTab` fixtures throughout: a tab with git fields is the case that goes
   * wrong, because the fingerprint has already settled before hydration lands.
   */
  describe('resolves each row against the live tab list', () => {
    const bareAgent: Tab = {
      type: TabType.AGENT,
      workspaceId: 'ws-1',
      id: 'a1',
      tileId: 'tile-1',
      position: '0|',
      workerId: 'w-1',
      gitToplevel: '/repo',
      gitOriginUrl: 'https://github.com/o/r.git',
      gitBranch: 'main',
    } as Tab

    function renderTabs(initial: Tab[]) {
      const [tabs, setTabs] = createSignal<Tab[]>(initial)
      render(() => (
        <WorkspaceTabTree
          tabs={tabs()}
          activeTabKey={null}
          onTabClick={() => {}}
          workspaceId="ws-1"
        />
      ))
      return setTabs
    }

    it('picks up a title that arrives after the row was painted', () => {
      const setTabs = renderTabs([bareAgent])
      // Pre-condition: this is exactly what the bug looked like.
      expect(screen.getByTestId('tab-tree-leaf').textContent).toContain('Agent')

      // Hydration lands. Only title/provider move; the git fields the
      // fingerprint covers are byte-identical, so the tree does NOT rebuild.
      setTabs([{ ...bareAgent, title: 'Agent Kiwi' } as Tab])
      expect(screen.getByTestId('tab-tree-leaf').textContent).toContain('Agent Kiwi')
    })

    it('picks up an agent provider that arrives after the row was painted', () => {
      const setTabs = renderTabs([bareAgent])
      // Empty ⇒ TabTypeIcon asked for no provider at all, which is what makes
      // the real icon fall back to the generic bot.
      expect(screen.getByTestId('agent-provider-icon').dataset.provider).toBe('')

      setTabs([{ ...bareAgent, agentProvider: AgentProvider.CLAUDE_CODE } as Tab])
      expect(screen.getByTestId('agent-provider-icon').dataset.provider)
        .toBe(String(AgentProvider.CLAUDE_CODE))
    })

    it('picks up a rename', () => {
      const named = { ...bareAgent, title: 'Agent Kiwi' } as Tab
      const setTabs = renderTabs([named])
      expect(screen.getByTestId('tab-tree-leaf').textContent).toContain('Agent Kiwi')

      setTabs([{ ...named, title: 'Renamed' } as Tab])
      expect(screen.getByTestId('tab-tree-leaf').textContent).toContain('Renamed')
    })

    it('picks up a terminal status flip', () => {
      const term = {
        ...bareAgent,
        type: TabType.TERMINAL,
        id: 't1',
        title: 'zsh',
        status: TerminalStatus.READY,
      } as Tab
      const setTabs = renderTabs([term])
      expect(screen.getByTestId('tab-tree-leaf').dataset.terminalStatus)
        .toBe(String(TerminalStatus.READY))

      setTabs([{ ...term, status: TerminalStatus.EXITED } as Tab])
      expect(screen.getByTestId('tab-tree-leaf').dataset.terminalStatus)
        .toBe(String(TerminalStatus.EXITED))
    })

    /**
     * The row must update IN PLACE. Remounting would be a second bug wearing
     * the fix's clothes: the leaf can hold the inline rename `<input>`, and a
     * fresh element drops the focus and the text the user is typing.
     */
    it('updates the row without remounting it', () => {
      const setTabs = renderTabs([bareAgent])
      const leafBefore = screen.getByTestId('tab-tree-leaf')

      setTabs([{ ...bareAgent, title: 'Agent Kiwi' } as Tab])
      expect(screen.getByTestId('tab-tree-leaf')).toBe(leafBefore)
      expect(leafBefore.textContent).toContain('Agent Kiwi')
    })

    /**
     * The cached bucket can name a tab the live list has already dropped — a
     * close is a metadata-invisible change until the next rebuild. The row must
     * disappear rather than read through `undefined`.
     */
    it('drops a row whose tab left the live list', () => {
      const second = { ...bareAgent, id: 'a2', position: '1|', title: 'Agent Two' } as Tab
      const setTabs = renderTabs([{ ...bareAgent, title: 'Agent One' } as Tab, second])
      expect(screen.getAllByTestId('tab-tree-leaf')).toHaveLength(2)

      setTabs([second])
      const leaves = screen.getAllByTestId('tab-tree-leaf')
      expect(leaves).toHaveLength(1)
      expect(leaves[0].textContent).toContain('Agent Two')
    })

    /**
     * `TabDragContext` renders the sidebar drag overlay from the `data` object
     * the row handed solid-dnd, and reads it when the drag STARTS. The row now
     * survives every metadata-only change, so a captured string would pin the
     * overlay to the title the tab had when its row mounted.
     */
    it('reports the live title to the drag overlay', () => {
      draggableData.length = 0
      const setTabs = renderTabs([bareAgent])
      expect(draggableData).toHaveLength(1)
      expect(draggableData[0].title).toBe('Agent')

      setTabs([{ ...bareAgent, title: 'Agent Kiwi' } as Tab])
      expect(draggableData, 'the row must not have remounted').toHaveLength(1)
      expect(draggableData[0].title).toBe('Agent Kiwi')
    })

    /**
     * The branch dialogs freeze what they are handed at open time, so the ref
     * must carry live tabs too — a stale snapshot would count and close the
     * wrong set.
     */
    it('hands the branch dialogs the live tabs, not the cached ones', async () => {
      const onDeleteBranch = vi.fn()
      const [tabs, setTabs] = createSignal<Tab[]>([bareAgent])
      render(() => (
        <WorkspaceTabTree
          tabs={tabs()}
          activeTabKey={null}
          onTabClick={() => {}}
          workspaceId="ws-1"
          onChangeBranch={() => {}}
          onDeleteBranch={onDeleteBranch}
        />
      ))

      setTabs([{ ...bareAgent, title: 'Agent Kiwi' } as Tab])
      const branchRow = screen.getByTestId('tab-tree-branch-group')
      await fireEvent.click(branchRow.querySelector('button') as HTMLButtonElement)
      await fireEvent.click(screen.getByText('Delete branch...'))

      expect(onDeleteBranch).toHaveBeenCalledTimes(1)
      expect(onDeleteBranch.mock.calls[0][0].tabs.map((t: Tab) => t.title)).toEqual(['Agent Kiwi'])
    })
  })

  it('keeps colon-overlapping branch groups independent when one is toggled', async () => {
    render(() => (
      <WorkspaceTabTree
        tabs={collisionPairTabs()}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
      />
    ))

    const [rowA, rowB] = screen.getAllByTestId('tab-tree-branch-group')
    const chevronOf = (row: HTMLElement) => row.querySelector('svg')!
    const isExpanded = (row: HTMLElement) => chevronOf(row).getAttribute('class')!.includes('chevronExpanded')

    expect(isExpanded(rowA)).toBe(true)
    expect(isExpanded(rowB)).toBe(true)

    await fireEvent.click(rowA)
    expect(isExpanded(rowA)).toBe(false)
    expect(isExpanded(rowB)).toBe(true)

    await fireEvent.click(rowA)
    expect(isExpanded(rowA)).toBe(true)
    expect(isExpanded(rowB)).toBe(true)
  })
})

// Subagent rows render as CHILDREN of their parent agent row, one indent level
// deeper. Asserted through the rendered indent (TabLeaf's only expression of
// depth) and through document order, so the test pins what the user sees.
describe('workspaceTabTree subagent nesting', () => {
  function subagentTab(id: string, parentAgentId: string): Tab {
    return { ...makeTab(TabType.AGENT, id, id), parentAgentId } as Tab
  }

  function leafIndents(): { id: string, indent: number }[] {
    return screen.getAllByTestId('tab-tree-leaf').map(el => ({
      id: el.getAttribute('data-tab-id') ?? '',
      indent: Number.parseInt((el as HTMLElement).style.paddingLeft, 10),
    }))
  }

  it('indents a subagent row one level under its parent', () => {
    render(() => (
      <WorkspaceTabTree
        tabs={[makeTab(TabType.AGENT, 'root', 'Root'), subagentTab('kid', 'root')]}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
      />
    ))

    const rows = leafIndents()
    expect(rows.map(r => r.id)).toEqual(['root', 'kid'])
    expect(rows[1].indent).toBeGreaterThan(rows[0].indent)
  })

  it('renders a subagent of a subagent two levels deep', () => {
    render(() => (
      <WorkspaceTabTree
        tabs={[
          makeTab(TabType.AGENT, 'root', 'Root'),
          subagentTab('kid', 'root'),
          subagentTab('grandkid', 'kid'),
        ]}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
      />
    ))

    const rows = leafIndents()
    expect(rows.map(r => r.id)).toEqual(['root', 'kid', 'grandkid'])
    expect(rows[1].indent).toBeGreaterThan(rows[0].indent)
    expect(rows[2].indent).toBeGreaterThan(rows[1].indent)
  })

  it('keeps a subagent flush with the roots when its parent tab is closed', () => {
    render(() => (
      <WorkspaceTabTree
        tabs={[makeTab(TabType.AGENT, 'other', 'Other'), subagentTab('kid', 'gone')]}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
      />
    ))

    // Both are roots, so the usual sort (position, then id) orders them.
    const rows = leafIndents()
    expect(rows.map(r => r.id)).toEqual(['kid', 'other'])
    expect(rows[1].indent).toBe(rows[0].indent)
  })

  it('re-nests a row when its parent link arrives after the first paint', () => {
    const [tabs, setTabs] = createSignal<Tab[]>([
      makeTab(TabType.AGENT, 'root', 'Root'),
      makeTab(TabType.AGENT, 'kid', 'Kid'),
    ])
    render(() => (
      <WorkspaceTabTree
        tabs={tabs()}
        activeTabKey={null}
        onTabClick={() => {}}
        workspaceId="ws-1"
      />
    ))
    expect(leafIndents()[1].indent).toBe(leafIndents()[0].indent)

    // Hydration fills in parentAgentId; the cached tree must rebuild.
    setTabs([makeTab(TabType.AGENT, 'root', 'Root'), subagentTab('kid', 'root')])

    const rows = leafIndents()
    expect(rows.map(r => r.id)).toEqual(['root', 'kid'])
    expect(rows[1].indent).toBeGreaterThan(rows[0].indent)
  })
})
