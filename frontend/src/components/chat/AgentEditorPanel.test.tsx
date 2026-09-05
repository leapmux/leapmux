import type { AgentEditorPanelProps } from './AgentEditorPanel'
import type { AgentInfo } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { localStorageGet, localStorageSet, PREFIX_ASK_STATE } from '~/lib/browserStorage'
import { loadDraft, saveDraft } from '~/lib/editor/draftPersistence'
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

interface RenderPanelOptions {
  workerId?: string
  controlStore?: ReturnType<typeof createControlStore>
  onControlResponse?: AgentEditorPanelProps['onControlResponse']
}

function renderPanel(options: RenderPanelOptions = {}) {
  const workerId = options.workerId ?? 'w1'
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
        controlRequests={options.controlStore?.getRequests('a1')}
        onControlResponse={options.onControlResponse}
        branchActions={stubBranchMenuActions()}
        branchWorkerId={workerId}
      />
    </PreferencesProvider>
  ))
}

/** A control request payload naming the tool the agent asks permission to run. */
function toolRequestPayload(toolName: string): Record<string, unknown> {
  return { request: { tool_name: toolName, input: {} } }
}

function addControlRequest(
  controlStore: ReturnType<typeof createControlStore>,
  request: Omit<ControlRequest, 'agentId'>,
) {
  controlStore.addRequest('a1', { agentId: 'a1', ...request })
}

// The crash this suite's sibling reproduces (`ControlRequestBanner.test.tsx`)
// cannot occur through the panel, so these are lifecycle tests rather than
// regression tests. `insert()` builds a RENDER effect, which runs in the same
// pure phase as a memo and OWNS the control component. Solid's `runTop` walks
// up to the topmost stale ancestor first, so that effect always disposes the
// component before any memo in its body can re-read the removed request. The
// keyed owners still matter -- they make the request prop non-reactive, so the
// panel no longer depends on that disposal order holding.
describe('agentEditorPanel control request lifecycle', () => {
  it('removes the active control request without reading a null request', () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, { requestId: 'plan-1', payload: toolRequestPayload('ExitPlanMode') })
    renderPanel({ controlStore })

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
    addControlRequest(controlStore, { requestId: 'plan-1', payload: toolRequestPayload('ExitPlanMode') })
    addControlRequest(controlStore, { requestId: 'bash-1', payload: toolRequestPayload('Bash') })
    renderPanel({ controlStore })

    expect(screen.getByTestId('plan-approve-btn')).toBeInTheDocument()

    controlStore.removeRequest('a1', 'plan-1')

    expect(screen.getByTestId('control-banner')).toHaveTextContent(/Permission Required:\s*Bash/)
    expect(screen.queryByTestId('plan-approve-btn')).not.toBeInTheDocument()
    expect(screen.getByTestId('control-allow-btn')).toHaveTextContent('Allow')
  })

  // The switches live in local state inside the actions component, so a remount
  // silently unchecks them. A keyed owner compares the request by IDENTITY, and
  // a queued sibling leaves the answered request's identity alone -- whereas a
  // plain conditional re-reads the whole store list and rebuilds the footer.
  it('keeps the active plan switches checked when a request queues behind it', () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, { requestId: 'plan-1', payload: toolRequestPayload('ExitPlanMode') })
    renderPanel({ controlStore })

    const clearContext = () => screen.getByTestId('plan-clear-context-checkbox').querySelector('input[type="checkbox"]')!
    fireEvent.click(clearContext())
    expect(clearContext()).toBeChecked()

    addControlRequest(controlStore, { requestId: 'bash-1', payload: toolRequestPayload('Bash') })

    expect(screen.getByTestId('control-banner')).toHaveTextContent('Plan Ready for Review')
    expect(clearContext()).toBeChecked()
  })

  // A cancel and re-ask of the SAME request id is a new INSTANCE with a fresh
  // claim token, and the store admits it as one. The footer has to come back
  // empty and answer with the new token. Carrying the switches over would
  // approve a plan the user never saw, with a setting they chose for the
  // instance that went away. (The identity semantics that decide this live in
  // `controlResponseHandling.test.ts`. Here the queue empties between the two
  // writes, so the footer is rebuilt whatever the owner keys on.)
  it('empties the plan switches for a re-ask of the same request id', () => {
    const controlStore = createControlStore()
    const onControlResponse = vi.fn().mockResolvedValue(undefined)
    const plan = (claimToken: string) => ({
      requestId: 'plan-1',
      payload: toolRequestPayload('ExitPlanMode'),
      claimToken,
    })
    addControlRequest(controlStore, plan('claim-1'))
    renderPanel({ controlStore, onControlResponse })

    const clearContext = () => screen.getByTestId('plan-clear-context-checkbox').querySelector('input[type="checkbox"]')!
    fireEvent.click(clearContext())
    expect(clearContext()).toBeChecked()

    controlStore.removeRequest('a1', 'plan-1')
    addControlRequest(controlStore, plan('claim-2'))

    expect(clearContext()).not.toBeChecked()

    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    const [, , content, claimToken] = onControlResponse.mock.calls[0]
    expect(claimToken).toBe('claim-2')
    // `buildAllowResponse` adds the key only for a checked switch, so its
    // absence is what proves the cancelled instance's choice did not carry.
    expect(JSON.parse(new TextDecoder().decode(content as Uint8Array))).not.toHaveProperty('clearContext')
  })

  // The footer answers with the request instance it RENDERED, so the worker's
  // idempotency claim keys on the answered instance. Reading the store again at
  // click time would lose both values as soon as the store moved on.
  it('answers with the rendered request id and its per-instance claim token', () => {
    const controlStore = createControlStore()
    const onControlResponse = vi.fn().mockResolvedValue(undefined)
    addControlRequest(controlStore, {
      requestId: 'plan-1',
      payload: toolRequestPayload('ExitPlanMode'),
      claimToken: 'claim-1',
    })
    renderPanel({ controlStore, onControlResponse })

    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    expect(onControlResponse).toHaveBeenCalledOnce()
    const [agentId, requestId, content, claimToken] = onControlResponse.mock.calls[0]
    expect(agentId).toBe('a1')
    expect(requestId).toBe('plan-1')
    expect(claimToken).toBe('claim-1')
    expect(JSON.parse(new TextDecoder().decode(content as Uint8Array))).toMatchObject({
      response: { request_id: 'plan-1', response: { behavior: 'allow' } },
    })
  })

  // A request that predates the worker's per-instance token carries none. The
  // store then keys its responded mark on the payload instead, so the footer
  // must pass the absent token through rather than substitute a placeholder.
  it('answers with no claim token when the rendered request carries none', () => {
    const controlStore = createControlStore()
    const onControlResponse = vi.fn().mockResolvedValue(undefined)
    addControlRequest(controlStore, { requestId: 'plan-1', payload: toolRequestPayload('ExitPlanMode') })
    renderPanel({ controlStore, onControlResponse })

    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    expect(onControlResponse).toHaveBeenCalledOnce()
    const [, requestId, , claimToken] = onControlResponse.mock.calls[0]
    expect(requestId).toBe('plan-1')
    expect(claimToken).toBeUndefined()
  })

  // The panel's `onControlResponse` is optional, and the chat views that omit it
  // still render the footer. Answering there must resolve rather than throw.
  it('answers without a response handler and still clears the draft', () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, { requestId: 'plan-1', payload: toolRequestPayload('ExitPlanMode') })
    saveDraft('a1-ctrl-plan-1', 'no handler', 0)
    renderPanel({ controlStore })

    expect(() => fireEvent.click(screen.getByTestId('plan-approve-btn'))).not.toThrow()

    expect(loadDraft('a1-ctrl-plan-1').content).toBe('')
  })

  // Answering discards the drafts of the ANSWERED request only: its rejection
  // text, its per-page question answers, and its saved selection state. A draft
  // belonging to a queued sibling must survive, which is what pins that the
  // cleanup reads the rendered request's id and not a wider key.
  it('clears only the answered request drafts and ask state', () => {
    const controlStore = createControlStore()
    const onControlResponse = vi.fn().mockResolvedValue(undefined)
    addControlRequest(controlStore, { requestId: 'plan-1', payload: toolRequestPayload('ExitPlanMode') })
    addControlRequest(controlStore, { requestId: 'bash-1', payload: toolRequestPayload('Bash') })
    saveDraft('a1-ctrl-plan-1', 'rejection reason', 0)
    saveDraft('a1-ctrl-plan-1-q-3', 'page three answer', 0)
    saveDraft('a1-ctrl-bash-1', 'queued sibling reason', 0)
    localStorageSet(`${PREFIX_ASK_STATE}a1:plan-1`, { selections: { 0: ['Postgres'] } })
    localStorageSet(`${PREFIX_ASK_STATE}a1:bash-1`, { selections: { 0: ['MySQL'] } })
    renderPanel({ controlStore, onControlResponse })

    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    expect(loadDraft('a1-ctrl-plan-1').content).toBe('')
    expect(loadDraft('a1-ctrl-plan-1-q-3').content).toBe('')
    expect(localStorageGet(`${PREFIX_ASK_STATE}a1:plan-1`)).toBeUndefined()
    expect(loadDraft('a1-ctrl-bash-1').content).toBe('queued sibling reason')
    expect(localStorageGet(`${PREFIX_ASK_STATE}a1:bash-1`)).toEqual({ selections: { 0: ['MySQL'] } })
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
    renderPanel({ workerId: 'w-unknown' })

    const tooltip = hoverForTooltip(screen.getByTestId('composer-branch-trigger'))
    expect(tooltip!.querySelector('[data-testid="working-tree-directory"]')!.textContent)
      .toBe(WORKTREE_DIR)
  })
})
