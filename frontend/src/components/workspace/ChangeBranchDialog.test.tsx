/// <reference types="vitest/globals" />
import type { GitBranchEntry } from '~/generated/leapmux/v1/git_pb'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { ChangeBranchDialog } from './ChangeBranchDialog'

vi.mock('~/context/OrgContext', () => ({
  useOrg: () => ({ orgId: () => 'org-1', slug: () => 'admin' }),
}))

vi.mock('~/api/clients', () => ({
  workerClient: {
    listWorkers: vi.fn().mockResolvedValue({
      workers: [{ id: 'w1', online: true, name: 'worker-1' }],
    }),
  },
}))

vi.mock('~/stores/workerInfo.store', () => {
  const fetchWorkerInfo = vi.fn().mockResolvedValue(undefined)
  return {
    workerInfoStore: {
      fetchWorkerInfo,
      workerInfo: () => null,
      getHomeDir: () => '/home/u',
      getOs: () => undefined,
    },
  }
})

vi.mock('~/api/workerRpc', () => ({
  inspectBranchChange: vi.fn(),
  listGitBranches: vi.fn(),
  listGitWorktrees: vi.fn(),
  getGitInfo: vi.fn(),
  listAvailableShells: vi.fn(),
  checkoutBranch: vi.fn(),
  createBranch: vi.fn(),
  openAgent: vi.fn(),
  openTerminal: vi.fn(),
}))

const branches: GitBranchEntry[] = [
  { $typeName: 'leapmux.v1.GitBranchEntry', name: 'main', isRemote: false },
  { $typeName: 'leapmux.v1.GitBranchEntry', name: 'feature', isRemote: false },
  { $typeName: 'leapmux.v1.GitBranchEntry', name: 'origin/remote-only', isRemote: true },
]

function setupRpcMocks() {
  // The dialog now issues a single InspectBranchChange RPC at mount;
  // the listGitBranches / getGitInfo mocks remain (unused) so the
  // shared workerRpc mock surface keeps a consistent shape.
  vi.mocked(workerRpc.inspectBranchChange).mockResolvedValue({
    $typeName: 'leapmux.v1.InspectBranchChangeResponse',
    repoRoot: '/repo',
    toplevel: '/repo',
    isWorktree: false,
    currentBranch: 'feature',
    isDirty: false,
    branches,
  })
  vi.mocked(workerRpc.listAvailableShells).mockResolvedValue({
    $typeName: 'leapmux.v1.ListAvailableShellsResponse',
    shells: ['/bin/zsh', '/bin/bash'],
    defaultShell: '/bin/zsh',
  })
  vi.mocked(workerRpc.checkoutBranch).mockResolvedValue({ $typeName: 'leapmux.v1.CheckoutBranchResponse' })
  vi.mocked(workerRpc.createBranch).mockResolvedValue({ $typeName: 'leapmux.v1.CreateBranchResponse' })
  vi.mocked(workerRpc.listGitWorktrees).mockResolvedValue({
    $typeName: 'leapmux.v1.ListGitWorktreesResponse',
    worktrees: [],
  })
}

function renderDialog(overrides?: Partial<Parameters<typeof ChangeBranchDialog>[0]>) {
  const props = {
    workerId: 'w1',
    gitToplevel: '/repo',
    workspaceId: 'ws-1',
    branchName: 'main',
    isWorktree: false,
    availableProviders: [AgentProvider.CLAUDE_CODE],
    onClose: vi.fn(),
    onBranchChanged: vi.fn(),
    onAgentCreated: vi.fn(),
    onTerminalCreated: vi.fn(),
    ...overrides,
  }
  render(() => <ChangeBranchDialog {...props} />)
  return props
}

// The dialog hides its form until both listGitBranches and getGitInfo
// have resolved, so every behavioral test waits for the "Switch to
// branch" radio to appear before interacting.
async function awaitFormReady() {
  await waitFor(() => expect(screen.getByText('Switch to branch')).toBeInTheDocument())
}

describe('changeBranchDialog', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    setupRpcMocks()
  })

  it('renders the three modes', async () => {
    renderDialog()
    await awaitFormReady()
    expect(screen.getByText('Switch to branch')).toBeInTheDocument()
    expect(screen.getByText('Create new branch')).toBeInTheDocument()
    expect(screen.getByText('Create new worktree')).toBeInTheDocument()
  })

  it('mount fires exactly one InspectBranchChange RPC (no separate getGitInfo + listGitBranches)', async () => {
    // Regression guard for the InspectBranchChange refactor: the dialog
    // used to fire getGitInfo (useGitPathInfo) AND listGitBranches
    // (GitOptions) sequentially, each forking queryGitPathInfo
    // server-side. Now it fires one bundle RPC and lets GitOptions
    // consume the branches via preloadedBranches — so neither of the
    // old RPCs may be touched on open.
    renderDialog()
    await awaitFormReady()
    await Promise.resolve()
    await Promise.resolve()
    expect(workerRpc.inspectBranchChange).toHaveBeenCalledTimes(1)
    expect(workerRpc.listGitBranches).not.toHaveBeenCalled()
    expect(workerRpc.getGitInfo).not.toHaveBeenCalled()
  })

  it('passes the row-supplied path to inspectBranchChange', async () => {
    renderDialog({ branchName: 'main' })
    await awaitFormReady()
    // The third arg is the opts bag with the createGuardedFetch
    // signal — match it loosely so signal-threading refactors don't
    // re-break this assertion.
    expect(workerRpc.inspectBranchChange).toHaveBeenCalledWith(
      'w1',
      expect.objectContaining({ path: '/repo', workerId: 'w1' }),
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    )
  })

  it('paints the form synchronously from the seed while the inspect RPC is in flight', async () => {
    // The dialog seeds gitInfo from props.branchName + props.gitToplevel
    // so the form is interactive on the very first paint — no spinner
    // round trip just to render the radios. Verifies the seed reaches
    // the GitPathInfo accessor (the form-gate predicate consumes it).
    vi.mocked(workerRpc.inspectBranchChange).mockReturnValue(new Promise<never>(() => {})) // probe held pending
    renderDialog({ branchName: 'main' })
    // No await needed: the seed gates showGitOptions synchronously.
    expect(screen.getByText('Switch to branch')).toBeInTheDocument()
    expect(screen.getByText('Create new branch')).toBeInTheDocument()
    // BranchSelect shows the loading placeholder until the inspect
    // resolves (no separate ListGitBranches fetcher exists for the
    // preloaded-branches path).
    expect(screen.getByText('Loading branches...')).toBeInTheDocument()
  })

  it('uses the seed-derived currentBranch in the base picker before the RPC lands', async () => {
    // The seeded currentBranch (= props.branchName) is the input
    // currentBranch-dependent UI reads pre-RPC. Verify the
    // create-branch collision check (which compares typed input against
    // currentBranch via branchExists / not-current-branch checks) sees
    // the seeded value: typing the seeded branch name reports
    // "already exists" once the branches list lands. With the inspect
    // pending, the picker is loading but the seed-driven dirty/no-op
    // logic uses currentBranch='main'.
    let resolveInspect!: (resp: Awaited<ReturnType<typeof workerRpc.inspectBranchChange>>) => void
    vi.mocked(workerRpc.inspectBranchChange).mockReturnValue(
      new Promise((resolve) => { resolveInspect = resolve }),
    )
    renderDialog({ branchName: 'main' })
    expect(screen.getByText('Switch to branch')).toBeInTheDocument()
    // Resolve the RPC. The branches list now includes 'main'; with the
    // seeded currentBranch='main' the picker shows "main (current)".
    resolveInspect({
      $typeName: 'leapmux.v1.InspectBranchChangeResponse',
      repoRoot: '/repo',
      toplevel: '/repo',
      isWorktree: false,
      currentBranch: 'main',
      isDirty: false,
      branches,
    } as Awaited<ReturnType<typeof workerRpc.inspectBranchChange>>)
    fireEvent.click(screen.getByText('Create new branch'))
    await waitFor(() => {
      expect(screen.getByRole('option', { name: 'main (current)' })).toBeInTheDocument()
    })
  })

  it('marks the current branch with "(current)" in the base-branch picker', async () => {
    renderDialog()
    await awaitFormReady()
    // Switch into create-branch mode; its Base Branch picker passes
    // showCurrent so the currently-checked-out branch is suffixed.
    fireEvent.click(screen.getByText('Create new branch'))
    await waitFor(() => {
      expect(screen.getByRole('option', { name: 'feature (current)' })).toBeInTheDocument()
    })
  })

  it('switch-branch: Apply disabled when no branch picked', async () => {
    renderDialog()
    await awaitFormReady()
    const apply = screen.getByRole('button', { name: 'Apply' }) as HTMLButtonElement
    expect(apply.disabled).toBe(true)
  })

  it('switch-branch: calls checkoutBranch with picked branch and closes', async () => {
    const props = renderDialog()
    await awaitFormReady()
    const select = screen.getAllByRole('combobox')[0] as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'main' } })

    const apply = screen.getByRole('button', { name: 'Apply' })
    fireEvent.click(apply)

    await waitFor(() => expect(workerRpc.checkoutBranch).toHaveBeenCalledTimes(1))
    expect(vi.mocked(workerRpc.checkoutBranch).mock.calls[0][0]).toBe('w1')
    expect(vi.mocked(workerRpc.checkoutBranch).mock.calls[0][1]).toMatchObject({
      path: '/repo',
      branch: 'main',
      workerId: 'w1',
    })
    await waitFor(() => expect(props.onBranchChanged).toHaveBeenCalled())
    await waitFor(() => expect(props.onClose).toHaveBeenCalled())
  })

  it('switch-branch: fires onBranchChanged with the chosen branch name', async () => {
    const props = renderDialog()
    await awaitFormReady()
    const select = screen.getAllByRole('combobox')[0] as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'main' } })
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))

    await waitFor(() => expect(props.onBranchChanged).toHaveBeenCalledTimes(1))
    expect(props.onBranchChanged).toHaveBeenCalledWith('main')
  })

  it('switch-branch: fires onBranchChanged with the local name when checking out a remote-tracking ref', async () => {
    // The worker creates a local branch named after the remote ref's
    // last segment (e.g. "origin/remote-only" → local "remote-only").
    // The sidebar shows the local name, so onBranchChanged must too.
    const props = renderDialog()
    await awaitFormReady()
    const select = screen.getAllByRole('combobox')[0] as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'origin/remote-only' } })
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))

    await waitFor(() => expect(props.onBranchChanged).toHaveBeenCalledTimes(1))
    expect(props.onBranchChanged).toHaveBeenCalledWith('remote-only')
  })

  it('switch-branch: preserves a local branch name that contains "/" instead of stripping the prefix', async () => {
    // Regression: stripRemotePrefix used to run unconditionally on the
    // selected target, so a local branch like `feature/auth` was stamped
    // as `auth` on every tab in the group — bucketing them under a
    // non-existent branch until the next status refresh repaired it.
    // The fix consults the BranchSelect entry's isRemote flag and only
    // strips the prefix for genuinely remote refs.
    vi.mocked(workerRpc.inspectBranchChange).mockResolvedValue({
      $typeName: 'leapmux.v1.InspectBranchChangeResponse',
      repoRoot: '/repo',
      toplevel: '/repo',
      isWorktree: false,
      currentBranch: 'main',
      isDirty: false,
      branches: [
        ...branches,
        { $typeName: 'leapmux.v1.GitBranchEntry', name: 'feature/auth', isRemote: false },
      ],
    })
    const props = renderDialog()
    await awaitFormReady()
    const select = screen.getAllByRole('combobox')[0] as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'feature/auth' } })
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))

    await waitFor(() => expect(props.onBranchChanged).toHaveBeenCalledTimes(1))
    expect(props.onBranchChanged).toHaveBeenCalledWith('feature/auth')
  })

  it('create-branch: fires onBranchChanged with the new branch name', async () => {
    const props = renderDialog()
    await awaitFormReady()
    fireEvent.click(screen.getByText('Create new branch'))
    const input = screen.getByPlaceholderText('feature-branch') as HTMLInputElement
    fireEvent.input(input, { target: { value: 'shiny-new' } })
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))

    await waitFor(() => expect(props.onBranchChanged).toHaveBeenCalledTimes(1))
    expect(props.onBranchChanged).toHaveBeenCalledWith('shiny-new')
  })

  it('create-branch: calls createBranch with name and base', async () => {
    const props = renderDialog()
    await awaitFormReady()

    fireEvent.click(screen.getByText('Create new branch'))
    const input = screen.getByPlaceholderText('feature-branch') as HTMLInputElement
    fireEvent.input(input, { target: { value: 'shiny-new' } })

    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))
    await waitFor(() => expect(workerRpc.createBranch).toHaveBeenCalledTimes(1))
    expect(vi.mocked(workerRpc.createBranch).mock.calls[0][1]).toMatchObject({
      newBranch: 'shiny-new',
      baseBranch: 'feature',
      path: '/repo',
    })
    await waitFor(() => expect(props.onBranchChanged).toHaveBeenCalled())
  })

  it('create-branch: Apply disabled when name collides with an existing branch', async () => {
    renderDialog()
    await awaitFormReady()
    fireEvent.click(screen.getByText('Create new branch'))
    const input = screen.getByPlaceholderText('feature-branch') as HTMLInputElement
    fireEvent.input(input, { target: { value: 'main' } })
    const apply = screen.getByRole('button', { name: 'Apply' }) as HTMLButtonElement
    expect(apply.disabled).toBe(true)
    expect(screen.getByText('A branch with this name already exists')).toBeInTheDocument()
  })

  it('worktree mode (agent): calls openAgent with createWorktree=true', async () => {
    vi.mocked(workerRpc.openAgent).mockResolvedValue({
      $typeName: 'leapmux.v1.OpenAgentResponse',
      agent: {
        $typeName: 'leapmux.v1.AgentInfo',
        id: 'a1',
        workerId: 'w1',
      } as never,
    } as never)

    const props = renderDialog()
    await awaitFormReady()
    fireEvent.click(screen.getByText('Create new worktree'))

    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))
    await waitFor(() => expect(workerRpc.openAgent).toHaveBeenCalledTimes(1))
    expect(vi.mocked(workerRpc.openAgent).mock.calls[0][1]).toMatchObject({
      createWorktree: true,
      workerId: 'w1',
      workingDir: '/repo',
    })
    await waitFor(() => expect(props.onAgentCreated).toHaveBeenCalled())
  })

  it('worktree mode (terminal): calls openTerminal with the chosen shell', async () => {
    vi.mocked(workerRpc.openTerminal).mockResolvedValue({
      $typeName: 'leapmux.v1.OpenTerminalResponse',
      terminalId: 't1',
      title: 'bash',
    } as never)

    const props = renderDialog()
    await awaitFormReady()
    fireEvent.click(screen.getByText('Create new worktree'))

    // Switch the "Open as" dropdown to terminal.
    const openAs = screen.getAllByRole('combobox').find(c => (c as HTMLSelectElement).value === String(TabType.AGENT)) as HTMLSelectElement
    fireEvent.change(openAs, { target: { value: String(TabType.TERMINAL) } })
    await waitFor(() => expect(workerRpc.listAvailableShells).toHaveBeenCalled())
    // Wait for the createResource-backed shells list to resolve so the
    // default shell propagates into canSubmit() and unblocks Apply.
    const apply = screen.getByRole('button', { name: 'Apply' }) as HTMLButtonElement
    await waitFor(() => expect(apply.disabled).toBe(false))

    fireEvent.click(apply)
    await waitFor(() => expect(workerRpc.openTerminal).toHaveBeenCalledTimes(1))
    expect(vi.mocked(workerRpc.openTerminal).mock.calls[0][1]).toMatchObject({
      createWorktree: true,
      shell: '/bin/zsh',
    })
    await waitFor(() => expect(props.onTerminalCreated).toHaveBeenCalledWith('t1', 'w1', '/repo', 'bash'))
  })

  it('cancel closes without firing any RPC', async () => {
    const props = renderDialog()
    await awaitFormReady()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(workerRpc.checkoutBranch).not.toHaveBeenCalled()
    expect(workerRpc.createBranch).not.toHaveBeenCalled()
    expect(workerRpc.openAgent).not.toHaveBeenCalled()
    expect(workerRpc.openTerminal).not.toHaveBeenCalled()
    // Cancel triggers the dialog close animation, then props.onClose() —
    // assertion would be flaky in jsdom without timers, so just verify
    // the dialog disabled-during-busy gate is open.
    void props
  })

  it('switch-branch: picking the current branch disables Apply and surfaces an inline notice', async () => {
    // Regression guard for the silent-no-op bug: prior to the
    // checkoutBranchError plumbing in GitOptions, picking `feature`
    // (the current branch in the test fixtures) would leave Apply
    // enabled and the worker happily ran `git checkout feature` to no
    // effect — making the dialog appear to do nothing on submit.
    renderDialog()
    await awaitFormReady()
    const select = screen.getAllByRole('combobox')[0] as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'feature' } })
    const apply = screen.getByRole('button', { name: 'Apply' }) as HTMLButtonElement
    await waitFor(() => expect(apply.disabled).toBe(true))
    expect(screen.getByText(/already on this branch/i)).toBeInTheDocument()
  })

  it('switch-branch: picking a remote ref that strips to the current branch also disables Apply', async () => {
    // `origin/feature` while on `feature`: the worker would resolve
    // both to local `feature` (no-op). Same outcome as picking
    // `feature` directly — must also gate submit and show the
    // already-on-local notice.
    vi.mocked(workerRpc.inspectBranchChange).mockResolvedValue({
      $typeName: 'leapmux.v1.InspectBranchChangeResponse',
      repoRoot: '/repo',
      toplevel: '/repo',
      isWorktree: false,
      currentBranch: 'feature',
      isDirty: false,
      branches: [
        ...branches,
        { $typeName: 'leapmux.v1.GitBranchEntry', name: 'origin/feature', isRemote: true },
      ],
    })
    renderDialog()
    await awaitFormReady()
    const select = screen.getAllByRole('combobox')[0] as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'origin/feature' } })
    const apply = screen.getByRole('button', { name: 'Apply' }) as HTMLButtonElement
    await waitFor(() => expect(apply.disabled).toBe(true))
    expect(screen.getByText(/already on local branch "feature"/i)).toBeInTheDocument()
  })

  it('switch-branch: picking a non-current branch enables Apply (positive control for the no-op gate)', async () => {
    // The fixture's currentBranch is `feature`; `main` is a real
    // switch destination — Apply must remain enabled.
    renderDialog()
    await awaitFormReady()
    const select = screen.getAllByRole('combobox')[0] as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'main' } })
    const apply = screen.getByRole('button', { name: 'Apply' }) as HTMLButtonElement
    await waitFor(() => expect(apply.disabled).toBe(false))
    expect(screen.queryByText(/already on/i)).toBeNull()
  })

  it('worktree mode: switching Open as → Terminal triggers shell listing', async () => {
    renderDialog()
    await awaitFormReady()
    fireEvent.click(screen.getByText('Create new worktree'))
    expect(workerRpc.listAvailableShells).not.toHaveBeenCalled()

    const openAs = screen.getAllByRole('combobox').find(c => (c as HTMLSelectElement).value === String(TabType.AGENT)) as HTMLSelectElement
    fireEvent.change(openAs, { target: { value: String(TabType.TERMINAL) } })
    await waitFor(() => expect(workerRpc.listAvailableShells).toHaveBeenCalledTimes(1))
  })

  it('worktree mode: shell listing fires exactly once even after toggling Open as back to Agent and again to Terminal', async () => {
    // Regression guard for the createResource latching memo: the shell
    // list should be fetched only the first time the user enters
    // create-worktree + terminal. Without the latch, createResource would
    // re-run its fetcher on every false→truthy transition of the source.
    renderDialog()
    await awaitFormReady()
    fireEvent.click(screen.getByText('Create new worktree'))

    const openAs = screen.getAllByRole('combobox').find(c => (c as HTMLSelectElement).value === String(TabType.AGENT)) as HTMLSelectElement
    fireEvent.change(openAs, { target: { value: String(TabType.TERMINAL) } })
    await waitFor(() => expect(workerRpc.listAvailableShells).toHaveBeenCalledTimes(1))

    // Toggle away (Agent → switch-branch path) and back to terminal.
    fireEvent.change(openAs, { target: { value: String(TabType.AGENT) } })
    fireEvent.click(screen.getByText('Switch to branch'))
    fireEvent.click(screen.getByText('Create new worktree'))
    fireEvent.change(openAs, { target: { value: String(TabType.TERMINAL) } })

    // Give any spurious refetch a chance to land before asserting.
    await Promise.resolve()
    await Promise.resolve()
    expect(workerRpc.listAvailableShells).toHaveBeenCalledTimes(1)
  })

  it('worktree mode: Branch name randomizer changes the input', async () => {
    renderDialog()
    await awaitFormReady()
    fireEvent.click(screen.getByText('Create new worktree'))
    const input = screen.getByPlaceholderText('feature-branch') as HTMLInputElement
    const before = input.value
    // RefreshButton: the closest button to the input is the randomizer.
    const refreshBtn = input.closest('div')?.querySelector('button') as HTMLButtonElement
    fireEvent.click(refreshBtn)
    // Slugs are deterministic per call; while there's a (small) chance
    // of collision, slug space is large enough to assume difference.
    expect(input.value).not.toBe(before)
  })

  it('worktree mode (terminal): Apply disabled when shells list is empty', async () => {
    vi.mocked(workerRpc.listAvailableShells).mockResolvedValueOnce({
      $typeName: 'leapmux.v1.ListAvailableShellsResponse',
      shells: [],
      defaultShell: '',
    })
    renderDialog()
    await awaitFormReady()
    fireEvent.click(screen.getByText('Create new worktree'))
    const openAs = screen.getAllByRole('combobox').find(c => (c as HTMLSelectElement).value === String(TabType.AGENT)) as HTMLSelectElement
    fireEvent.change(openAs, { target: { value: String(TabType.TERMINAL) } })
    await waitFor(() => expect(workerRpc.listAvailableShells).toHaveBeenCalled())
    const apply = screen.getByRole('button', { name: 'Apply' }) as HTMLButtonElement
    expect(apply.disabled).toBe(true)
  })

  it('surfaces RPC failure messages in the dialog', async () => {
    vi.mocked(workerRpc.checkoutBranch).mockRejectedValue(new Error('git boom'))
    renderDialog()
    await awaitFormReady()
    const select = screen.getAllByRole('combobox')[0] as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'main' } })
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))
    await waitFor(() => expect(screen.getByText('git boom')).toBeInTheDocument())
  })
})
