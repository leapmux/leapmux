/// <reference types="vitest/globals" />
import type { ComponentProps } from 'solid-js'
import type { AppShellDialogStates } from './AppShellDialogs'
import type { useTabOperations } from './useTabOperations'
import type { Tab } from '~/stores/tab.types'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { WorktreeAction } from '~/generated/proto/leapmux/v1/common_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { createDialogState, createToggleDialog, createUpdatableDialogState } from '~/hooks/createDialogState'
import { repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { emitAddTab } from '~/stores/tabOps'
import { installTestBridge } from '~/test-support/crdtBridge'
import { makeInspectResp, makeWorktreeRemovalResp } from '~/test-support/gitBranchFixtures'
import { pickMenuValue } from '~/test-support/menu'
import { createTestTabStores } from '~/test-support/tabStores'
import { AppShellDialogs } from './AppShellDialogs'

// Replace the module wholesale, like the sibling DeleteBranchDialog suite.
// A spread of the real module would pull the whole channel and Noise stack
// plus five generated protobuf modules into this file's graph, for roughly
// 250 ms of extra transform and import work per run. Vitest raises a
// missing export on property ACCESS, not at link time, and no dialog in
// this suite reaches another workerRpc call.
vi.mock('~/api/workerRpc', () => ({
  inspectBranchDeletion: vi.fn(),
  inspectWorktreeRemoval: vi.fn(),
  deleteBranch: vi.fn(),
  pushBranch: vi.fn(),
}))

vi.mock('~/components/common/Toast', () => ({
  showInfoToast: vi.fn(),
  showWarnToast: vi.fn(),
  showErrorToast: vi.fn(),
}))

// The two creation dialogs are stubbed for the same reason ChangeBranchDialog
// is (see below): this suite tests the PARENT's plumbing, not the dialogs' own
// fields. The stubs surface the guard reason the parent computes so a test can
// assert what the real dialog would disable submit on.
// The agent the stub reports as created, so a test can drive `onCreated`
// without standing up the real dialog's worker and directory fields.
//
// Built with `create(AgentInfoSchema, ...)`, not as an object literal, so it
// carries every field the wire supplies -- `status`, `agentProvider`,
// `acceptsMessages` and the rest that `protoToAgentTabFields` reads. A literal
// leaves them `undefined`, which no real response does.
//
// It is built inside the factory rather than in `vi.hoisted`, because
// `vi.hoisted` runs before the static imports initialize and cannot name
// `create`. The factory is async and runs lazily, so it can import both.
vi.mock('~/components/shell/NewAgentDialog', async () => {
  const { create } = await import('@bufbuild/protobuf')
  const { AgentInfoSchema } = await import('~/generated/proto/leapmux/v1/agent_pb')
  // No `gitStatus`: the OpenAgent response carries none. The worker computes
  // it in startup phase 1 and sends it on the STARTING broadcast, so the
  // `git status` shell-out does not block the RPC.
  const createdAgent = create(AgentInfoSchema, {
    id: 'created-agent',
    workerId: 'w1',
    title: 'Agent Mimi',
    workingDir: '/repo',
  })
  return {
    // Two buttons, one for each answer the real dialog gives for
    // `seedGitFromActiveTab`: the plain git mode may seed, every mode that
    // redirects the working directory may not.
    NewAgentDialog: (props: {
      blockedReason?: () => string | undefined
      onCreated: (agent: typeof createdAgent, opts: { seedGitFromActiveTab: boolean }) => void
      onClose: () => void
    }) => (
      <>
        <div data-testid="new-agent-stub">{props.blockedReason?.() ?? ''}</div>
        <button
          type="button"
          data-testid="new-agent-created"
          onClick={() => props.onCreated(createdAgent, { seedGitFromActiveTab: true })}
        >
          created
        </button>
        <button
          type="button"
          data-testid="new-agent-created-worktree"
          onClick={() => props.onCreated(createdAgent, { seedGitFromActiveTab: false })}
        >
          created in a worktree
        </button>
      </>
    ),
  }
})

vi.mock('~/components/shell/NewTerminalDialog', () => ({
  NewTerminalDialog: (props: { blockedReason?: () => string | undefined, onClose: () => void }) => (
    <div data-testid="new-terminal-stub">{props.blockedReason?.() ?? ''}</div>
  ),
}))

// This stub replaces the ChangeBranch slot, so a test can fire its callbacks
// in the HOSTILE order (close, then notify). The stub does not drive that
// dialog's git-mode UI. The point under test is the parent closure, not the
// child dialog. The second button covers the workspace guard on
// onTerminalCreated, which the real dialog reaches only after a worktree
// create.
vi.mock('~/components/workspace/ChangeBranchDialog', () => ({
  ChangeBranchDialog: (props: {
    blockedReason?: () => string | undefined
    onBranchChanged?: (branch: string) => void
    onTerminalCreated?: (terminalId: string, workerId: string, workingDir: string, title: string) => void
    onClose: () => void
  }) => (
    <>
      <div data-testid="change-branch-blocked">{props.blockedReason?.() ?? ''}</div>
      <button
        type="button"
        data-testid="change-branch-stub"
        onClick={() => {
          props.onClose()
          props.onBranchChanged?.('main')
        }}
      >
        stub
      </button>
      <button
        type="button"
        data-testid="change-branch-terminal-stub"
        onClick={() => props.onTerminalCreated?.('t1', 'w2', '/other', 'bash')}
      >
        terminal
      </button>
    </>
  ),
}))

function makeTab(): Tab {
  return {
    type: TabType.AGENT,
    id: 'a1',
    title: 'a1',
    tileId: 'tile-1',
    position: '0',
    workerId: 'w1',
    workingDir: '/repo',
    gitToplevel: '/repo',
  } as Tab
}

function makeDialogs(): AppShellDialogStates {
  return {
    newAgent: createToggleDialog(),
    newTerminal: createToggleDialog(),
    newWorkspace: createDialogState(),
    confirmDeleteWs: createDialogState(),
    confirmArchiveWs: createDialogState(),
    lastTabConfirm: createUpdatableDialogState(),
    keyPinConfirm: createDialogState(),
    changeBranch: createDialogState(),
    deleteBranch: createDialogState(),
  }
}

function renderDialogs(
  activeWorkspace: () => { id: string } | null = () => null,
  opts: { placementTileId?: string, mutatable?: boolean } = {},
) {
  const dialogs = makeDialogs()
  const onBranchChanged = vi.fn()
  const closeWorktreeTabs = vi.fn().mockResolvedValue({
    removed: true,
    failed: false,
    stillReferenced: false,
    unknown: false,
  })
  // `placementTileId` answers where a new tab would land — `''` means no
  // projected tree. `hasPlaceableTab` asks exactly this, so one knob drives
  // every guard-reason arm.
  const placementTileId = vi.fn(() => opts.placementTileId ?? '')
  // `satisfies` type-checks the fields this fixture DOES fill, so a rename
  // of `closeWorktreeTabs` or a change to the TabContext shape fails to
  // compile. The one widening cast stays at the render call, because each
  // test makes only the branch dialogs' <Show> conditions truthy, so every
  // other dialog's props stay unread. The alternative constructs eight
  // operational stores (agentOps / termOps / view / metadata / …) that the
  // closed dialogs never ask for.
  const tabOps = { closeWorktreeTabsAndReport: closeWorktreeTabs } satisfies Pick<ReturnType<typeof useTabOperations>, 'closeWorktreeTabsAndReport'>
  const repoGitStore = createRepoGitStore()
  const props = {
    dialogs,
    onBranchChanged,
    tabOps: tabOps as unknown as ReturnType<typeof useTabOperations>,
    activeWorkspace,
    isActiveWorkspaceMutatable: () => opts.mutatable ?? true,
    layoutStore: { placementTileId } as unknown as ComponentProps<typeof AppShellDialogs>['layoutStore'],
    repoGitStore,
    getCurrentTabContext: () => ({
      workerId: 'w1',
      workingDir: '/repo',
      homeDir: '/home/u',
      gitToplevel: '/repo',
    }),
  } satisfies Partial<ComponentProps<typeof AppShellDialogs>>
  render(() => <AppShellDialogs {...(props as unknown as ComponentProps<typeof AppShellDialogs>)} />)
  return { dialogs, onBranchChanged, closeWorktreeTabs, placementTileId }
}

async function chooseSwitchToAndConfirm(branch: string) {
  await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
  pickMenuValue('branch-select-menu', branch)
  // ConfirmButton arms on the first click and fires on the second.
  fireEvent.click(screen.getByRole('button', { name: 'Delete branch' }))
  await waitFor(() => expect(screen.getByRole('button', { name: 'Confirm?' })).toBeInTheDocument())
  fireEvent.click(screen.getByRole('button', { name: 'Confirm?' }))
}

describe('appShellDialogs branch dialogs', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(makeInspectResp())
    vi.mocked(workerRpc.deleteBranch).mockResolvedValue({ $typeName: 'leapmux.v1.DeleteBranchResponse' })
    // The worktree delete path preflights the removal before it hands the
    // tab closes off. Nothing here tests a refusal, so answer "no known
    // refusal" and let the delete proceed.
    vi.mocked(workerRpc.inspectWorktreeRemoval).mockResolvedValue(makeWorktreeRemovalResp())
  })

  // Regression: a delete of the checked-out branch left the deleted branch's
  // label on the sidebar, next to a warn toast. Only a page reload repaired
  // it. The dialog closed BEFORE it notified the parent. The parent's
  // callback then read the <Show> children accessor that the close disposed.
  // Solid throws "Stale read from <Show>" there, so the stamp and the
  // git-status refresh never ran.
  //
  // Only a test that drives the dialog THROUGH AppShellDialogs reaches this
  // bug. The dialog's own suite supplies a plain vi.fn(), so it reports
  // success while the real composition stays broken.
  it('delete branch: notifies the parent with the repo identity and shows no warning', async () => {
    const { dialogs, onBranchChanged } = renderDialogs()
    dialogs.deleteBranch.open({
      workerId: 'w1',
      gitToplevel: '/repo',
      branchName: 'doomed',
      isWorktree: false,
      tabs: [makeTab()],
    })

    await chooseSwitchToAndConfirm('main')

    await waitFor(() => expect(onBranchChanged).toHaveBeenCalledTimes(1))
    expect(onBranchChanged).toHaveBeenCalledWith(expect.objectContaining({ workerId: 'w1', gitToplevel: '/repo' }), 'main')
    expect(showWarnToast).not.toHaveBeenCalled()
    // The dialog still closes: the notify must not prevent the close.
    await waitFor(() => expect(dialogs.deleteBranch.value()).toBeNull())
  })

  // The payload's `isWorktree` has to REACH the dialog, and only a test that
  // drives it through this component covers that hop -- the dialog's own suite
  // passes the prop itself, so it stays green even when the parent drops it.
  //
  // Asserted with the inspect RPC still PENDING, because that is the only
  // window in which the seed is the sole source: once the response lands it
  // answers for itself, and a dropped payload field would go unnoticed.
  it('delete branch: seeds the dialog from the payload before the inspect lands', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion).mockReturnValue(new Promise(() => {}))
    const { dialogs } = renderDialogs()
    dialogs.deleteBranch.open({
      workerId: 'w1',
      gitToplevel: '/repo-worktrees/doomed',
      branchName: 'doomed',
      isWorktree: true,
      tabs: [makeTab()],
    })

    await waitFor(() => expect(screen.getByRole('heading', { name: 'Delete worktree' })).toBeInTheDocument())
  })

  it('delete branch: a reopen with a different repo uses the NEW identity', async () => {
    // A keyed <Show> re-runs its children function on every payload identity
    // change, so each open renders against its own payload. If it did not,
    // the second dialog would notify the parent with the first repo's
    // identity. It would also stamp the branch onto the wrong tabs. That is
    // a worse bug than the one that this commit fixes.
    const { dialogs, onBranchChanged } = renderDialogs()
    dialogs.deleteBranch.open({
      workerId: 'w1',
      gitToplevel: '/repo',
      branchName: 'doomed',
      isWorktree: false,
      tabs: [makeTab()],
    })
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    dialogs.deleteBranch.close()

    dialogs.deleteBranch.open({
      workerId: 'w9',
      gitToplevel: '/second-repo',
      branchName: 'doomed',
      isWorktree: false,
      tabs: [makeTab()],
    })
    await chooseSwitchToAndConfirm('main')

    await waitFor(() => expect(onBranchChanged).toHaveBeenCalledTimes(1))
    expect(onBranchChanged).toHaveBeenCalledWith(expect.objectContaining({ workerId: 'w9', gitToplevel: '/second-repo' }), 'main')
  })

  it('delete branch: a replacing open() re-points the dialog at the new repo', async () => {
    // `createDialogState.open()` replaces a payload without a null step, and
    // its own suite names this consumer. A keyed <Show> re-runs the children
    // function on that identity change, so the second repo's dialog reports
    // the second repo. A non-keyed <Show> would keep the first payload,
    // because its condition memo collapses every truthy value to one.
    const { dialogs, onBranchChanged } = renderDialogs()
    dialogs.deleteBranch.open({
      workerId: 'w1',
      gitToplevel: '/repo',
      branchName: 'doomed',
      isWorktree: false,
      tabs: [makeTab()],
    })
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())

    // No close in between.
    dialogs.deleteBranch.open({
      workerId: 'w9',
      gitToplevel: '/second-repo',
      branchName: 'doomed',
      isWorktree: false,
      tabs: [makeTab()],
    })
    await chooseSwitchToAndConfirm('main')

    await waitFor(() => expect(onBranchChanged).toHaveBeenCalledTimes(1))
    expect(onBranchChanged).toHaveBeenCalledWith(expect.objectContaining({ workerId: 'w9', gitToplevel: '/second-repo' }), 'main')
    expect(vi.mocked(workerRpc.deleteBranch).mock.calls[0][1]).toMatchObject({ path: '/second-repo' })
  })

  it('delete branch: the worktree path forwards the payload\'s tab snapshot', async () => {
    // The worktree path is the only consumer that reads `tabs` after an
    // await. Pin that it still receives exactly the tabs that the sidebar
    // opened the dialog with, by identity and not by value.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    const { dialogs, closeWorktreeTabs, onBranchChanged } = renderDialogs()
    const tabs = [makeTab()]
    dialogs.deleteBranch.open({
      workerId: 'w1',
      gitToplevel: '/repo',
      branchName: 'doomed',
      isWorktree: true,
      tabs,
    })

    await waitFor(() => expect(screen.getByTestId('working-tree-rows')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: 'Delete worktree' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Confirm?' })).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: 'Confirm?' }))

    await waitFor(() => expect(closeWorktreeTabs).toHaveBeenCalledWith(tabs, WorktreeAction.REMOVE, true))
    expect(closeWorktreeTabs.mock.calls[0][0]).toBe(tabs)
    // The worktree path removes the tabs outright. There is no branch to
    // stamp, so the dialog must not tell the parent that a branch changed.
    await waitFor(() => expect(dialogs.deleteBranch.value()).toBeNull())
    expect(onBranchChanged).not.toHaveBeenCalled()
  })

  it('delete branch: the parent callback survives a notify that arrives after the close', async () => {
    // Independent of the dialog's call order. A keyed <Show> hands the
    // children function the payload itself, so no callback holds an accessor
    // that the close can dispose. Even a child that closes first and
    // notifies afterwards must therefore reach the parent. This is the half
    // of the fix that makes the class of bug impossible rather than merely
    // absent today.
    const { dialogs, onBranchChanged } = renderDialogs()
    dialogs.changeBranch.open({
      workerId: 'w2',
      gitToplevel: '/other',
      workspaceId: 'ws1',
      branchName: 'feature',
      isWorktree: false,
    })

    fireEvent.click(await screen.findByTestId('change-branch-stub'))

    expect(onBranchChanged).toHaveBeenCalledTimes(1)
    expect(onBranchChanged).toHaveBeenCalledWith(expect.objectContaining({ workerId: 'w2', gitToplevel: '/other' }), 'main')
    expect(dialogs.changeBranch.value()).toBeNull()
  })

  // Every payload slot renders under a keyed <Show>, so its callbacks hold
  // the payload itself rather than an accessor the close disposes. These pin
  // the two slots where a stale read would strand a promise forever: the
  // workspace confirms resolve a `new Promise` that AppShell awaits with no
  // timeout, and keyPinConfirm's resolve gates every later key-pin prompt
  // through KeyPinStore's confirm chain. Both call resolve NEXT TO their
  // close, so before keyed they were correct only by statement order.
  it.each([
    ['confirmDeleteWs', true],
    ['confirmDeleteWs', false],
    ['confirmArchiveWs', true],
    ['confirmArchiveWs', false],
  ] as const)('%s resolves its promise when the user answers %s', async (which, answer) => {
    const { dialogs } = renderDialogs()
    const resolve = vi.fn()
    dialogs[which].open({ workspaceId: 'ws-1', resolve })

    if (!answer) {
      fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }))
    }
    else if (which === 'confirmDeleteWs') {
      // The delete confirm is `danger`, so ConfirmDialog wraps it in a
      // ConfirmButton: it arms on the first click and fires on the second.
      fireEvent.click(await screen.findByRole('button', { name: 'Delete' }))
      fireEvent.click(await screen.findByRole('button', { name: 'Confirm?' }))
    }
    else {
      fireEvent.click(await screen.findByRole('button', { name: 'Archive' }))
    }

    expect(resolve).toHaveBeenCalledTimes(1)
    expect(resolve).toHaveBeenCalledWith(answer)
    expect(dialogs[which].value()).toBeNull()
  })

  it('keyPinConfirm resolves its decision, so the confirm chain is not stranded', async () => {
    const { dialogs } = renderDialogs()
    const resolve = vi.fn()
    dialogs.keyPinConfirm.open({
      workerId: 'w1',
      expectedFingerprint: 'aa:bb',
      actualFingerprint: 'cc:dd',
      resolve,
    })

    fireEvent.click(await screen.findByRole('button', { name: /Reject/i }))

    expect(resolve).toHaveBeenCalledTimes(1)
    expect(dialogs.keyPinConfirm.value()).toBeNull()
  })

  // The workspace guard on onAgentCreated / onTerminalCreated had no
  // coverage: every test left `activeWorkspace` null, so the true branch was
  // unreachable and an inverted comparison would have stayed green. A tab
  // may only join the focused tile when the dialog's own workspace IS the
  // active one; otherwise it lands in the wrong workspace's tree.
  it('change branch: inserts a created tab only when the dialog targets the active workspace', async () => {
    const { dialogs, placementTileId } = renderDialogs(() => ({ id: 'ws1' }))
    dialogs.changeBranch.open({
      workerId: 'w2',
      gitToplevel: '/other',
      workspaceId: 'ws1',
      branchName: 'feature',
      isWorktree: false,
    })

    fireEvent.click(await screen.findByTestId('change-branch-terminal-stub'))

    expect(placementTileId).toHaveBeenCalled()
  })

  it('change branch: skips the tab insertion when the dialog targets another workspace', async () => {
    const { dialogs, placementTileId } = renderDialogs(() => ({ id: 'another-ws' }))
    dialogs.changeBranch.open({
      workerId: 'w2',
      gitToplevel: '/other',
      workspaceId: 'ws1',
      branchName: 'feature',
      isWorktree: false,
    })

    fireEvent.click(await screen.findByTestId('change-branch-terminal-stub'))

    expect(placementTileId).not.toHaveBeenCalled()
  })

  // The change-branch guard reason describes the ACTIVE workspace's
  // placement — the same condition the tab-insertion callbacks gate on. A
  // dialog opened against another workspace's branch row places no local
  // tab, so it must not inherit a reason it cannot act on.
  it('change branch: hands the guard reason only when the dialog targets the active workspace', async () => {
    const { dialogs } = renderDialogs(() => ({ id: 'ws1' }), { placementTileId: '' })
    dialogs.changeBranch.open({
      workerId: 'w2',
      gitToplevel: '/other',
      workspaceId: 'ws1',
      branchName: 'feature',
      isWorktree: false,
    })

    expect(await screen.findByTestId('change-branch-blocked')).toHaveTextContent(/not ready yet/i)
  })

  it('change branch: passes no reason when the dialog targets another workspace', async () => {
    const { dialogs } = renderDialogs(() => ({ id: 'another-ws' }), { placementTileId: '' })
    dialogs.changeBranch.open({
      workerId: 'w2',
      gitToplevel: '/other',
      workspaceId: 'ws1',
      branchName: 'feature',
      isWorktree: false,
    })

    expect(await screen.findByTestId('change-branch-blocked')).toHaveTextContent(/^$/)
  })

  // A tab is placed onto the active workspace's projected tree, so the
  // creation dialogs must not create the worker-side agent/pty when there is
  // nowhere to place it — the placement refusal arrives only after the RPC,
  // and the resource it strands has no tab to reach it by. The parent hands
  // the dialogs the reason; these pin its outcomes, one per arm.
  describe('new-tab guard reason', () => {
    it('tells the dialogs to block creation when there is no workspace at all', async () => {
      const { dialogs } = renderDialogs(() => null)
      dialogs.newAgent.open()

      expect(await screen.findByTestId('new-agent-stub')).toHaveTextContent(/create a workspace first/i)
    })

    it('blocks while the active workspace has no projected tree yet', async () => {
      // placementTileId '' — the tree never arrived, so nothing can take a tab.
      const { dialogs } = renderDialogs(() => ({ id: 'ws1' }), { placementTileId: '' })
      dialogs.newTerminal.open()

      expect(await screen.findByTestId('new-terminal-stub')).toHaveTextContent(/not ready yet/i)
    })

    it('blocks when the active workspace is archived', async () => {
      // An archived workspace keeps its tree, so only the mutatability arm
      // fires — the same refusal every quick-action path applies.
      const { dialogs } = renderDialogs(() => ({ id: 'ws1' }), { placementTileId: 'tile-1', mutatable: false })
      dialogs.newAgent.open()

      expect(await screen.findByTestId('new-agent-stub')).toHaveTextContent(/archived/i)
    })

    it('passes no reason once the workspace can take a tab', async () => {
      const { dialogs } = renderDialogs(
        () => ({ id: 'ws1' }),
        { placementTileId: 'tile-1' },
      )
      dialogs.newAgent.open()

      expect(await screen.findByTestId('new-agent-stub')).toHaveTextContent(/^$/)
    })
  })
})

/**
 * What the parent writes when the New Agent dialog reports a created agent.
 *
 * This one needs the operational stores the suite above deliberately leaves
 * unfilled, because the assertion is about the tab that `openTabInFocusedTile`
 * actually places. It gets its own render helper rather than widening
 * `renderDialogs`, so the branch-dialog tests keep paying nothing for it.
 */
describe('appShellDialogs agent creation', () => {
  function renderForCreate() {
    const harness = installTestBridge({ workspaceId: 'ws1' })
    const stores = createTestTabStores('ws1')
    const repoGitStore = createRepoGitStore()
    const dialogs = makeDialogs()
    const props = {
      dialogs,
      activeWorkspace: () => ({ id: 'ws1' }),
      isActiveWorkspaceMutatable: () => true,
      layoutStore: stores.layoutStore,
      view: stores.view,
      metadata: stores.metadata,
      selection: stores.selection,
      repoGitStore,
      // Called unguarded from the created-agent handler, on the next frame.
      focusEditor: vi.fn(),
      getCurrentTabContext: () => ({
        workerId: 'w1',
        workingDir: '/repo',
        homeDir: '/home/u',
        gitToplevel: '/repo',
      }),
    }
    render(() => <AppShellDialogs {...(props as unknown as ComponentProps<typeof AppShellDialogs>)} />)
    return { dialogs, repoGitStore, ...stores, rootTileId: harness.rootTileId }
  }

  /**
   * An ACTIVE tab on a DIFFERENT worker, in the same directory the new agent
   * opens in, with its repo already known.
   *
   * A different worker is what makes the seed observable: same-worker opens
   * share a repo key and copy nothing. The directories must match, because
   * `resolveOptimisticGitInfo` refuses to copy across a directory change.
   */
  function seedActiveTabOnOtherWorker(
    stores: ReturnType<typeof renderForCreate>,
  ) {
    emitAddTab({
      type: TabType.AGENT,
      id: 'active',
      tileId: stores.rootTileId,
      position: 'p1',
      workerId: 'w-other',
    })
    stores.metadata.patch('active', { workingDir: '/repo', gitToplevel: '/repo' })
    stores.selection.setActive(stores.view.getAgentTab('active')!)
    stores.repoGitStore.upsert(repoKey('w-other', '/repo'), {
      workerId: 'w-other',
      toplevel: '/repo',
      branch: 'feature/sidebar',
      gitStatusSeen: true,
    })
  }

  // Both facts belong to `openedAgentTabFields`, whose doc comment states why:
  // it carries `hydrated: true`, and it writes no repo entry because the
  // OpenAgent response carries no git status.
  it('marks the placed agent hydrated and writes no repo entry', async () => {
    const { dialogs, repoGitStore, view, metadata } = renderForCreate()
    dialogs.newAgent.open()

    fireEvent.click(await screen.findByTestId('new-agent-created'))

    await waitFor(() => expect(view.getAgentTab('created-agent')).toBeDefined())
    // `isHydrated` reads the metadata row, not the assembled tab.
    expect(
      metadata.get('created-agent')?.hydrated,
      'the hydrator must not re-ask for this tab',
    ).toBe(true)
    expect(view.getAgentTab('created-agent')?.gitToplevel, 'the response carries no repo identity').toBeUndefined()
    expect(Object.keys(repoGitStore.repos())).toEqual([])
  })

  /**
   * The OpenAgent response carries no git status, so without a seed the new tab
   * renders outside its repository group for the whole of the agent's startup.
   * The tab-bar path already seeds; this closes the gap for the dialog.
   */
  it('seeds the branch from the active tab when the agent opens in place', async () => {
    const stores = renderForCreate()
    seedActiveTabOnOtherWorker(stores)
    stores.dialogs.newAgent.open()

    fireEvent.click(await screen.findByTestId('new-agent-created'))

    await waitFor(() => expect(stores.view.getAgentTab('created-agent')).toBeDefined())
    expect(
      stores.repoGitStore.get(repoKey('w1', '/repo'))?.branch,
      'the sidebar can group the new tab before the worker reports',
    ).toBe('feature/sidebar')
  })

  /**
   * Every git mode except the plain one redirects the agent -- into a new or
   * existing worktree, or onto a different branch. The active tab's branch is
   * then the wrong answer for the directory the agent lands in, so a seed there
   * would put a confidently wrong label on the tab rather than no label.
   */
  it('seeds nothing when the agent is redirected into a worktree', async () => {
    const stores = renderForCreate()
    seedActiveTabOnOtherWorker(stores)
    stores.dialogs.newAgent.open()

    fireEvent.click(await screen.findByTestId('new-agent-created-worktree'))

    await waitFor(() => expect(stores.view.getAgentTab('created-agent')).toBeDefined())
    expect(stores.repoGitStore.get(repoKey('w1', '/repo'))).toBeUndefined()
  })

  it('closes the dialog once the agent is placed', async () => {
    const { dialogs } = renderForCreate()
    dialogs.newAgent.open()

    fireEvent.click(await screen.findByTestId('new-agent-created'))

    await waitFor(() => expect(dialogs.newAgent.isOpen()).toBe(false))
  })
})
