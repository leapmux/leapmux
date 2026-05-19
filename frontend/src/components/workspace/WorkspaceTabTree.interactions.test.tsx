import type { Tab } from '~/stores/tab.types'
import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { buildTree, WorkspaceTabTree } from './WorkspaceTabTree'

vi.mock('@thisbeyond/solid-dnd', () => ({
  createDraggable: () => () => {},
}))

vi.mock('~/components/shell/TabDragContext', () => ({
  SIDEBAR_TAB_PREFIX: 'sidebar-tab:',
}))

vi.mock('~/components/common/AgentProviderIcon', () => ({
  AgentProviderIcon: () => null,
}))

function makeTab(type: TabType, id: string, title?: string): Tab {
  // The wide `TabType` parameter is narrowed to the union's literal
  // members via the explicit return-type cast; callers pass one of the
  // three discriminants so the runtime value is always a valid variant.
  return {
    type,
    id,
    title: title ?? id,
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
        tabItemOps={{ onClose: onTabClose }}
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
    expect(onRename).toHaveBeenCalledWith(expect.objectContaining({ type: TabType.AGENT, id: 'a1' }), 'Renamed Agent')
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
   */
  it('does not re-reconcile branch rows when only non-tree fields change', async () => {
    const initial: Tab = {
      type: TabType.AGENT,
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
