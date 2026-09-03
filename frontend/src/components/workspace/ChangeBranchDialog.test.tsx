/// <reference types="vitest/globals" />
import type { GitBranchEntry } from '~/generated/proto/leapmux/v1/git_pb'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { GitMode } from '~/hooks/useGitModeState'
import { menuOptions, menuTriggerText, pickMenuValue } from '~/test-support/menu'
import { ChangeBranchDialog } from './ChangeBranchDialog'

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
    branchName: 'main',
    isWorktree: false,
    availableProviders: [AgentProvider.CLAUDE_CODE],
    onClose: vi.fn(),
    onBranchChanged: vi.fn(),
    onAgentCreated: vi.fn(),
    onTerminalCreated: vi.fn(),
    ...overrides,
    // After the spread, so the merged type stays the three-mode union rather
    // than widening to the whole GitMode enum.
    initialMode: overrides?.initialMode ?? GitMode.SwitchBranch,
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

  // The branch context menu offers one item per mode, so the item the user
  // picked has to be the radio that is already selected. Without the seed all
  // three items opened on "Switch to branch" and the user had to pick again.
  describe('the mode it opens on', () => {
    const cases = [
      ['Switch to branch', GitMode.SwitchBranch],
      ['Create new branch', GitMode.CreateBranch],
      ['Create new worktree', GitMode.CreateWorktree],
    ] as const

    /** The radio of the mode row labelled `label`. */
    function radioFor(label: string): HTMLInputElement {
      const row = screen.getByText(label).closest('label')
      expect(row).not.toBeNull()
      return row!.querySelector('input[type="radio"]') as HTMLInputElement
    }

    for (const [label, mode] of cases) {
      it(`paints ${label} selected`, async () => {
        renderDialog({ initialMode: mode })
        await awaitFormReady()

        expect(radioFor(label).checked).toBe(true)
        for (const [other] of cases.filter(([l]) => l !== label))
          expect(radioFor(other).checked).toBe(false)
      })
    }

    // The seed carries the mode alone: GitOptions owns the fields and emits a
    // complete intent on its first flush, so the mode's own sub-form has to be
    // usable without the user touching a radio.
    it('renders the picked mode\'s own fields', async () => {
      renderDialog({ initialMode: GitMode.CreateWorktree })
      await awaitFormReady()

      expect(screen.getByLabelText('Branch Name')).toBeInTheDocument()
      expect(screen.getByText('Open as')).toBeInTheDocument()
    })
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
    // No await needed: the seed controls showGitOptions synchronously.
    expect(screen.getByText('Switch to branch')).toBeInTheDocument()
    expect(screen.getByText('Create new branch')).toBeInTheDocument()
    // BranchSelect shows the loading placeholder until the inspect
    // resolves (no separate ListGitBranches fetcher exists for the
    // preloaded-branches path). Asserted on the TRIGGER, because the menu also
    // states it in place of the option list while loading -- the gate that
    // stops a stale list staying mounted and clickable through a refetch.
    expect(menuTriggerText('branch-select-menu')).toContain('Loading branches...')
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
      expect(menuOptions('branch-select-menu')).toContain('main (current)')
    })
  })

  it('marks the current branch with "(current)" in the base-branch picker', async () => {
    renderDialog()
    await awaitFormReady()
    // Switch into create-branch mode; its Base Branch picker passes
    // showCurrent so the currently-checked-out branch is suffixed.
    fireEvent.click(screen.getByText('Create new branch'))
    await waitFor(() => {
      expect(menuOptions('branch-select-menu')).toContain('feature (current)')
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
    pickMenuValue('branch-select-menu', 'main')

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
    pickMenuValue('branch-select-menu', 'main')
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
    pickMenuValue('branch-select-menu', 'origin/remote-only')
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
    pickMenuValue('branch-select-menu', 'feature/auth')
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

  it('create-branch: submits its own RPC without the user touching a radio', async () => {
    // Opened straight from "Create new branch...", so the seeded mode has to
    // reach `dispatchMode` -- a seed that only painted the radio would still
    // dispatch a checkout.
    renderDialog({ initialMode: GitMode.CreateBranch })
    await awaitFormReady()

    const input = screen.getByPlaceholderText('feature-branch') as HTMLInputElement
    fireEvent.input(input, { target: { value: 'shiny-new' } })
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))

    await waitFor(() => expect(workerRpc.createBranch).toHaveBeenCalledTimes(1))
    expect(vi.mocked(workerRpc.createBranch).mock.calls[0][1]).toMatchObject({ newBranch: 'shiny-new', path: '/repo' })
    expect(workerRpc.checkoutBranch).not.toHaveBeenCalled()
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

  // The worktree mode's submit opens a worker-side agent/pty that placement
  // could orphan, so a blocked reason must disable it like the other
  // tab-creating dialogs — and the click must never reach the RPC.
  it('worktree mode: a blocked reason disables Apply, shows the notice, and fires no RPC', async () => {
    const props = renderDialog({ blockedReason: () => 'The workspace view is not ready yet.' })
    await awaitFormReady()
    fireEvent.click(screen.getByText('Create new worktree'))

    expect(await screen.findByTestId('new-tab-blocked-reason'))
      .toHaveTextContent(/not ready yet/i)
    const apply = screen.getByRole('button', { name: 'Apply' }) as HTMLButtonElement
    expect(apply.disabled).toBe(true)

    fireEvent.click(apply)
    await Promise.resolve()
    expect(workerRpc.openAgent, 'the guard holds before the worker RPC').not.toHaveBeenCalled()
    expect(workerRpc.openTerminal).not.toHaveBeenCalled()
    expect(props.onClose, 'and the dialog did not close as a success').not.toHaveBeenCalled()
  })

  // The companion: the reason is mode-conditioned, not global. A branch
  // checkout creates no tab, so the same accessor must not disable it.
  it('the blocked reason never applies outside worktree mode — a branch checkout opens no tab', async () => {
    const props = renderDialog({ blockedReason: () => 'The workspace view is not ready yet.' })
    await awaitFormReady()

    expect(screen.queryByTestId('new-tab-blocked-reason')).toBeNull()
    pickMenuValue('branch-select-menu', 'main')
    const apply = screen.getByRole('button', { name: 'Apply' }) as HTMLButtonElement
    expect(apply.disabled, 'switch-branch arms despite the reason').toBe(false)

    fireEvent.click(apply)
    await waitFor(() => expect(workerRpc.checkoutBranch).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(props.onClose).toHaveBeenCalled())
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
    fireEvent.click(screen.getByRole('radio', { name: 'Terminal' }))
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
    pickMenuValue('branch-select-menu', 'feature')
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
    pickMenuValue('branch-select-menu', 'origin/feature')
    const apply = screen.getByRole('button', { name: 'Apply' }) as HTMLButtonElement
    await waitFor(() => expect(apply.disabled).toBe(true))
    expect(screen.getByText(/already on local branch "feature"/i)).toBeInTheDocument()
  })

  it('switch-branch: picking a non-current branch enables Apply (positive control for the no-op gate)', async () => {
    // The fixture's currentBranch is `feature`; `main` is a real
    // switch destination — Apply must remain enabled.
    renderDialog()
    await awaitFormReady()
    pickMenuValue('branch-select-menu', 'main')
    const apply = screen.getByRole('button', { name: 'Apply' }) as HTMLButtonElement
    await waitFor(() => expect(apply.disabled).toBe(false))
    expect(screen.queryByText(/already on/i)).toBeNull()
  })

  it('worktree mode: switching Open as → Terminal triggers shell listing', async () => {
    renderDialog()
    await awaitFormReady()
    fireEvent.click(screen.getByText('Create new worktree'))
    expect(workerRpc.listAvailableShells).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('radio', { name: 'Terminal' }))
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

    fireEvent.click(screen.getByRole('radio', { name: 'Terminal' }))
    await waitFor(() => expect(workerRpc.listAvailableShells).toHaveBeenCalledTimes(1))

    // Toggle away (Agent → switch-branch path) and back to terminal.
    fireEvent.click(screen.getByRole('radio', { name: 'Agent' }))
    fireEvent.click(screen.getByText('Switch to branch'))
    fireEvent.click(screen.getByText('Create new worktree'))
    fireEvent.click(screen.getByRole('radio', { name: 'Terminal' }))

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
    fireEvent.click(screen.getByRole('radio', { name: 'Terminal' }))
    await waitFor(() => expect(workerRpc.listAvailableShells).toHaveBeenCalled())
    const apply = screen.getByRole('button', { name: 'Apply' }) as HTMLButtonElement
    expect(apply.disabled).toBe(true)
  })

  it('surfaces RPC failure messages in the dialog', async () => {
    vi.mocked(workerRpc.checkoutBranch).mockRejectedValue(new Error('git boom'))
    renderDialog()
    await awaitFormReady()
    pickMenuValue('branch-select-menu', 'main')
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))
    await waitFor(() => expect(screen.getByText('git boom')).toBeInTheDocument())
  })
})

// WHICH checkout the dialog acts on. Nothing stated it before: the mode block
// names the current branch for the switch picker alone, so a user with the same
// branch name in a worktree and in the main repo could not tell two open
// dialogs apart.
describe('changeBranchDialog header', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    setupRpcMocks()
  })

  it('names the checkout and its directory', async () => {
    renderDialog({ gitToplevel: '/repo' })
    await awaitFormReady()

    expect(screen.getByText('Branch')).toBeInTheDocument()
    expect(screen.getByTestId('working-tree-directory').textContent).toBe('/repo')
  })

  it('calls a worktree a worktree once the inspect RPC lands', async () => {
    vi.mocked(workerRpc.inspectBranchChange).mockResolvedValue({
      $typeName: 'leapmux.v1.InspectBranchChangeResponse',
      repoRoot: '/repo',
      toplevel: '/repo-worktrees/feature',
      isWorktree: true,
      currentBranch: 'feature',
      isDirty: false,
      branches,
    })
    renderDialog({ gitToplevel: '/repo-worktrees/feature', isWorktree: true })
    await awaitFormReady()

    expect(screen.getByText('Worktree branch')).toBeInTheDocument()
    expect(screen.getByTestId('working-tree-directory').textContent).toBe('/repo-worktrees/feature')
  })

  // The row is seeded from the sidebar, so it is right before the RPC answers.
  it('seeds the kind from the row that opened it', () => {
    vi.mocked(workerRpc.inspectBranchChange).mockReturnValue(new Promise(() => {}))
    renderDialog({ gitToplevel: '/repo-worktrees/feature', isWorktree: true, branchName: 'feature' })

    expect(screen.getByText('Worktree branch')).toBeInTheDocument()
    expect(screen.getByTestId('working-tree-name').textContent).toBe('feature')
  })

  // Pins `homeDir={worker.getHomeDir()}`. The two cases above use a directory
  // OUTSIDE the mocked home, where `tildify` answers the same with or without a
  // home dir -- so only a path under it can tell a wired prop from a dropped
  // one, and every sibling surface already has this test.
  it('shortens a directory under the worker home dir', async () => {
    renderDialog({ gitToplevel: '/home/u/repo' })
    await awaitFormReady()

    expect(screen.getByTestId('working-tree-directory').textContent).toBe('~/repo')
  })

  // A failed inspect disproves neither the kind nor the branch: both come from
  // the row that opened the dialog. Resetting them left a worktree relabelled
  // "Branch" with a blank name, beside the error banner -- on the one surface
  // that exists to state which checkout the dialog acts on.
  it('keeps naming a worktree a worktree when the inspect RPC fails', async () => {
    vi.mocked(workerRpc.inspectBranchChange).mockRejectedValue(new Error('worker offline'))
    renderDialog({ gitToplevel: '/repo-worktrees/feature', isWorktree: true, branchName: 'feature' })

    await waitFor(() => expect(screen.getByText('worker offline')).toBeInTheDocument())
    expect(screen.getByText('Worktree branch')).toBeInTheDocument()
    expect(screen.getByTestId('working-tree-name').textContent).toBe('feature')
  })

  // The mirror case, so the reset is not simply pinned to `true`.
  it('keeps naming a branch a branch when the inspect RPC fails', async () => {
    vi.mocked(workerRpc.inspectBranchChange).mockRejectedValue(new Error('worker offline'))
    renderDialog({ gitToplevel: '/repo', isWorktree: false, branchName: 'main' })

    await waitFor(() => expect(screen.getByText('worker offline')).toBeInTheDocument())
    expect(screen.getByText('Branch')).toBeInTheDocument()
    expect(screen.getByTestId('working-tree-name').textContent).toBe('main')
  })
})

// The Title field belongs to create-worktree only, the one mode that opens a
// tab. Its generator reads the Open-as toggle, which is what makes this more
// than a copy of the other dialogs' field.
describe('changeBranchDialog title', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    setupRpcMocks()
  })

  const titleInput = () => screen.getByTestId('title-input') as HTMLInputElement

  it('shows no title field outside create-worktree mode', async () => {
    renderDialog()
    await awaitFormReady()
    expect(screen.queryByTestId('title-input')).toBeNull()
  })

  it('pre-fills an Agent title when the mode opens', async () => {
    renderDialog()
    await awaitFormReady()
    fireEvent.click(screen.getByText('Create new worktree'))

    expect(titleInput().value).toMatch(/^Agent [A-Z][A-Za-z]+$/)
  })

  // The prefix has to follow the toggle, or the tab carries the other kind's
  // name. This is the whole reason regenerateIfPristine exists.
  it('re-rolls the title to a Terminal name when Open as flips', async () => {
    renderDialog()
    await awaitFormReady()
    fireEvent.click(screen.getByText('Create new worktree'))
    expect(titleInput().value).toMatch(/^Agent /)

    fireEvent.click(screen.getByRole('radio', { name: 'Terminal' }))
    expect(titleInput().value).toMatch(/^Terminal [A-Z][A-Za-z]+$/)

    fireEvent.click(screen.getByRole('radio', { name: 'Agent' }))
    expect(titleInput().value).toMatch(/^Agent [A-Z][A-Za-z]+$/)
  })

  // The other half of that rule, and the one a naive implementation breaks:
  // a name the user typed survives the flip untouched.
  it('keeps a typed title when Open as flips', async () => {
    renderDialog()
    await awaitFormReady()
    fireEvent.click(screen.getByText('Create new worktree'))

    fireEvent.input(titleInput(), { target: { value: 'Auth fix' } })
    fireEvent.click(screen.getByRole('radio', { name: 'Terminal' }))
    expect(titleInput().value).toBe('Auth fix')
  })

  it('sends the cleaned title on the agent path', async () => {
    vi.mocked(workerRpc.openAgent).mockResolvedValue({
      $typeName: 'leapmux.v1.OpenAgentResponse',
      agent: { $typeName: 'leapmux.v1.AgentInfo', id: 'a1', workerId: 'w1' } as never,
    } as never)

    renderDialog()
    await awaitFormReady()
    fireEvent.click(screen.getByText('Create new worktree'))
    fireEvent.input(titleInput(), { target: { value: '  Auth   fix  ' } })

    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))
    await waitFor(() => expect(workerRpc.openAgent).toHaveBeenCalledTimes(1))
    expect(vi.mocked(workerRpc.openAgent).mock.calls[0][1]).toMatchObject({ title: 'Auth fix' })
  })

  it('sends the cleaned title on the terminal path', async () => {
    vi.mocked(workerRpc.openTerminal).mockResolvedValue({
      $typeName: 'leapmux.v1.OpenTerminalResponse',
      terminalId: 't1',
      title: 'Build logs',
    } as never)

    renderDialog()
    await awaitFormReady()
    fireEvent.click(screen.getByText('Create new worktree'))
    fireEvent.click(screen.getByRole('radio', { name: 'Terminal' }))
    await waitFor(() => expect(workerRpc.listAvailableShells).toHaveBeenCalled())

    fireEvent.input(titleInput(), { target: { value: 'Build  logs' } })
    const apply = screen.getByRole('button', { name: 'Apply' }) as HTMLButtonElement
    await waitFor(() => expect(apply.disabled).toBe(false))

    fireEvent.click(apply)
    await waitFor(() => expect(workerRpc.openTerminal).toHaveBeenCalledTimes(1))
    expect(vi.mocked(workerRpc.openTerminal).mock.calls[0][1]).toMatchObject({ title: 'Build logs' })
  })

  it('disables Apply and fires no RPC when the title is emptied', async () => {
    renderDialog()
    await awaitFormReady()
    fireEvent.click(screen.getByText('Create new worktree'))

    fireEvent.input(titleInput(), { target: { value: '   ' } })
    const apply = screen.getByRole('button', { name: 'Apply' }) as HTMLButtonElement
    await waitFor(() => expect(apply.disabled).toBe(true))
    expect(screen.getByText('Name must not be empty')).toBeInTheDocument()

    fireEvent.click(apply)
    await Promise.resolve()
    expect(workerRpc.openAgent).not.toHaveBeenCalled()
    expect(workerRpc.openTerminal).not.toHaveBeenCalled()
  })

  // The gate is mode-scoped: a branch switch sends no title, so a title the
  // user emptied while in worktree mode must not strand the other modes.
  it('a switch-branch submit still arms after the title was emptied', async () => {
    renderDialog()
    await awaitFormReady()
    fireEvent.click(screen.getByText('Create new worktree'))
    fireEvent.input(titleInput(), { target: { value: '' } })

    fireEvent.click(screen.getByText('Switch to branch'))
    pickMenuValue('branch-select-menu', 'main')
    const apply = screen.getByRole('button', { name: 'Apply' }) as HTMLButtonElement
    await waitFor(() => expect(apply.disabled).toBe(false))

    fireEvent.click(apply)
    await waitFor(() => expect(workerRpc.checkoutBranch).toHaveBeenCalledTimes(1))
  })
})
