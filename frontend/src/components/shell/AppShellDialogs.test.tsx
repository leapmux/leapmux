/// <reference types="vitest/globals" />
import type { ComponentProps } from 'solid-js'
import type { AppShellDialogStates } from './AppShellDialogs'
import type { useTabOperations } from './useTabOperations'
import type { Tab } from '~/stores/tab.types'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createDialogState, createToggleDialog } from '~/hooks/createDialogState'
import { makeInspectResp } from '~/test-support/gitBranchFixtures'
import { AppShellDialogs } from './AppShellDialogs'

// Replace the module wholesale, like the sibling DeleteBranchDialog suite.
// A spread of the real module would pull the whole channel and Noise stack
// plus five generated protobuf modules into this file's graph, for roughly
// 250 ms of extra transform and import work per run. Vitest raises a
// missing export on property ACCESS, not at link time, and no dialog in
// this suite reaches another workerRpc call.
vi.mock('~/api/workerRpc', () => ({
  inspectBranchDeletion: vi.fn(),
  deleteBranch: vi.fn(),
  pushBranch: vi.fn(),
}))

vi.mock('~/components/common/Toast', () => ({
  showInfoToast: vi.fn(),
  showWarnToast: vi.fn(),
  showErrorToast: vi.fn(),
}))

// This stub replaces the ChangeBranch slot, so a test can fire its callbacks
// in the HOSTILE order (close, then notify). The stub does not drive that
// dialog's git-mode UI. The point under test is the parent closure, not the
// child dialog. The second button covers the workspace guard on
// onTerminalCreated, which the real dialog reaches only after a worktree
// create.
vi.mock('~/components/workspace/ChangeBranchDialog', () => ({
  ChangeBranchDialog: (props: {
    onBranchChanged?: (branch: string) => void
    onTerminalCreated?: (terminalId: string, workerId: string, workingDir: string, title: string) => void
    onClose: () => void
  }) => (
    <>
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
    lastTabConfirm: createDialogState(),
    keyPinConfirm: createDialogState(),
    changeBranch: createDialogState(),
    deleteBranch: createDialogState(),
  }
}

function renderDialogs(activeWorkspace: () => { id: string } | null = () => null) {
  const dialogs = makeDialogs()
  const onBranchChanged = vi.fn()
  const closeWorktreeTabs = vi.fn().mockResolvedValue({
    removed: true,
    failed: false,
    stillReferenced: false,
    unknown: false,
  })
  const focusedTileId = vi.fn(() => '')
  // `satisfies` type-checks the fields this fixture DOES fill, so a rename
  // of `closeWorktreeTabs` or a change to the TabContext shape fails to
  // compile. The one widening cast stays at the render call, because each
  // test makes only the branch dialogs' <Show> conditions truthy, so every
  // other dialog's props stay unread. The alternative constructs eight
  // operational stores (agentOps / termOps / view / metadata / …) that the
  // closed dialogs never ask for.
  const tabOps = { closeWorktreeTabs } satisfies Pick<ReturnType<typeof useTabOperations>, 'closeWorktreeTabs'>
  const props = {
    dialogs,
    onBranchChanged,
    tabOps: tabOps as unknown as ReturnType<typeof useTabOperations>,
    activeWorkspace,
    layoutStore: { focusedTileId } as unknown as ComponentProps<typeof AppShellDialogs>['layoutStore'],
    getCurrentTabContext: () => ({
      workerId: 'w1',
      workingDir: '/repo',
      homeDir: '/home/u',
      gitToplevel: '/repo',
    }),
  } satisfies Partial<ComponentProps<typeof AppShellDialogs>>
  render(() => <AppShellDialogs {...(props as unknown as ComponentProps<typeof AppShellDialogs>)} />)
  return { dialogs, onBranchChanged, closeWorktreeTabs, focusedTileId }
}

async function chooseSwitchToAndConfirm(branch: string) {
  await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
  const select = screen.getAllByRole('combobox')[0] as HTMLSelectElement
  fireEvent.change(select, { target: { value: branch } })
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
      tabs: [makeTab()],
    })

    await chooseSwitchToAndConfirm('main')

    await waitFor(() => expect(onBranchChanged).toHaveBeenCalledTimes(1))
    expect(onBranchChanged).toHaveBeenCalledWith('w1', '/repo', 'main')
    expect(showWarnToast).not.toHaveBeenCalled()
    // The dialog still closes: the notify must not prevent the close.
    await waitFor(() => expect(dialogs.deleteBranch.value()).toBeNull())
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
      tabs: [makeTab()],
    })
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    dialogs.deleteBranch.close()

    dialogs.deleteBranch.open({
      workerId: 'w9',
      gitToplevel: '/second-repo',
      branchName: 'doomed',
      tabs: [makeTab()],
    })
    await chooseSwitchToAndConfirm('main')

    await waitFor(() => expect(onBranchChanged).toHaveBeenCalledTimes(1))
    expect(onBranchChanged).toHaveBeenCalledWith('w9', '/second-repo', 'main')
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
      tabs: [makeTab()],
    })
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())

    // No close in between.
    dialogs.deleteBranch.open({
      workerId: 'w9',
      gitToplevel: '/second-repo',
      branchName: 'doomed',
      tabs: [makeTab()],
    })
    await chooseSwitchToAndConfirm('main')

    await waitFor(() => expect(onBranchChanged).toHaveBeenCalledTimes(1))
    expect(onBranchChanged).toHaveBeenCalledWith('w9', '/second-repo', 'main')
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
      tabs,
    })

    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: 'Delete branch' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Confirm?' })).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: 'Confirm?' }))

    await waitFor(() => expect(closeWorktreeTabs).toHaveBeenCalledWith(tabs))
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
    expect(onBranchChanged).toHaveBeenCalledWith('w2', '/other', 'main')
    expect(dialogs.changeBranch.value()).toBeNull()
  })

  // The workspace guard on onAgentCreated / onTerminalCreated had no
  // coverage: every test left `activeWorkspace` null, so the true branch was
  // unreachable and an inverted comparison would have stayed green. A tab
  // may only join the focused tile when the dialog's own workspace IS the
  // active one; otherwise it lands in the wrong workspace's tree.
  it('change branch: inserts a created tab only when the dialog targets the active workspace', async () => {
    const { dialogs, focusedTileId } = renderDialogs(() => ({ id: 'ws1' }))
    dialogs.changeBranch.open({
      workerId: 'w2',
      gitToplevel: '/other',
      workspaceId: 'ws1',
      branchName: 'feature',
      isWorktree: false,
    })

    fireEvent.click(await screen.findByTestId('change-branch-terminal-stub'))

    expect(focusedTileId).toHaveBeenCalled()
  })

  it('change branch: skips the tab insertion when the dialog targets another workspace', async () => {
    const { dialogs, focusedTileId } = renderDialogs(() => ({ id: 'another-ws' }))
    dialogs.changeBranch.open({
      workerId: 'w2',
      gitToplevel: '/other',
      workspaceId: 'ws1',
      branchName: 'feature',
      isWorktree: false,
    })

    fireEvent.click(await screen.findByTestId('change-branch-terminal-stub'))

    expect(focusedTileId).not.toHaveBeenCalled()
  })
})
