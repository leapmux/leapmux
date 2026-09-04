import type { AgentInfo } from '~/generated/proto/leapmux/v1/agent_pb'
import { render, screen } from '@solidjs/testing-library'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createControlStore } from '~/stores/control.store'
import { repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { stubBranchMenuActions } from '~/test-support/branchMenu'
import { hoverForTooltip } from '~/test-support/clipStub'
import { AgentEditorPanel } from './AgentEditorPanel'
import '~/components/chat/providers'

const HOME = '/home/dev'
const WORKTREE_DIR = '/home/dev/Workspaces/r-worktrees/feature'

// The panel reads the home directory from the WORKER STORE. It used to read
// `props.agent.homeDir`, which `agentTabToInfo` hard-codes to '' on every path
// that renders this panel -- so nothing the composer showed ever shortened.
vi.mock('~/stores/workerInfo.store', () => ({
  workerInfoStore: {
    fetchWorkerInfo: vi.fn().mockResolvedValue(undefined),
    workerInfo: () => null,
    getHomeDir: (workerId: string) => (workerId === 'w1' ? HOME : ''),
    getOs: () => 'linux',
  },
}))

beforeAll(() => {
  HTMLElement.prototype.showPopover = vi.fn()
  HTMLElement.prototype.hidePopover = vi.fn()
  HTMLElement.prototype.togglePopover = vi.fn()
})

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
})

function agent(overrides: Partial<AgentInfo> = {}): AgentInfo {
  return {
    agentProvider: AgentProvider.CLAUDE_CODE,
    workerId: 'w1',
    // What `agentTabToInfo` really builds: a Tab row carries no home dir.
    homeDir: '',
    optionGroups: [],
    ...overrides,
  } as unknown as AgentInfo
}

function renderPanel(workerId = 'w1', controlStore?: ReturnType<typeof createControlStore>) {
  const repoGitStore = createRepoGitStore()
  const gitTab = { workerId, gitToplevel: WORKTREE_DIR }
  repoGitStore.upsert(repoKey(workerId, WORKTREE_DIR), {
    branch: 'feature',
    toplevel: WORKTREE_DIR,
    isWorktree: true,
    originUrl: 'https://github.com/o/r.git',
  })
  render(() => (
    <PreferencesProvider>
      <AgentEditorPanel
        agentId="a1"
        agent={agent({ workerId })}
        repoGitStore={repoGitStore}
        gitTab={gitTab}
        onSendMessage={() => {}}
        controlRequests={controlStore?.getRequests('a1')}
        branchActions={stubBranchMenuActions()}
        branchWorkerId={workerId}
      />
    </PreferencesProvider>
  ))
}

function addControlRequest(
  controlStore: ReturnType<typeof createControlStore>,
  requestId: string,
  toolName: string,
) {
  controlStore.addRequest('a1', {
    requestId,
    agentId: 'a1',
    payload: {
      request: { tool_name: toolName, input: {} },
    },
  })
}

describe('agentEditorPanel control request lifecycle', () => {
  it('removes the active control request without reading a null request', () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, 'plan-1', 'ExitPlanMode')
    renderPanel('w1', controlStore)

    expect(screen.getByTestId('control-banner')).toBeInTheDocument()
    expect(screen.getByTestId('plan-approve-btn')).toBeInTheDocument()
    expect(screen.getByTestId('composer-footer-slot')).toHaveAttribute('data-full-width')

    expect(() => controlStore.removeRequest('a1', 'plan-1')).not.toThrow()

    expect(screen.queryByTestId('control-banner')).not.toBeInTheDocument()
    expect(screen.queryByTestId('plan-approve-btn')).not.toBeInTheDocument()
    expect(screen.getByTestId('composer-footer-slot')).not.toHaveAttribute('data-full-width')
  })

  it('renders the next queued control request after removing the active request', () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, 'plan-1', 'ExitPlanMode')
    addControlRequest(controlStore, 'bash-1', 'Bash')
    renderPanel('w1', controlStore)

    expect(screen.getByTestId('plan-approve-btn')).toBeInTheDocument()

    controlStore.removeRequest('a1', 'plan-1')

    expect(screen.getByTestId('control-banner')).toHaveTextContent(/Permission Required:\s*Bash/)
    expect(screen.queryByTestId('plan-approve-btn')).not.toBeInTheDocument()
    expect(screen.getByTestId('control-allow-btn')).toHaveTextContent('Allow')
  })
})

describe('agentEditorPanel working-tree chip', () => {
  // The defect this pins: the chip printed an absolute path while the sidebar
  // row for the SAME checkout printed a tilde one, because the panel read the
  // home dir off a field nothing populates.
  it('shortens the chip tooltip directory against the worker home dir', () => {
    renderPanel()

    const tooltip = hoverForTooltip(screen.getByTestId('composer-branch-trigger'))
    expect(tooltip).not.toBeNull()
    expect(tooltip!.querySelector('[data-testid="working-tree-directory"]')!.textContent)
      .toBe('~/Workspaces/r-worktrees/feature')
  })

  it('names the checkout kind on the chip', () => {
    renderPanel()

    expect(screen.getByTestId('composer-branch-trigger').querySelector('[data-testid="worktree-icon"]'))
      .not
      .toBeNull()
  })

  // A worker the store knows nothing about reports no home dir. The absolute
  // path is correct there; a guessed short one would not be.
  it('leaves the directory absolute for a worker with no system info', () => {
    renderPanel('w-unknown')

    const tooltip = hoverForTooltip(screen.getByTestId('composer-branch-trigger'))
    expect(tooltip!.querySelector('[data-testid="working-tree-directory"]')!.textContent)
      .toBe(WORKTREE_DIR)
  })
})
