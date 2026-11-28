import type { MessageInitShape } from '@bufbuild/protobuf'
import type { AgentEditorPanelProps } from './AgentEditorPanel'
import type { ControlAnswer, ControlAnswerHandler } from './controls/types'
import type { PlanDecision } from '~/generated/proto/leapmux/v1/agent_control_pb'
import type { AgentInfo } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ControlRequest } from '~/stores/control.store'
import { create } from '@bufbuild/protobuf'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@solidjs/testing-library'
import { createSignal, Show } from 'solid-js'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { compactControl } from '~/components/common/CompactControl.css'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { CLAUDE_MODE } from '~/generated/contracts/claude-protocol'
import { AgentActivityState, AgentInfoSchema, AgentInputKind, AgentInputProcessRecoverySchema, AgentInputQueuePauseReason, AgentInputQueueSnapshotSchema, AgentInputState, AgentProvider, AttachmentSchema, AvailableOptionGroupSchema, QueueHeadDispatch } from '~/generated/proto/leapmux/v1/agent_pb'
import { flushStorageWrites, localStorageLoad, localStorageStore, PREFIX_CONTROL_STATE, setStorageAccountForTests } from '~/lib/browserStorage'
import { clearDraft, loadDraft, saveDraft } from '~/lib/editor/draftPersistence'
import { formatResetTimestamp } from '~/lib/rateLimitUtils'
import { createControlStore } from '~/stores/control.store'
import { getEditorRef } from '~/stores/editorRef.store'
import { getActiveChatPanel } from '~/stores/focusedChatPanel.store'
import { repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { stubBranchMenuActions } from '~/test-support/branchMenu'
import { hoverForTooltip } from '~/test-support/clipStub'
import { ALLOW_DENY_OPTIONS, testControlRequest } from '~/test-support/controlRequests'
import { TEST_USER_ID } from '~/test-support/crdtBridge'
import { useTestStorage } from '~/test-support/persistentStorage'
import { createAgentComposerActionStateStore } from './agentComposerActionState'
import { AgentEditorPanel } from './AgentEditorPanel'
import { clearAttachments, getAttachments, setAttachments } from './attachments'
import { queueEditDraftKey } from './composer/draftIdentity'
import { forgetAgentScheduleDrafts } from './composer/scheduleDrafts'
import { localFieldValueOf } from './composer/scheduleSend'
import '~/components/chat/providers'

// The asynchronous storage tier has no in-memory mirror, so these round-trips
// need a database to round-trip through.
useTestStorage()

const HOME = '/home/dev'
const WORKTREE_DIR = '/home/dev/Workspaces/r-worktrees/feature'

// The panel reads the home directory and the worker name from the WORKER
// STORE, because AgentInfo carries neither.
vi.mock('~/stores/workerInfo.store', () => ({
  workerInfoStore: {
    fetchWorkerInfo: vi.fn().mockResolvedValue(undefined),
    workerInfo: () => null,
    getName: (workerId: string) => (workerId === 'w1'
      ? 'build-box-1'
      : ''),
    getHomeDir: (workerId: string) => (workerId === 'w1'
      ? HOME
      : ''),
    getOs: () => 'linux',
  },
}))

beforeAll(() => {
  HTMLElement.prototype.showPopover = vi.fn()
  HTMLElement.prototype.hidePopover = vi.fn()
  HTMLElement.prototype.togglePopover = vi.fn()
})

beforeEach(() => {
  // `setImmediate` stays REAL. fake-indexeddb schedules its request callbacks on
  // it, and the saved-answer record these cases exercise lives in IndexedDB, so
  // freezing it would leave every read pending for the length of the test.
  // Everything the component itself schedules is still under the fake clock.
  vi.useFakeTimers({
    toFake: [
      'setTimeout',
      'clearTimeout',
      'setInterval',
      'clearInterval',
      'Date',
    ],
  })
})

afterEach(async () => {
  vi.useRealTimers()
  for (const id of [
    'a1',
    'a2',
    'a1-queue-owned-a1',
    'a2-queue-owned-a2',
  ]) {
    clearDraft(id)
    clearAttachments(id)
  }
  // The schedule drafts outlive a panel on purpose, so each case drops them.
  await Promise.all([
    forgetAgentScheduleDrafts('a1'),
    forgetAgentScheduleDrafts('a2'),
  ])
})

function agent(overrides: MessageInitShape<typeof AgentInfoSchema> = {}): AgentInfo {
  return create(AgentInfoSchema, {
    agentProvider: AgentProvider.CLAUDE_CODE,
    workerId: 'w1',
    optionGroups: [],
    ...overrides,
  })
}

interface RenderPanelOptions {
  agentSessionInfo?: AgentEditorPanelProps['agentSessionInfo']
  workerId?: string
  agentProvider?: AgentProvider
  controlStore?: ReturnType<typeof createControlStore>
  onControlAnswer?: AgentEditorPanelProps['onControlAnswer']
  onSettingChange?: AgentEditorPanelProps['onSettingChange']
  onInterrupt?: AgentEditorPanelProps['onInterrupt']
  agentActivity?: AgentActivityState
  optionGroups?: AgentInfo['optionGroups']
  inputQueue?: AgentEditorPanelProps['inputQueue']
  onRecoverInputProcess?: AgentEditorPanelProps['onRecoverInputProcess']
  onSetQueuePaused?: AgentEditorPanelProps['onSetQueuePaused']
  turnRetryOffer?: AgentEditorPanelProps['turnRetryOffer']
  promptSuggestion?: AgentEditorPanelProps['promptSuggestion']
  onDismissPromptSuggestion?: AgentEditorPanelProps['onDismissPromptSuggestion']
  onSendMessage?: AgentEditorPanelProps['onSendMessage']
  onRetryPendingSend?: AgentEditorPanelProps['onRetryPendingSend']
  onRetryTurn?: AgentEditorPanelProps['onRetryTurn']
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
  // The app wires the questions state from the control store, so the harness
  // wires the same pair: a store-backed flag and its setter.
  const panelControlStore = options.controlStore
  return render(() => (
    <PreferencesProvider>
      <AgentEditorPanel
        agentId="a1"
        agent={agent({
          workerId,
          agentProvider: options.agentProvider ?? AgentProvider.CLAUDE_CODE,
          ...(options.optionGroups !== undefined
            ? { optionGroups: options.optionGroups }
            : {}),
        })}
        repoGitStore={repoGitStore}
        gitTab={gitTab}
        onSendMessage={options.onSendMessage ?? (() => {})}
        {...(options.agentSessionInfo !== undefined
          ? { agentSessionInfo: options.agentSessionInfo }
          : {})}
        {...(panelControlStore !== undefined
          ? {
              controlRequests: panelControlStore.getRequests('a1'),
              questionsOpen: panelControlStore.questionsOpen('a1'),
              onQuestionsOpenChange: (open: boolean) => panelControlStore.setQuestionsOpen('a1', open),
            }
          : {})}
        {...(options.onControlAnswer !== undefined
          ? { onControlAnswer: options.onControlAnswer }
          : {})}
        {...(options.onSettingChange !== undefined
          ? { onSettingChange: options.onSettingChange }
          : {})}
        {...(options.onInterrupt !== undefined
          ? { onInterrupt: options.onInterrupt }
          : {})}
        {...(options.agentActivity !== undefined
          ? { agentActivity: options.agentActivity }
          : {})}
        {...(options.inputQueue !== undefined
          ? { inputQueue: options.inputQueue }
          : {})}
        {...(options.onRecoverInputProcess !== undefined
          ? { onRecoverInputProcess: options.onRecoverInputProcess }
          : {})}
        {...(options.onSetQueuePaused !== undefined
          ? { onSetQueuePaused: options.onSetQueuePaused }
          : {})}
        {...(options.onRetryPendingSend !== undefined
          ? { onRetryPendingSend: options.onRetryPendingSend }
          : {})}
        {...(options.turnRetryOffer !== undefined
          ? { turnRetryOffer: options.turnRetryOffer }
          : {})}
        {...(options.promptSuggestion !== undefined
          ? { promptSuggestion: options.promptSuggestion }
          : {})}
        {...(options.onDismissPromptSuggestion !== undefined
          ? { onDismissPromptSuggestion: options.onDismissPromptSuggestion }
          : {})}
        {...(options.onRetryTurn !== undefined
          ? { onRetryTurn: options.onRetryTurn }
          : {})}
        branchActions={stubBranchMenuActions()}
        branchWorkerId={workerId}
      />
    </PreferencesProvider>
  ))
}

/** The fields of a control request: its native payload, and the prompt that the Worker reads from it. */
type RequestFields = Pick<ControlRequest, 'payload' | 'prompt'>

/** A permission request for one tool, which the Worker reads with LeapMux's own Allow and Deny. */
function permissionRequest(toolName = 'Bash'): RequestFields {
  return {
    payload: {
      request: { tool_name: toolName, input: {} },
    },
    prompt: {
      kind: 'permission',
      permission: {
        title: toolName,
        options: [...ALLOW_DENY_OPTIONS],
      },
    },
  }
}

/** A plan approval with no plan text, as the Worker reads Claude Code's ExitPlanMode. */
function planRequest(): RequestFields {
  return {
    payload: {
      request: { tool_name: 'ExitPlanMode', input: {} },
    },
    prompt: { kind: 'plan' },
  }
}

/**
 * A two-question request, so the paging and the per-page draft keys the editor
 * really writes are live. `permissionRequest` cannot stand in: `activeDraftKey`
 * adds the `-q-<page>` suffix only for a question request.
 */
function questionRequest(): RequestFields {
  const questions = [
    {
      question: 'Which database?',
      options: [
        { label: 'Postgres' },
        { label: 'MySQL' },
      ],
    },
    {
      question: 'Which runtime?',
      options: [
        { label: 'Bun' },
        { label: 'Node' },
      ],
    },
  ]
  return {
    payload: {
      request: {
        tool_name: 'AskUserQuestion',
        input: { questions },
      },
    },
    prompt: { kind: 'question', questions },
  }
}

describe('agent editor panel question selection notes', () => {
  const question = {
    question: 'Which database?',
    options: [
      { label: 'Postgres' },
      { label: 'SQLite' },
    ],
  }

  it.each([
    {
      label: 'a prompt that preserves notes',
      provider: AgentProvider.GROK_BUILD,
      preserves: true,
    },
    {
      label: 'a prompt with mutually exclusive answers',
      provider: AgentProvider.CLAUDE_CODE,
      preserves: false,
    },
  ])('honors the selection policy of $label after the editor debounce', async ({ provider, preserves }) => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, {
      requestId: 'ask-note',
      claimToken: 'claim-note',
      prompt: {
        kind: 'question',
        questions: [question],
        preservesSelectionNotes: preserves,
      },
    })
    renderPanel({
      agentProvider: provider,
      controlStore,
      onControlAnswer: async () => true,
    })
    await vi.waitFor(() => expect(screen.getByTestId('question-option-Postgres')).toBeVisible())
    fireEvent.click(screen.getByTestId('question-option-Postgres'))
    const option = screen.getByTestId('question-option-Postgres').querySelector('input')
    expect(option).toBeChecked()
    await vi.waitFor(() => expect(getEditorRef('a1')).toBeDefined())
    const editor = getEditorRef('a1')
    if (!editor)
      throw new Error('The mounted editor registered no handle.')
    editor.set('Use version 16')
    // Advance the editor listener and draft timers before checking the selected option.
    await vi.advanceTimersByTimeAsync(1_000)
    expect(editor.get()).toBe('Use version 16')
    if (preserves) {
      expect(option).toBeChecked()
      expect(option).not.toBeDisabled()
      fireEvent.click(screen.getByTestId('question-option-SQLite'))
      expect(screen.getByTestId('question-option-SQLite').querySelector('input')).toBeChecked()
      expect(editor.get()).toBe('Use version 16')
    }
    else {
      expect(option).not.toBeChecked()
      expect(option).toBeDisabled()
    }
  })

  // The user's own requirement: on a multiple choice, the typed text joins the chosen
  // options, so typing leaves the selection in place.
  it('keeps the chosen options of a multiple choice while the editor holds text', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, {
      requestId: 'ask-many',
      claimToken: 'claim-many',
      prompt: {
        kind: 'question',
        questions: [
          { ...question, multiSelect: true },
        ],
      },
    })
    renderPanel({
      controlStore,
      onControlAnswer: async () => true,
    })
    await vi.waitFor(() => expect(screen.getByTestId('question-option-Postgres')).toBeVisible())
    fireEvent.click(screen.getByTestId('question-option-Postgres'))
    await vi.waitFor(() => expect(getEditorRef('a1')).toBeDefined())
    const editor = getEditorRef('a1')
    if (!editor)
      throw new Error('The mounted editor registered no handle.')
    editor.set('MariaDB')
    await vi.advanceTimersByTimeAsync(1_000)
    const option = screen.getByTestId('question-option-Postgres').querySelector('input')
    expect(option).toBeChecked()
    expect(option).not.toBeDisabled()
    expect(editor.get()).toBe('MariaDB')
  })
})

/** The mutable permission-mode catalog that Claude reports. */
function claudePermissionModeGroup(currentValue: string = CLAUDE_MODE.Default): AgentInfo['optionGroups'][number] {
  return create(AvailableOptionGroupSchema, {
    id: 'permissionMode',
    label: 'Approval',
    order: 30,
    mutable: true,
    defaultValue: CLAUDE_MODE.Default,
    currentValue,
    options: Object.values(CLAUDE_MODE).map(mode => ({ id: mode, name: mode })),
  })
}

/** The plan decision of one answer, or a failure for an answer of another kind. */
function planDecisionOf(answer: ControlAnswer | undefined): PlanDecision {
  if (answer?.kind !== 'decision' || answer.decision.kind.case !== 'plan')
    throw new Error('the panel sent no plan decision')
  return answer.decision.kind.value
}

function addControlRequest(
  controlStore: ReturnType<typeof createControlStore>,
  request: Pick<ControlRequest, 'requestId'> & Partial<ControlRequest>,
) {
  controlStore.addRequest('a1', testControlRequest({ agentId: 'a1', ...request }))
}

/** Waits until the saved answer for the active request is ready. */
async function waitForControlActionsReady() {
  await waitFor(() => expect(screen.getByTestId('control-actions')).not.toBeDisabled())
}

// The crash this suite's sibling reproduces (`ControlRequestBanner.test.tsx`)
// cannot occur through the panel, so these are lifecycle tests rather than
// regression tests. Both slots hand their control component ONE request
// instance as a plain value, so no memo in the component's body observes the
// active request and none can re-run with that request removed.
//
// The footer slot also has a second guard, which the banner slot does not.
// `insert()` builds a RENDER effect that OWNS the footer row, and that effect
// reads the active request through the `actions` getter. Solid queues a render
// effect in `Effects`, not in `Updates`, so it does not run in the memo phase.
// `runTop` still runs it first: it walks the owner chain of each stale memo and
// updates the outermost stale ancestor before the memo itself. The banner slot
// has no such ancestor, because `createComponent` untracks the element that its
// prop getter builds.
describe('AgentEditorPanel', () => {
  it('shows the reset-date tooltip for an epoch-zero quota reset', () => {
    renderPanel({
      agentSessionInfo: {
        rateLimits: {
          primary: { status: 'allowed_warning', utilization: 0.9, resetsAt: 0 },
        },
      },
    })
    const warning = within(screen.getByTestId('agent-info-trigger')).getByText('Warning', { exact: true })
    const tooltip = hoverForTooltip(warning)
    expect(tooltip?.textContent).toContain(formatResetTimestamp(0))
  })

  it('shows a calendar reset tooltip for an exceeded account quota', () => {
    const rateLimits = {
      credit: { status: 'exceeded', resetsOn: '2026-11-25' },
    }
    renderPanel({
      agentSessionInfo: { rateLimits },
    })
    const exceeded = within(screen.getByTestId('agent-info-trigger')).getByText('Exceeded', { exact: true })
    expect(hoverForTooltip(exceeded)?.textContent).toContain('Resets on 2026-11-25')
  })

  it('identifies the focused editor before and after the agent changes', () => {
    const repoGitStore = createRepoGitStore()
    let selectAgent: ((id: string) => void) | undefined
    render(() => {
      const [agentId, setAgentId] = createSignal('a1')
      selectAgent = id => setAgentId(id)
      return (
        <PreferencesProvider>
          <AgentEditorPanel agentId={agentId()} agent={agent()} repoGitStore={repoGitStore} onSendMessage={() => {}} />
        </PreferencesProvider>
      )
    })
    const panel = screen.getByTestId('agent-editor-panel')
    expect(panel).toHaveAttribute('data-agent-id', 'a1')
    if (!selectAgent)
      throw new Error('The editor identity fixture did not initialize its agent selector.')
    selectAgent('a2')
    expect(panel).toHaveAttribute('data-agent-id', 'a2')
  })

  it('reports an unavailable response handler without accepting the request', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, {
      requestId: 'permission',
      ...permissionRequest(),
      claimToken: 'claim',
    })
    renderPanel({ controlStore })
    await waitForControlActionsReady()
    fireEvent.click(screen.getByTestId('control-allow-btn'))
    await vi.waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('response handler is unavailable'))
    expect(controlStore.getRequests('a1')).toHaveLength(1)
  })

  it.each([
    {
      label: 'a permission',
      fields: permissionRequest(),
      approve: 'control-allow-btn',
      word: 'Allow',
    },
    {
      label: 'a plan',
      fields: {
        prompt: { kind: 'plan', text: 'Keep the response.' },
      } as const,
      approve: 'plan-approve-btn',
      word: 'Approve',
    },
    {
      label: 'a confirm dialog',
      fields: {
        prompt: {
          kind: 'dialog',
          dialog: { title: 'Confirm the request.', variant: 'confirm' },
        },
      } as const,
      approve: 'control-allow-btn',
      word: 'Approve',
    },
  ])('disables pending decisions and retains the request after failed delivery for $label', async ({ fields, approve, word }) => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, { requestId: 'permission', ...fields, claimToken: 'claim' })
    let resolveDelivery!: () => void
    let rejectDelivery!: (error: Error) => void
    const delivery = new Promise<void>((resolve, reject) => {
      resolveDelivery = resolve
      rejectDelivery = reject
    })
    const onControlAnswer = vi.fn<ControlAnswerHandler>().mockReturnValueOnce(delivery).mockResolvedValue(undefined)
    renderPanel({ controlStore, onControlAnswer })
    try {
      await waitForControlActionsReady()
      fireEvent.click(screen.getByTestId(approve))
      expect(screen.getByTestId('control-actions')).toBeDisabled()
      expect(onControlAnswer).toHaveBeenCalledOnce()
      rejectDelivery(new Error('offline'))
      await vi.waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('offline'))
      expect(screen.getByTestId('control-actions')).not.toBeDisabled()
      expect(controlStore.getRequests('a1')).toHaveLength(1)
      fireEvent.click(screen.getByTestId(approve))
      await vi.waitFor(() => expect(onControlAnswer).toHaveBeenCalledTimes(2))
      expect(screen.queryByRole('alert')).not.toBeInTheDocument()
      expect(screen.getByTestId(approve)).toHaveTextContent(word)
    }
    finally {
      resolveDelivery()
    }
  })

  it('shows a retry offer only when the panel can send the retry', () => {
    const offer = { turnId: 'turn-1', model: 'gpt-5.5' }
    const withoutHandler = renderPanel({ turnRetryOffer: offer })
    expect(screen.queryByTestId('turn-retry-offer-banner')).not.toBeInTheDocument()
    expect(screen.queryByTestId('turn-retry-offer-retry')).not.toBeInTheDocument()
    withoutHandler.unmount()

    renderPanel({
      turnRetryOffer: offer,
      onRetryTurn: vi.fn().mockResolvedValue(true),
    })
    expect(screen.getByTestId('turn-retry-offer-banner')).toBeInTheDocument()
    expect(screen.getByTestId('turn-retry-offer-retry')).toBeInTheDocument()
  })

  it('inserts a suggested next message into the idle composer without sending it', async () => {
    const send = vi.fn()
    const dismiss = vi.fn()
    renderPanel({
      promptSuggestion: { text: 'run the tests' },
      agentActivity: AgentActivityState.IDLE,
      onDismissPromptSuggestion: dismiss,
      onSendMessage: send,
    })
    await vi.waitFor(() => expect(getEditorRef('a1')).toBeDefined())
    await fireEvent.click(screen.getByRole('button', { name: 'Use' }))
    expect(getEditorRef('a1')?.get()).toBe('run the tests')
    expect(send).not.toHaveBeenCalled()
    expect(dismiss).toHaveBeenCalledOnce()
  })

  it('keeps text typed before the suggestion visibility updates', async () => {
    const dismiss = vi.fn()
    renderPanel({
      promptSuggestion: { text: 'run the tests' },
      agentActivity: AgentActivityState.IDLE,
      onDismissPromptSuggestion: dismiss,
    })
    await vi.waitFor(() => expect(getEditorRef('a1')).toBeDefined())
    expect(screen.getByTestId('prompt-suggestion-chip')).toBeVisible()
    getEditorRef('a1')?.insert('h')
    await fireEvent.click(screen.getByRole('button', { name: 'Use' }))
    expect(getEditorRef('a1')?.get()).toBe('h')
    expect(dismiss).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(1_000)
    expect(screen.queryByTestId('prompt-suggestion-chip')).not.toBeInTheDocument()
  })

  it.each([false, true])('hides the queue toggle during a control request and restores its state (paused=%s)', (paused) => {
    const controlStore = createControlStore()
    renderPanel({
      controlStore,
      inputQueue: create(AgentInputQueueSnapshotSchema, {
        pauseReason: paused
          ? AgentInputQueuePauseReason.MANUAL
          : AgentInputQueuePauseReason.UNSPECIFIED,
      }),
    })
    const label = paused ? 'Resume Queue' : 'Pause Queue'
    expect(screen.getByTestId('queue-pause-button')).toHaveAttribute('aria-label', label)
    addControlRequest(controlStore, {
      requestId: 'permission',
      ...permissionRequest(),
    })
    expect(screen.queryByTestId('queue-pause-button')).not.toBeInTheDocument()
    controlStore.removeRequest('a1', 'permission')
    expect(screen.getByTestId('queue-pause-button')).toHaveAttribute('aria-label', label)
  })

  it('removes the active control request without reading a null request', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, {
      requestId: 'plan-1',
      ...planRequest(),
    })
    renderPanel({ controlStore })

    expect(screen.getByTestId('control-banner')).toBeInTheDocument()
    expect(screen.getByTestId('plan-approve-btn')).toBeInTheDocument()
    expect(screen.getByTestId('composer-footer-slot')).toHaveAttribute('data-full-width')

    expect(() => controlStore.removeRequest('a1', 'plan-1')).not.toThrow()

    expect(screen.queryByTestId('control-banner')).not.toBeInTheDocument()
    expect(screen.queryByTestId('plan-approve-btn')).not.toBeInTheDocument()
    expect(screen.getByTestId('composer-footer-slot')).not.toHaveAttribute('data-full-width')
  })

  it('renders the next queued control request after removing the active request', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, {
      requestId: 'plan-1',
      ...planRequest(),
    })
    addControlRequest(controlStore, {
      requestId: 'bash-1',
      ...permissionRequest(),
    })
    renderPanel({ controlStore })

    expect(screen.getByTestId('plan-approve-btn')).toBeInTheDocument()

    controlStore.removeRequest('a1', 'plan-1')

    expect(screen.getByTestId('control-banner')).toHaveTextContent(/Permission Required\s*Bash/)
    expect(screen.queryByTestId('plan-approve-btn')).not.toBeInTheDocument()
    expect(screen.getByTestId('control-allow-btn')).toHaveTextContent('Allow')
  })

  // The switches live in local state inside the actions component, so a remount
  // silently unchecks them. The slot compares the request by IDENTITY, and a
  // queued sibling does not change the answered request's identity. A plain
  // read of the store list would instead rebuild the footer on every write.
  it('keeps the active plan switches checked when a request queues behind it', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, {
      requestId: 'plan-1',
      ...planRequest(),
    })
    renderPanel({ controlStore })

    const clearContext = () => screen.getByTestId('plan-clear-context-checkbox').querySelector('input[type="checkbox"]')!
    await waitForControlActionsReady()
    fireEvent.click(clearContext())
    expect(clearContext()).toBeChecked()

    addControlRequest(controlStore, {
      requestId: 'bash-1',
      ...permissionRequest(),
    })

    expect(screen.getByTestId('control-banner')).toHaveTextContent('Plan Ready for Review')
    expect(clearContext()).toBeChecked()
  })

  // A cancel and re-ask of the SAME request id is a new INSTANCE with a fresh
  // claim token, and the store admits it as one. The footer has to come back
  // empty and answer with the new token. Carrying the switches over would
  // approve a plan the user never saw, with a setting they chose for the
  // instance that went away. (The identity semantics that decide this live in
  // `controlResponseHandling.test.ts`. Here the queue empties between the two
  // writes, so the panel rebuilds the footer whatever the slot keys on.)
  it('empties the plan switches for a re-ask of the same request id', async () => {
    const controlStore = createControlStore()
    const onControlAnswer = vi.fn<ControlAnswerHandler>().mockResolvedValue(undefined)
    const plan = (claimToken: string) => ({
      requestId: 'plan-1',
      ...planRequest(),
      claimToken,
    })
    addControlRequest(controlStore, plan('claim-1'))
    renderPanel({ controlStore, onControlAnswer })

    const clearContext = () => screen.getByTestId('plan-clear-context-checkbox').querySelector('input[type="checkbox"]')!
    await waitForControlActionsReady()
    fireEvent.click(clearContext())
    expect(clearContext()).toBeChecked()

    controlStore.removeRequest('a1', 'plan-1', 'claim-1')
    addControlRequest(controlStore, plan('claim-2'))

    expect(clearContext()).not.toBeChecked()

    await waitForControlActionsReady()
    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    // The click above produced the call; `?? []` is the type-level guard alone.
    const [request, answer] = onControlAnswer.mock.calls[0] ?? []
    expect(request?.claimToken).toBe('claim-2')
    // An unchecked switch is what proves the cancelled instance's choice did not carry.
    expect(planDecisionOf(answer)).toMatchObject({ approve: true, clearContext: false })
  })

  // The footer answers with the request instance it RENDERED, so the worker's
  // idempotency claim keys on the answered instance. Reading the store again at
  // click time would lose both values as soon as the store changed.
  it('answers with the rendered request id and its per-instance claim token', async () => {
    const controlStore = createControlStore()
    const onControlAnswer = vi.fn<ControlAnswerHandler>().mockResolvedValue(undefined)
    addControlRequest(controlStore, {
      requestId: 'plan-1',
      ...planRequest(),
      claimToken: 'claim-1',
    })
    renderPanel({ controlStore, onControlAnswer })

    await waitForControlActionsReady()
    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    expect(onControlAnswer).toHaveBeenCalledOnce()
    // Called once above; `?? []` is the type-level guard alone.
    const [request, answer] = onControlAnswer.mock.calls[0] ?? []
    expect(request?.agentId).toBe('a1')
    expect(request?.requestId).toBe('plan-1')
    expect(request?.claimToken).toBe('claim-1')
    expect(planDecisionOf(answer)).toMatchObject({ approve: true })
  })

  // A request that predates the worker's per-instance token carries none. The
  // store then keys its responded mark on the payload instead, so the footer
  // must pass the absent token through rather than substitute a placeholder.
  it('answers with no claim token when the rendered request carries none', async () => {
    const controlStore = createControlStore()
    const onControlAnswer = vi.fn<ControlAnswerHandler>().mockResolvedValue(undefined)
    addControlRequest(controlStore, {
      requestId: 'plan-1',
      ...planRequest(),
    })
    renderPanel({ controlStore, onControlAnswer })

    await waitForControlActionsReady()
    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    expect(onControlAnswer).toHaveBeenCalledOnce()
    // Called once above; `?? []` is the type-level guard alone.
    const [request] = onControlAnswer.mock.calls[0] ?? []
    expect(request?.requestId).toBe('plan-1')
    expect(request?.claimToken).toBeUndefined()
  })

  // An unavailable handler must not discard a response that the user cannot deliver.
  it('retains the draft when the response handler is unavailable', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, {
      requestId: 'plan-1',
      ...planRequest(),
    })
    saveDraft('a1-ctrl-plan-1', 'no handler', 0)
    renderPanel({ controlStore })

    await waitForControlActionsReady()
    const feedback = await waitFor(() => {
      const button = screen.getByTestId('plan-reject-btn')
      expect(button).toHaveTextContent('Send feedback')
      return button
    })
    expect(() => fireEvent.click(feedback)).not.toThrow()

    await vi.waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('response handler is unavailable'))
    expect((await loadDraft('a1-ctrl-plan-1')).content).toBe('no handler')
  })

  // Answering discards the drafts of the ANSWERED request only: its editor text
  // and its saved selection state. A draft belonging to a queued sibling must
  // survive. That pins the cleanup to the rendered request's id and not to a
  // wider key.
  it('clears only the answered request drafts and ask state', async () => {
    const controlStore = createControlStore()
    const onControlAnswer = vi.fn<ControlAnswerHandler>().mockResolvedValue(undefined)
    addControlRequest(controlStore, {
      requestId: 'plan-1',
      ...planRequest(),
    })
    addControlRequest(controlStore, {
      requestId: 'bash-1',
      ...permissionRequest(),
    })
    saveDraft('a1-ctrl-plan-1', 'rejection reason', 0)
    saveDraft('a1-ctrl-bash-1', 'queued sibling reason', 0)
    // A plan request has no questions, so the editor can never write a page key
    // for it. The cleanup derives the page count from the request, so it must
    // leave this key alone rather than sweep a guessed range of page indices.
    saveDraft('a1-ctrl-plan-1-q-3', 'not a key this request can write', 0)
    localStorageStore(`${PREFIX_CONTROL_STATE}a1:plan-1`, {
      selections: {
        0: ['Postgres'],
      },
    })
    localStorageStore(`${PREFIX_CONTROL_STATE}a1:bash-1`, {
      selections: {
        0: ['MySQL'],
      },
    })
    renderPanel({ controlStore, onControlAnswer })

    await waitForControlActionsReady()
    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    await vi.waitFor(async () => expect((await loadDraft('a1-ctrl-plan-1')).content).toBe(''))
    expect((await loadDraft('a1-ctrl-plan-1-q-3')).content).toBe('not a key this request can write')
    expect(await localStorageLoad(`${PREFIX_CONTROL_STATE}a1:plan-1`)).toBeUndefined()
    expect((await loadDraft('a1-ctrl-bash-1')).content).toBe('queued sibling reason')
    expect(await localStorageLoad(`${PREFIX_CONTROL_STATE}a1:bash-1`)).toEqual({
      selections: {
        0: ['MySQL'],
      },
    })
  })

  // The per-page keys are derived from the question set, not from a fixed range,
  // so the cleanup clears exactly the pages the editor could have written. A
  // plan request has no questions and therefore no page keys at all -- a draft
  // under a page key it could never write must not be swept away with it.
  it('clears every per-page draft of the answered question set', async () => {
    const controlStore = createControlStore()
    const onControlAnswer = vi.fn<ControlAnswerHandler>().mockResolvedValue(undefined)
    addControlRequest(controlStore, {
      requestId: 'ask-1',
      ...questionRequest(),
    })
    addControlRequest(controlStore, {
      requestId: 'bash-1',
      ...permissionRequest(),
    })
    saveDraft('a1-ctrl-ask-1-q-0', 'note for the first question', 0)
    saveDraft('a1-ctrl-ask-1-q-1', 'note for the second question', 0)
    saveDraft('a1-ctrl-bash-1', 'queued sibling reason', 0)
    expect(await localStorageStore(`${PREFIX_CONTROL_STATE}a1:ask-1`, {
      selections: {
        0: ['Postgres'],
        1: ['Bun'],
      },
      currentPage: 1,
    }).durable).toBe(true)
    renderPanel({ controlStore, onControlAnswer })

    await waitForControlActionsReady()
    fireEvent.click(screen.getByTestId('control-submit-btn'))

    expect(onControlAnswer).toHaveBeenCalledOnce()
    expect((await loadDraft('a1-ctrl-ask-1-q-0')).content).toBe('')
    expect((await loadDraft('a1-ctrl-ask-1-q-1')).content).toBe('')
    expect((await loadDraft('a1-ctrl-bash-1')).content).toBe('queued sibling reason')
  })

  // A remount is routine: the composer is rebuilt whenever the focused agent
  // changes, because `AppShell` renders it through a getter on `focusedAgentId`.
  // The switch belongs to the request INSTANCE, not to the component that drew
  // it, so it must come back checked -- otherwise Approve silently omits the
  // choice the user made.
  // The panel's preset memo follows the composer menu's rule: a preset is
  // offered only when the live catalog carries every axis it sets. Claude's
  // presets both switch permissionMode, so the pill group appears exactly when
  // the catalog carries that group.
  it('draws the permission pills only while the catalog carries the preset axes', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, {
      requestId: 'plan-1',
      ...planRequest(),
      claimToken: 'claim-1',
    })
    renderPanel({
      controlStore,
      onSettingChange: vi.fn(),
      optionGroups: [
        claudePermissionModeGroup(),
      ],
    })
    await waitFor(() => expect(screen.getByRole('radiogroup', { name: 'Permissions' })).toBeInTheDocument())

    // The catalog drops the permissionMode group: no preset the banner could
    // offer is applicable, so the group must disappear rather than draw pills
    // that would silently do nothing.
    cleanup()
    renderPanel({
      controlStore,
      onSettingChange: vi.fn(),
      optionGroups: [],
    })
    await waitFor(() => expect(screen.queryByTestId('control-permissions-pill-group')).not.toBeInTheDocument())
  })

  it('draws plan permission choices without a settings change handler', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, {
      requestId: 'plan-1',
      ...planRequest(),
      claimToken: 'claim-1',
    })

    renderPanel({
      controlStore,
      optionGroups: [
        claudePermissionModeGroup(),
      ],
    })

    await waitFor(() => expect(screen.getByRole('radiogroup', { name: 'Permissions' })).toBeInTheDocument())
    expect(within(screen.getByRole('radiogroup', { name: 'Permissions' })).getByRole('radio', { name: 'Smart' })).toBeChecked()
  })

  // The panel is the only scope that holds BOTH halves the opening choice needs
  // -- the live catalog and the confirmed values -- so it is the only place the
  // wiring from `activePermissionPreset` to the drawn pill can be checked.
  it('opens an ordinary request on the preset the session already has on', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, {
      requestId: 'bash-1',
      ...permissionRequest(),
      claimToken: 'claim-1',
    })
    const pill = () => within(screen.getByRole('radiogroup', { name: 'Permissions' }))

    // The session runs on Claude's bypass mode, so the group opens there and an
    // Allow keeps it there instead of dropping the session back to asking.
    const running = renderPanel({
      controlStore,
      onSettingChange: vi.fn(),
      optionGroups: [
        claudePermissionModeGroup(CLAUDE_MODE.BypassPermissions),
      ],
    })
    await waitFor(() => expect(pill().getByRole('radio', { name: 'Bypass' })).toBeChecked())

    // Nothing on: the group opens on the option that changes nothing. Bypass
    // must never arrive without the user choosing it.
    running.unmount()
    cleanup()
    renderPanel({
      controlStore,
      onSettingChange: vi.fn(),
      optionGroups: [
        claudePermissionModeGroup(),
      ],
    })
    await waitFor(() => expect(pill().getByRole('radio', { name: 'Unchanged' })).toBeChecked())
    expect(pill().getByRole('radio', { name: 'Bypass' })).not.toBeChecked()
  })

  // The pill choice belongs to the request INSTANCE like a switch: a rebuild of
  // the control component must not reset it to Unchanged.
  it('restores the permission pill choice of the rendered request instance after a remount', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, {
      requestId: 'plan-1',
      ...planRequest(),
      claimToken: 'claim-1',
    })
    const bypassRadio = () => within(screen.getByRole('radiogroup', { name: 'Permissions' })).getByRole('radio', { name: 'Bypass' })
    const first = renderPanel({
      controlStore,
      onSettingChange: vi.fn(),
      optionGroups: [
        claudePermissionModeGroup(),
      ],
    })

    await waitForControlActionsReady()
    fireEvent.click(bypassRadio())
    expect(bypassRadio()).toBeChecked()

    first.unmount()
    renderPanel({
      controlStore,
      onSettingChange: vi.fn(),
      optionGroups: [
        claudePermissionModeGroup(),
      ],
    })

    // Polled: the saved record is read after the remount, not during it.
    await waitFor(() => expect(bypassRadio()).toBeChecked())
  })

  it('blocks plan approval until the saved permission choice restores', async () => {
    const controlStore = createControlStore()
    const onControlAnswer = vi.fn<ControlAnswerHandler>().mockResolvedValue(undefined)
    addControlRequest(controlStore, {
      requestId: 'plan-1',
      ...planRequest(),
      claimToken: 'claim-1',
    })
    const key = `${PREFIX_CONTROL_STATE}a1:plan-1:claim-1`
    expect(await localStorageStore(key, {
      choices: { 'control-permissions-pill': 'bypass' },
    }).durable).toBe(true)

    renderPanel({
      controlStore,
      onControlAnswer,
      onSettingChange: vi.fn(),
      optionGroups: [
        claudePermissionModeGroup(),
      ],
    })

    const approve = screen.getByTestId('plan-approve-btn')
    expect(approve).toBeDisabled()
    fireEvent.click(approve)
    expect(onControlAnswer).not.toHaveBeenCalled()

    await waitFor(() => expect(within(screen.getByRole('radiogroup', { name: 'Permissions' })).getByRole('radio', { name: 'Bypass' })).toBeChecked())
    expect(approve).not.toBeDisabled()
    fireEvent.click(approve)

    await waitFor(() => expect(onControlAnswer).toHaveBeenCalledOnce())
    // Called once above; `?? []` is the type-level guard alone.
    const [, answer] = onControlAnswer.mock.calls[0] ?? []
    expect(planDecisionOf(answer)).toMatchObject({ approve: true, permissionMode: CLAUDE_MODE.BypassPermissions })
  })

  it('restores the plan switches of the rendered request instance after a remount', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, {
      requestId: 'plan-1',
      ...planRequest(),
      claimToken: 'claim-1',
    })
    const clearContext = () => screen.getByTestId('plan-clear-context-checkbox').querySelector('input[type="checkbox"]')!
    const first = renderPanel({ controlStore })

    await waitForControlActionsReady()
    fireEvent.click(clearContext())
    expect(clearContext()).toBeChecked()

    first.unmount()
    renderPanel({ controlStore })

    // Polled: the saved record is read after the remount, not during it.
    await waitFor(() => expect(clearContext()).toBeChecked())
  })

  // The same record covers the pill groups that a control request draws. A
  // permission prompt with a session grant, as the Worker reads Codex's, stores
  // its scope selection in that record.
  it('restores the allow scope of a permission prompt after a remount', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, {
      requestId: 'perm-1',
      claimToken: 'claim-1',
      prompt: {
        kind: 'permission',
        permission: {
          options: [
            { optionId: 'turn', kind: 'allow_once', name: 'Allow' },
            {
              optionId: 'session',
              kind: 'allow_always',
              name: 'Allow for Session',
              scope: 'session',
            },
            { optionId: 'deny', kind: 'reject_once', name: 'Deny' },
          ],
        },
      },
    })
    const first = renderPanel({ controlStore, agentProvider: AgentProvider.CODEX })

    await waitForControlActionsReady()
    const sessionChoice = screen.getByRole('radio', { name: 'Session' })
    fireEvent.click(sessionChoice)
    expect(sessionChoice).toBeChecked()

    first.unmount()
    renderPanel({ controlStore, agentProvider: AgentProvider.CODEX })

    // Polled: the saved record is read after the remount, not during it.
    await waitFor(() => expect(screen.getByRole('radio', { name: 'Session' })).toBeChecked())
  })

  // The record is written for EVERY control request now, not only a question,
  // because permission prompts and plan approvals carry saved choices. Answering
  // must therefore discard it: a record that outlived its request would be
  // storage that nothing can ever read again.
  it('discards the persisted switches of the answered request', async () => {
    const controlStore = createControlStore()
    const onControlAnswer = vi.fn<ControlAnswerHandler>().mockResolvedValue(undefined)
    addControlRequest(controlStore, {
      requestId: 'plan-1',
      ...planRequest(),
      claimToken: 'claim-1',
    })
    renderPanel({ controlStore, onControlAnswer })
    const key = `${PREFIX_CONTROL_STATE}a1:plan-1:claim-1`

    await waitForControlActionsReady()
    fireEvent.click(screen.getByTestId('plan-clear-context-checkbox').querySelector('input[type="checkbox"]')!)
    await waitFor(async () => {
      expect((await localStorageLoad<{ switches?: Record<string, boolean> }>(key))?.switches)
        .toEqual({ 'plan-clear-context-checkbox': true })
    })

    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    await vi.waitFor(async () => expect(await localStorageLoad(key)).toBeUndefined())
    // The choice still reached the decision; only the saved copy is gone.
    const [, answer] = onControlAnswer.mock.calls[0] ?? []
    expect(planDecisionOf(answer)).toMatchObject({ approve: true, clearContext: true })
  })

  // A cancel and re-ask reuses the request id with a FRESH claim token, so the
  // queue holds two instances of one id. Answering the head makes the second one
  // active without changing that id. Two things have to key on the instance for
  // this to come back empty: the effect that resets the answers, and the key
  // those answers are saved under. An id alone leaves the new question already
  // answered, and one Submit click then sends what the user chose for the
  // instance that went away.
  it('empties the answers for a question that reuses the request id', async () => {
    const controlStore = createControlStore()
    const original = questionRequest()
    // The re-ask asks a different first question.
    const revisedQuestions = original.prompt.kind === 'question'
      ? original.prompt.questions.map((question, index) => index === 0
          ? { ...question, question: 'Which cache?' }
          : question)
      : []
    addControlRequest(controlStore, {
      requestId: 'ask-1',
      ...original,
      claimToken: 'claim-1',
    })
    addControlRequest(controlStore, {
      requestId: 'ask-1',
      payload: {
        request: {
          tool_name: 'AskUserQuestion',
          input: { questions: revisedQuestions },
        },
      },
      prompt: { kind: 'question', questions: revisedQuestions },
      claimToken: 'claim-2',
    })
    renderPanel({ controlStore })

    fireEvent.click(screen.getByTestId('question-option-Postgres'))
    expect(screen.getByTestId('control-question-group')).toHaveTextContent('Which runtime?')

    controlStore.removeRequest('a1', 'ask-1', 'claim-1')

    expect(screen.getByTestId('control-question-group')).toHaveTextContent('Which cache?')
    expect(screen.getByTestId('control-submit-btn')).toBeDisabled()
  })

  // The saved answers follow the instance too, not only the reset. A remount
  // must restore the instance's OWN page and selections -- that is what the
  // saved copy is for -- and must not restore a sibling instance's.
  it('restores the saved answers of the rendered request instance only', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, {
      requestId: 'ask-1',
      ...questionRequest(),
      claimToken: 'claim-1',
    })
    localStorageStore(`${PREFIX_CONTROL_STATE}a1:ask-1:claim-1`, {
      selections: {
        0: ['MySQL'],
      },
      currentPage: 1,
    })
    localStorageStore(`${PREFIX_CONTROL_STATE}a1:ask-1:claim-2`, {
      selections: {
        0: ['Postgres'],
      },
      currentPage: 0,
    })
    renderPanel({ controlStore })

    await waitFor(() =>
      expect(screen.getByTestId('control-question-group')).toHaveTextContent('Which runtime?'))
  })
})

// What the keyboard layer asks the panel before it claims $mod+Enter. Getting
// this wrong loses messages, so each case pins one input that must count as
// "something to submit".
// A non-blocking question waits for the reader instead of taking the composer
// over. One banner line says it waits; the normal composer stays usable; the
// reader opens the question from the banner and closes it again with "Answer
// later".
describe('agent editor panel pending questions', () => {
  it('keeps native defaults when the sent prompt raises a question before acknowledgement', async () => {
    vi.useRealTimers()
    const controlStore = createControlStore()
    saveDraft('a1', 'Ask the optional languages form.', -1)
    const send = vi.fn(async () => {
      addControlRequest(controlStore, {
        requestId: 'native-languages',
        claimToken: 'native-languages-token',
        payload: {
          type: 'leapmux_ask',
          id: 'native-languages',
          questions: [
            {
              id: 'langs',
              question: 'Languages',
              multi: true,
              allowEmpty: true,
              allowCustomText: true,
              initialSelections: ['0', '2'],
              options: [
                { value: '0', label: 'Go' },
                { value: '1', label: 'Rust' },
              ],
            },
          ],
        },
        // The prompt that the Worker reads from the native form. It drops the
        // default '2', which no option offers.
        prompt: {
          kind: 'question',
          questions: [
            {
              id: 'langs',
              question: 'Languages',
              multiSelect: true,
              allowEmpty: true,
              allowCustomText: true,
              initialSelections: ['0'],
              options: [
                { value: '0', label: 'Go' },
                { value: '1', label: 'Rust' },
              ],
            },
          ],
        },
      })
    })
    renderPanel({ controlStore, agentProvider: AgentProvider.OH_MY_PI, onSendMessage: send })
    await waitFor(() => expect(getEditorRef('a1')?.get()).toBe('Ask the optional languages form.'))
    await waitFor(() => expect(screen.getByTestId('send-button')).not.toBeDisabled())
    fireEvent.click(screen.getByTestId('send-button'))
    await waitFor(() => expect(send).toHaveBeenCalledOnce())
    await waitFor(() => expect(screen.getByTestId('question-option-Go').querySelector('input')).toBeChecked())
    expect(screen.getByTestId('question-option-Rust').querySelector('input')).not.toBeChecked()
  })

  it('rejects a file selection after a blocking request takes the composer', async () => {
    const controlStore = createControlStore()
    renderPanel({ controlStore })
    const input = screen.getByTestId('file-input')
    expect(input).not.toBeDisabled()
    const read = vi.spyOn(FileReader.prototype, 'readAsArrayBuffer').mockImplementation(() => undefined)
    try {
      addControlRequest(controlStore, {
        requestId: 'late-request',
        ...questionRequest(),
      })
      await fireEvent.change(input, {
        target: {
          files: [
            new File(['late'], 'late.txt', { type: 'text/plain' }),
          ],
        },
      })
      expect(read).not.toHaveBeenCalled()
      expect(input).toBeDisabled()
      expect(getAttachments('a1')).toEqual([])
    }
    finally {
      read.mockRestore()
    }
  })

  it('accepts a file selection while a non-blocking question stays closed', async () => {
    vi.useRealTimers()
    const controlStore = createControlStore()
    addControlRequest(controlStore, {
      requestId: 'waiting-question',
      ...questionRequest(),
      nonBlocking: true,
    })
    renderPanel({ controlStore })
    const input = screen.getByTestId('file-input')
    expect(input).not.toBeDisabled()
    await fireEvent.change(input, {
      target: {
        files: [
          new File(['late'], 'late.txt', { type: 'text/plain' }),
        ],
      },
    })
    await waitFor(() => expect(getAttachments('a1')).toHaveLength(1))
    expect(getAttachments('a1')[0]?.filename).toBe('late.txt')
  })

  it('shows the banner and the normal composer while a non-blocking question stays closed', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, {
      requestId: 'q1',
      claimToken: 'tok-q1',
      ...questionRequest(),
      nonBlocking: true,
    })
    renderPanel({ controlStore })

    expect(screen.getByTestId('pending-questions-banner')).toHaveTextContent('The agent asked a question.')
    expect(screen.getByTestId('send-button')).toBeInTheDocument()
    expect(screen.queryByTestId('control-banner')).not.toBeInTheDocument()

    // The one attachment predicate answers through the plus menu: a closed
    // question keeps the composer free, so it accepts files.
    await fireEvent.click(screen.getByTestId('composer-plus-trigger'))
    expect(screen.getByTestId('composer-attach-file')).not.toBeDisabled()
  })

  it('opens the question from the banner and closes it again with Answer later', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, {
      requestId: 'q1',
      claimToken: 'tok-q1',
      ...questionRequest(),
      nonBlocking: true,
    })
    renderPanel({
      controlStore,
      onControlAnswer: async () => true,
    })

    fireEvent.click(screen.getByTestId('pending-questions-answer'))

    await waitFor(() => expect(screen.getByTestId('control-banner')).toBeInTheDocument())
    expect(screen.getByTestId('control-answer-later')).toBeInTheDocument()
    expect(screen.queryByTestId('pending-questions-banner')).not.toBeInTheDocument()

    fireEvent.click(screen.getByTestId('control-answer-later'))

    await waitFor(() => expect(screen.getByTestId('pending-questions-banner')).toBeInTheDocument())
    expect(screen.queryByTestId('control-banner')).not.toBeInTheDocument()
  })

  it('shows no Answer later button for a blocking request', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, {
      requestId: 'b1',
      claimToken: 'tok-b1',
      ...questionRequest(),
    })
    renderPanel({ controlStore })

    await waitFor(() => expect(screen.getByTestId('control-banner')).toBeInTheDocument())
    expect(screen.queryByTestId('control-answer-later')).not.toBeInTheDocument()
  })
})

describe('agent editor panel composer emptiness', () => {
  /**
   * The handle the panel registered, once the editor has installed its live
   * document ref (`getEditorRef`). Uses `vi.waitFor` because `beforeEach`
   * enables Vitest fake timers, which freeze `@testing-library/dom`'s `waitFor`.
   */
  async function registeredHandle() {
    await vi.waitFor(() => {
      expect(getActiveChatPanel()).toBeDefined()
      expect(getEditorRef('a1')).toBeDefined()
    })
    return getActiveChatPanel()!
  }

  it('reports nothing to submit for an untouched composer', async () => {
    renderPanel()
    expect((await registeredHandle()).hasPendingInput()).toBe(false)
  })

  it('reports something to submit once the composer holds text', async () => {
    renderPanel()
    const handle = await registeredHandle()
    getEditorRef('a1')?.set('hello')
    expect(handle.hasPendingInput()).toBe(true)
  })

  /**
   * The regression guard for the whole design.
   *
   * `hasContent` is fed by a listener debounced 200 ms, and the fake timers here
   * are never advanced -- so a `hasPendingInput` built on that signal would
   * still read empty and the steer shortcut would swallow the keypress that was
   * meant to send this text. `insert` dispatches a real ProseMirror transaction
   * WITHOUT calling `onContentChange`, which is exactly the window the user
   * types into.
   *
   * The Send button assertion is what stops the case passing by accident: it
   * proves the two sources genuinely disagree at this instant.
   */
  it('counts a character typed inside the debounce window, because the send path reads the same live document', async () => {
    const { getByRole } = renderPanel()
    const handle = await registeredHandle()
    getEditorRef('a1')?.insert('h')

    expect(handle.hasPendingInput()).toBe(true)
    expect(
      (getByRole('button', { name: 'Send' }) as HTMLButtonElement).disabled,
      'the debounced signal has not caught up yet, which is the point',
    ).toBe(true)
  })

  it('reports something to submit while a control request waits for approval', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, {
      requestId: 'r1',
      ...permissionRequest(),
    })
    renderPanel({ controlStore })
    expect((await registeredHandle()).hasPendingInput()).toBe(true)
  })

  // An empty submit ANSWERS a permission prompt, but it answers an ask-user
  // question with nothing -- so that one is not something to submit.
  it('reports nothing to submit while an ask-user question waits', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, {
      requestId: 'r1',
      ...questionRequest(),
    })
    renderPanel({ controlStore })
    expect((await registeredHandle()).hasPendingInput()).toBe(false)
  })
})

describe('agent editor panel', () => {
  it.each([false, true])('requests recovery for an empty unpaused queue without answering a control (blocking control=%s)', async (blockingControl) => {
    const recovery = create(AgentInputProcessRecoverySchema, {
      outputGeneration: 4n,
      agentSessionId: 'session-4',
      agentProvider: AgentProvider.CODEX,
      processInstanceId: 'process-4',
    })
    const onRecoverInputProcess = vi.fn()
    const onSetQueuePaused = vi.fn(async () => {})
    const controlStore = createControlStore()
    const onControlAnswer = vi.fn<ControlAnswerHandler>(async () => true)
    if (blockingControl) {
      addControlRequest(controlStore, {
        requestId: 'pending-permission',
        ...permissionRequest(),
      })
    }
    renderPanel({
      controlStore,
      onControlAnswer,
      agentProvider: AgentProvider.CODEX,
      inputQueue: create(AgentInputQueueSnapshotSchema, { processRecovery: recovery }),
      onRecoverInputProcess,
      onSetQueuePaused,
    })
    expect(screen.getByTestId('queue-pause-button')).toHaveAttribute('aria-label', 'Recover login')
    await fireEvent.click(screen.getByTestId('queue-pause-button'))
    expect(onRecoverInputProcess).toHaveBeenCalledExactlyOnceWith(recovery)
    expect(onSetQueuePaused).not.toHaveBeenCalled()
    expect(onControlAnswer).not.toHaveBeenCalled()
    expect(controlStore.getRequests('a1')).toHaveLength(blockingControl ? 1 : 0)
  })

  it('shows the queue pause control without a control request', () => {
    renderPanel()
    expect(screen.getByTestId('queue-pause-button')).toHaveTextContent('Pause Queue')
  })

  it('gives the composer actions a name although a phone hides their labels', () => {
    renderPanel()
    // Each label is a `display: none` span below `sm`, and that reaches
    // neither a screen reader nor a by-name lookup. The aria-label does.
    expect(screen.getByTestId('queue-pause-button')).toHaveAttribute('aria-label', 'Pause Queue')
    expect(screen.getByTestId('send-button')).toHaveAttribute('aria-label', 'Send')
  })

  it('shows an icon beside the queue pause label', () => {
    renderPanel()
    expect(screen.getByTestId('queue-pause-button').querySelector('svg')).not.toBeNull()
  })

  it('sizes every composer action with the shared compact style', () => {
    // The footer slot's own rule states no size, so a button that omits this
    // style falls back to the full-size metrics and breaks the row it shares
    // with the `[+]` button, whose height uses the same compact metrics.
    renderPanel({
      agentActivity: AgentActivityState.WORKING,
      onInterrupt: vi.fn(),
    })

    expect(screen.getByTestId('queue-pause-button')).toHaveClass('outline', compactControl)
    expect(screen.getByTestId('interrupt-button')).toHaveClass('outline', compactControl)
    expect(screen.getByTestId('send-button')).toHaveClass(compactControl)
    expect(screen.getByTestId('send-button')).not.toHaveClass('outline')
  })

  it('stops interrupt loading when the request settles and background work keeps the button visible', async () => {
    let finishInterrupt: () => void = () => {}
    const onInterrupt = vi.fn(() => new Promise<void>((resolve) => {
      finishInterrupt = resolve
    }))
    renderPanel({ agentActivity: AgentActivityState.WORKING, onInterrupt })

    const interrupt = screen.getByTestId('interrupt-button')
    fireEvent.click(interrupt)
    expect(interrupt).toBeDisabled()
    expect(interrupt).toHaveTextContent('Interrupting...')

    finishInterrupt()
    await Promise.resolve()
    await vi.advanceTimersByTimeAsync(1_000)

    expect(interrupt).not.toBeDisabled()
    expect(interrupt).toHaveTextContent('Interrupt')
    expect(onInterrupt).toHaveBeenCalledOnce()
  })

  it('stops interrupt loading when the request reports a failure', async () => {
    const onInterrupt = vi.fn().mockRejectedValue(new Error('interrupt failed'))
    renderPanel({ agentActivity: AgentActivityState.WORKING, onInterrupt })

    const interrupt = screen.getByTestId('interrupt-button')
    fireEvent.click(interrupt)
    expect(interrupt).toBeDisabled()

    await Promise.resolve()
    await vi.advanceTimersByTimeAsync(1_000)

    expect(interrupt).not.toBeDisabled()
    expect(interrupt).toHaveTextContent('Interrupt')
  })

  it('keeps interrupt loading on the tab that sent the request', async () => {
    const [agentId, setAgentId] = createSignal('a1')
    let finishInterrupt: () => void = () => {}
    const request = new Promise<void>((resolve) => {
      finishInterrupt = resolve
    })
    const onInterrupt = vi.fn(() => request)
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel
          agentId={agentId()}
          agent={agent()}
          repoGitStore={createRepoGitStore()}
          onSendMessage={() => {}}
          onInterrupt={onInterrupt}
          agentActivity={AgentActivityState.WORKING}
        />
      </PreferencesProvider>
    ))

    fireEvent.click(screen.getByTestId('interrupt-button'))
    expect(screen.getByTestId('interrupt-button')).toBeDisabled()

    setAgentId('a2')
    expect(screen.getByTestId('interrupt-button')).not.toBeDisabled()
    expect(screen.getByTestId('interrupt-button')).toHaveTextContent('Interrupt')

    setAgentId('a1')
    expect(screen.getByTestId('interrupt-button')).toBeDisabled()
    expect(screen.getByTestId('interrupt-button')).toHaveTextContent('Interrupting...')

    finishInterrupt()
    await Promise.resolve()
    await vi.advanceTimersByTimeAsync(1_000)
    expect(screen.getByTestId('interrupt-button')).not.toBeDisabled()
  })

  it('keeps queue-pause loading on the tab that sent the request', async () => {
    const [agentId, setAgentId] = createSignal('a1')
    let finishPause: () => void = () => {}
    const request = new Promise<void>((resolve) => {
      finishPause = resolve
    })
    const onSetQueuePaused = vi.fn(() => request)
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel
          agentId={agentId()}
          agent={agent()}
          repoGitStore={createRepoGitStore()}
          onSendMessage={() => {}}
          inputQueue={create(AgentInputQueueSnapshotSchema, {
          })}
          onSetQueuePaused={onSetQueuePaused}
        />
      </PreferencesProvider>
    ))

    fireEvent.click(screen.getByTestId('queue-pause-button'))
    expect(screen.getByTestId('queue-pause-button')).toBeDisabled()

    setAgentId('a2')
    expect(screen.getByTestId('queue-pause-button')).not.toBeDisabled()

    setAgentId('a1')
    expect(screen.getByTestId('queue-pause-button')).toBeDisabled()

    finishPause()
    await Promise.resolve()
    await Promise.resolve()
    expect(screen.getByTestId('queue-pause-button')).not.toBeDisabled()
  })

  it('shows no pause banner while the queue runs', () => {
    renderPanel()
    expect(screen.queryByTestId('queue-pause-banner')).not.toBeInTheDocument()
  })

  it('turns the pause toggle into Resume Queue, in the label and in the name', () => {
    const snapshot = create(AgentInputQueueSnapshotSchema, {
      pauseReason: AgentInputQueuePauseReason.MANUAL,
    })
    const { unmount } = render(() => (
      <PreferencesProvider>
        <AgentEditorPanel
          agentId="a1"
          agent={agent({ workerId: 'w1' })}
          repoGitStore={createRepoGitStore()}
          gitTab={{ workerId: 'w1', gitToplevel: WORKTREE_DIR }}
          onSendMessage={() => {}}
          branchActions={stubBranchMenuActions()}
          branchWorkerId="w1"
          inputQueue={snapshot}
        />
      </PreferencesProvider>
    ))

    const toggle = screen.getByTestId('queue-pause-button')
    // Both halves, because below `sm` the label is `display: none` and the
    // aria-label is the button's only name. The visible word must also stay
    // INSIDE that name, or a voice-control user cannot address it.
    expect(toggle).toHaveTextContent('Resume Queue')
    expect(toggle).toHaveAttribute('aria-label', 'Resume Queue')
    // A `Play` icon, not the `Pause` one it shows at rest. `lucide-solid` puts
    // the icon's own name in the class list, which is the only handle on which
    // glyph rendered.
    expect(toggle.querySelector('svg')?.getAttribute('class')).toContain('play')
    unmount()
  })

  // A read-only subagent tab takes no message, so its queue can hold none. An
  // interrupt still pauses that queue, and a banner or toggle there would offer
  // an action that the tab cannot take.
  it('draws no queue controls on a read-only subagent tab whose queue paused', () => {
    const snapshot = create(AgentInputQueueSnapshotSchema, {
      pauseReason: AgentInputQueuePauseReason.INTERRUPTED,
    })
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel
          agentId="c1"
          agent={agent({ workerId: 'w1' })}
          repoGitStore={createRepoGitStore()}
          gitTab={{ workerId: 'w1', gitToplevel: WORKTREE_DIR }}
          onSendMessage={() => {}}
          branchActions={stubBranchMenuActions()}
          branchWorkerId="w1"
          inputQueue={snapshot}
          disabledReason="This subagent takes no messages."
        />
      </PreferencesProvider>
    ))

    expect(screen.queryByTestId('queue-pause-banner')).not.toBeInTheDocument()
    expect(screen.queryByTestId('queue-pause-banner-resume')).not.toBeInTheDocument()
    expect(screen.queryByTestId('queue-pause-button')).not.toBeInTheDocument()
  })

  it('says Send will queue while the queue is paused', () => {
    const snapshot = create(AgentInputQueueSnapshotSchema, {
      pauseReason: AgentInputQueuePauseReason.INTERRUPTED,
    })
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel
          agentId="a1"
          agent={agent({ workerId: 'w1' })}
          repoGitStore={createRepoGitStore()}
          gitTab={{ workerId: 'w1', gitToplevel: WORKTREE_DIR }}
          onSendMessage={() => {}}
          branchActions={stubBranchMenuActions()}
          branchWorkerId="w1"
          inputQueue={snapshot}
        />
      </PreferencesProvider>
    ))

    // The banner explains the pause even though the queue holds no items --
    // the case where nothing else on screen says anything.
    expect(screen.getByTestId('queue-pause-banner')).toHaveTextContent(
      'Queue paused because you interrupted the agent.',
    )
    // And the press itself says what it will do. The visible word stays inside
    // the accessible name, so a by-name lookup and voice control still match.
    const send = screen.getByTestId('send-button')
    expect(send).toHaveAttribute('aria-label', 'Add to queue')
    expect(send).toHaveTextContent('Queue')
  })

  it('resumes the queue from the banner button', async () => {
    const onSetQueuePaused = vi.fn().mockResolvedValue(undefined)
    const snapshot = create(AgentInputQueueSnapshotSchema, {
      pauseReason: AgentInputQueuePauseReason.AGENT_STOPPED,
    })
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel
          agentId="a1"
          agent={agent({ workerId: 'w1' })}
          repoGitStore={createRepoGitStore()}
          gitTab={{ workerId: 'w1', gitToplevel: WORKTREE_DIR }}
          onSendMessage={() => {}}
          branchActions={stubBranchMenuActions()}
          branchWorkerId="w1"
          inputQueue={snapshot}
          onSetQueuePaused={onSetQueuePaused}
        />
      </PreferencesProvider>
    ))

    await fireEvent.click(screen.getByTestId('queue-pause-banner-resume'))
    expect(onSetQueuePaused).toHaveBeenCalledExactlyOnceWith('a1', false)
  })

  it('sends one pause RPC for two fast presses of Resume', async () => {
    let settle: () => void = () => {}
    const onSetQueuePaused = vi.fn(() => new Promise<void>((resolve) => {
      settle = resolve
    }))
    const snapshot = create(AgentInputQueueSnapshotSchema, {
      pauseReason: AgentInputQueuePauseReason.AGENT_STOPPED,
    })
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel
          agentId="a1"
          agent={agent({ workerId: 'w1' })}
          repoGitStore={createRepoGitStore()}
          gitTab={{ workerId: 'w1', gitToplevel: WORKTREE_DIR }}
          onSendMessage={() => {}}
          branchActions={stubBranchMenuActions()}
          branchWorkerId="w1"
          inputQueue={snapshot}
          onSetQueuePaused={onSetQueuePaused}
        />
      </PreferencesProvider>
    ))

    // Nothing updates optimistically: `paused` moves only when the Worker's
    // snapshot lands, so the button still reads Resume for the whole round trip
    // and invites a second press. The state converges either way, because the
    // RPC carries an absolute boolean -- but a failure raises one warn toast per
    // call, so two presses of one intent raise two toasts.
    const resume = screen.getByTestId('queue-pause-banner-resume')
    await fireEvent.click(resume)
    await fireEvent.click(resume)
    expect(onSetQueuePaused).toHaveBeenCalledTimes(1)
    expect(resume).toBeDisabled()
    // The composer's own toggle is the SAME intent, so it is refused too.
    expect(screen.getByTestId('queue-pause-button')).toBeDisabled()

    settle()
    await Promise.resolve()
    await Promise.resolve()
    expect(resume).not.toBeDisabled()
  })

  it('blocks new attachments while an enqueue remains in flight', async () => {
    vi.useRealTimers()
    const attachment = {
      id: 'pending-1',
      file: new File(['pending'], 'pending.txt', { type: 'text/plain' }),
      filename: 'pending.txt',
      mimeType: 'text/plain',
      data: new TextEncoder().encode('pending'),
      size: 7,
    }
    setAttachments('a1', [attachment])
    let finishEnqueue: (() => void) | undefined
    let send: (() => void) | undefined
    const onSendMessage = vi.fn(() => new Promise<void>((resolve) => {
      finishEnqueue = resolve
    }))
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel
          agentId="a1"
          agent={agent()}
          repoGitStore={createRepoGitStore()}
          onSendMessage={onSendMessage}
          triggerSendRef={(value) => { send = value }}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(send).toBeTypeOf('function'))
    expect(screen.getByTestId('send-button')).not.toBeDisabled()

    send?.()

    expect(onSendMessage).toHaveBeenCalledWith('', [attachment], '')
    expect(screen.getByTestId('file-input')).toBeDisabled()
    finishEnqueue?.()
    await waitFor(() => expect(screen.getByTestId('file-input')).not.toBeDisabled())
  })

  // The spinner holds for a debounce window after the enqueue resolves, so it
  // never flashes away. That window must not hold the attachment paths: the
  // enqueue takes milliseconds, and a composer that refuses a paste, a drop and
  // the file picker for the rest of the second is the bug this pins.
  it('accepts an attachment while the send spinner still holds its debounce', async () => {
    vi.useRealTimers()
    let finishEnqueue: (() => void) | undefined
    let send: (() => void) | undefined
    let addFiles: ((files: File[]) => Promise<number>) | undefined
    const onSendMessage = vi.fn(() => new Promise<void>((resolve) => {
      finishEnqueue = resolve
    }))
    saveDraft('a1', 'queued text', -1)
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel
          agentId="a1"
          agent={agent()}
          repoGitStore={createRepoGitStore()}
          onSendMessage={onSendMessage}
          triggerSendRef={(value) => { send = value }}
          addFilesRef={(fn) => { addFiles = fn }}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(send).toBeTypeOf('function'))
    await waitFor(() => expect(addFiles).toBeTypeOf('function'))

    await waitFor(() => expect(screen.getByTestId('schedule-send-trigger')).not.toBeDisabled())

    // Fake `setTimeout` alone, so the 1000 ms debounce cannot fire on its own
    // while every assertion below runs. A full fake clock also stops the
    // FileReader that `addFiles` awaits, and the read never completes.
    vi.useFakeTimers({
      toFake: ['setTimeout', 'clearTimeout'],
    })
    send?.()
    await waitFor(() => expect(onSendMessage).toHaveBeenCalledWith('queued text', undefined, ''))
    expect(screen.getByTestId('file-input')).toBeDisabled()
    expect(screen.getByTestId('send-spinner')).toBeInTheDocument()

    finishEnqueue?.()
    await waitFor(() => expect(screen.getByTestId('file-input')).not.toBeDisabled())

    // The enqueue settled, so every attachment path opens again -- while the
    // spinner still shows, which is what proves the two are separate.
    expect(screen.getByTestId('send-spinner')).toBeInTheDocument()
    await expect(addFiles?.([
      new File(['hello'], 'hello.txt', { type: 'text/plain' }),
    ])).resolves.toBe(1)

    await fireEvent.click(screen.getByTestId('composer-plus-trigger'))
    expect(screen.getByTestId('composer-attach-file')).not.toBeDisabled()

    // The spinner clears only when its own debounce ends.
    vi.advanceTimersByTime(1000)
    expect(screen.queryByTestId('send-spinner')).not.toBeInTheDocument()
  })

  it('clears only the submitted draft after an agent switch', async () => {
    vi.useRealTimers()
    const [agentId, setAgentId] = createSignal('a1')
    const attachment = (id: string) => ({
      id,
      file: new File([id], `${id}.txt`, { type: 'text/plain' }),
      filename: `${id}.txt`,
      mimeType: 'text/plain',
      data: new TextEncoder().encode(id),
      size: id.length,
    })
    const attachmentA = attachment('attachment-a')
    const attachmentB = attachment('attachment-b')
    setAttachments('a1', [attachmentA])
    setAttachments('a2', [attachmentB])
    saveDraft('a1', 'draft a', -1)
    saveDraft('a2', 'draft b', -1)
    let finishEnqueue: (() => void) | undefined
    let send: (() => void) | undefined
    const onSendMessage = vi.fn(() => new Promise<void>((resolve) => {
      finishEnqueue = resolve
    }))
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel
          agentId={agentId()}
          agent={agent()}
          repoGitStore={createRepoGitStore()}
          onSendMessage={onSendMessage}
          triggerSendRef={(value) => { send = value }}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(send).toBeTypeOf('function'))
    await waitFor(() => expect(screen.getByTestId('schedule-send-trigger')).not.toBeDisabled())
    send?.()
    expect(onSendMessage).toHaveBeenCalledWith('draft a', [attachmentA], '')

    setAgentId('a2')
    await waitFor(() => expect(document.querySelector('[data-testid="composer-editor"] .ProseMirror')).toHaveTextContent('draft b'))
    expect(screen.getByTestId('file-input')).not.toBeDisabled()
    await waitFor(() => expect(screen.getByTestId('send-button')).not.toBeDisabled())
    expect(screen.queryByTestId('send-spinner')).not.toBeInTheDocument()

    setAgentId('a1')
    await waitFor(() => expect(document.querySelector('[data-testid="composer-editor"] .ProseMirror')).toHaveTextContent('draft a'))
    expect(screen.getByTestId('file-input')).toBeDisabled()
    expect(screen.getByTestId('send-button')).toBeDisabled()
    expect(screen.getByTestId('send-spinner')).toBeInTheDocument()

    setAgentId('a2')
    await waitFor(() => expect(document.querySelector('[data-testid="composer-editor"] .ProseMirror')).toHaveTextContent('draft b'))
    finishEnqueue?.()
    // Wait on the draft that the send clears, not on the composer re-opening:
    // the attachment paths open one microtask before the editor clears its
    // submitted draft.
    await waitFor(async () => expect((await loadDraft('a1')).content).toBe(''))

    expect(screen.getByTestId('file-input')).not.toBeDisabled()
    expect(document.querySelector('[data-testid="composer-editor"] .ProseMirror')).toHaveTextContent('draft b')
    expect((await loadDraft('a2')).content).toBe('draft b')
    expect(getAttachments('a1')).toEqual([])
    expect(getAttachments('a2')).toEqual([attachmentB])
  })

  it('does not clear a new agent queue edit when the old save finishes', async () => {
    vi.useRealTimers()
    const [agentId, setAgentId] = createSignal('a1')
    const queue = (id: string) => create(AgentInputQueueSnapshotSchema, {
      pauseReason: AgentInputQueuePauseReason.MANUAL,
      items: [
        {
          id: `owned-${id}`,
          text: `preview ${id}`,
          kind: AgentInputKind.USER_MESSAGE,
          state: AgentInputState.QUEUED,
          editOwnerClientId: 'client-a',
        },
      ],
    })
    const onBeginQueueEdit = vi.fn((agentId: string) => Promise.resolve({
      snapshot: queue(agentId),
      attachments: [],
      text: `full ${agentId}`,
    }))
    let finishSave: (() => void) | undefined
    const onUpdateQueueItem = vi.fn(() => new Promise<void>((resolve) => {
      finishSave = resolve
    }))
    saveDraft('a1-queue-owned-a1', 'saved a1 edit', -1)
    let send: (() => void) | undefined
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel
          agentId={agentId()}
          agent={agent()}
          repoGitStore={createRepoGitStore()}
          onSendMessage={() => {}}
          inputQueue={queue(agentId())}
          queueClientId="client-a"
          onBeginQueueEdit={onBeginQueueEdit}
          onUpdateQueueItem={onUpdateQueueItem}
          triggerSendRef={(value) => { send = value }}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(document.querySelector('[data-testid="composer-editor"] .ProseMirror')).toHaveTextContent('saved a1 edit'))
    await waitFor(() => expect(screen.getByTestId('schedule-send-trigger')).not.toBeDisabled())
    send?.()
    // The item holds no schedule and the save keeps it so, so the save omits it.
    expect(onUpdateQueueItem).toHaveBeenCalledWith('a1', expect.objectContaining({ id: 'owned-a1' }), 'saved a1 edit', [], undefined)

    setAgentId('a2')
    await waitFor(() => expect(document.querySelector('[data-testid="composer-editor"] .ProseMirror')).toHaveTextContent('full a2'))
    finishSave?.()
    await waitFor(() => expect(screen.getByTestId('file-input')).not.toBeDisabled())

    expect(onBeginQueueEdit.mock.calls.filter(([agentId]) => agentId === 'a2')).toHaveLength(1)
    expect(document.querySelector('[data-testid="composer-editor"] .ProseMirror')).toHaveTextContent('full a2')
    expect(screen.getByRole('button', { name: 'Cancel Edit' })).toBeInTheDocument()
  })

  it('preserves normal attachments when a queue edit is canceled', async () => {
    const normalAttachment = {
      id: 'normal-1',
      file: new File(['normal'], 'normal.txt', { type: 'text/plain' }),
      filename: 'normal.txt',
      mimeType: 'text/plain',
      data: new TextEncoder().encode('normal'),
      size: 6,
    }
    setAttachments('a1', [normalAttachment])
    const queueSnapshot = (owner: string) => create(AgentInputQueueSnapshotSchema, {
      revision: owner ? 2n : 1n,
      pauseReason: AgentInputQueuePauseReason.MANUAL,
      items: [
        {
          id: 'queued-1',
          text: 'queued preview',
          kind: AgentInputKind.USER_MESSAGE,
          state: AgentInputState.QUEUED,
          editOwnerClientId: owner,
        },
      ],
    })
    const [snapshot, setSnapshot] = createSignal(queueSnapshot(''))
    const edited = queueSnapshot('client-a')
    const onCancelQueueEdit = vi.fn(() => {
      setSnapshot(queueSnapshot(''))
      return Promise.resolve()
    })
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel
          agentId="a1"
          agent={agent()}
          repoGitStore={createRepoGitStore()}
          onSendMessage={() => {}}
          inputQueue={snapshot()}
          queueClientId="client-a"
          onBeginQueueEdit={() => {
            setSnapshot(edited)
            return Promise.resolve({ snapshot: edited, attachments: [], text: 'full queued text' })
          }}
          onCancelQueueEdit={onCancelQueueEdit}
        />
      </PreferencesProvider>
    ))

    await fireEvent.click(screen.getByRole('button', { name: 'Edit' }))
    await Promise.resolve()
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel Edit' }))
    await Promise.resolve()
    // The cancel carries the panel agent beside the item, which states none.
    expect(onCancelQueueEdit).toHaveBeenCalledExactlyOnceWith('a1', expect.objectContaining({ id: 'queued-1' }))
    expect(getAttachments('a1')).toEqual([normalAttachment])
  })

  it('keeps a pending normal attachment out of a queued input being edited', async () => {
    vi.useRealTimers()
    const readers: FileReader[] = []
    const read = vi.spyOn(FileReader.prototype, 'readAsArrayBuffer').mockImplementation(function (this: FileReader) {
      readers.push(this)
    })
    const queuedAttachment = create(AttachmentSchema, {
      filename: 'queued.txt',
      mimeType: 'text/plain',
      data: new TextEncoder().encode('queued'),
    })
    const snapshot = (owner: string) => create(AgentInputQueueSnapshotSchema, {
      revision: owner ? 2n : 1n,
      pauseReason: AgentInputQueuePauseReason.MANUAL,
      items: [
        {
          id: 'queued-1',
          text: 'queued preview',
          kind: AgentInputKind.USER_MESSAGE,
          state: AgentInputState.QUEUED,
          editOwnerClientId: owner,
        },
      ],
    })
    const [queue, setQueue] = createSignal(snapshot(''))
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel
          agentId="a1"
          agent={agent()}
          repoGitStore={createRepoGitStore()}
          onSendMessage={() => {}}
          inputQueue={queue()}
          queueClientId="client-a"
          onBeginQueueEdit={() => {
            const edited = snapshot('client-a')
            setQueue(edited)
            return Promise.resolve({
              snapshot: edited,
              attachments: [queuedAttachment],
              text: 'queued text',
            })
          }}
        />
      </PreferencesProvider>
    ))
    try {
      await fireEvent.change(screen.getByTestId('file-input'), {
        target: {
          files: [
            new File(['pending'], 'pending.txt'),
          ],
        },
      })
      const reader = readers[0]
      expect(reader).toBeDefined()
      await fireEvent.click(screen.getByRole('button', { name: 'Edit' }))
      await waitFor(() => expect(screen.getByTestId('attachment-pill')).toHaveTextContent('queued.txt'))
      Object.defineProperty(reader!, 'result', { value: new TextEncoder().encode('pending').buffer })
      reader!.onload?.(new ProgressEvent('load') as ProgressEvent<FileReader>)
      await Promise.resolve()
      expect(screen.getAllByTestId('attachment-pill')).toHaveLength(1)
      expect(screen.getByTestId('attachment-pill')).toHaveTextContent('queued.txt')
      expect(getAttachments(queueEditDraftKey('a1', 'queued-1'))).toMatchObject([
        {
          filename: 'queued.txt',
          mimeType: 'text/plain',
          data: queuedAttachment.data,
        },
      ])
      expect(getAttachments('a1')).toEqual([])
    }
    finally {
      read.mockRestore()
      clearAttachments(queueEditDraftKey('a1', 'queued-1'))
    }
  })

  // The delete path and the cancel path share one cleanup helper. This pins the
  // delete call site: no edit is loaded here, so only the helper can drop the
  // draft that the deleted input left behind.
  it('forgets the draft of a deleted queued input', async () => {
    const draftKey = queueEditDraftKey('a1', 'queued-1')
    saveDraft(draftKey, 'stale edit', -1)
    const snapshot = create(AgentInputQueueSnapshotSchema, {
      pauseReason: AgentInputQueuePauseReason.MANUAL,
      items: [
        {
          id: 'queued-1',
          text: 'queued preview',
          kind: AgentInputKind.USER_MESSAGE,
          state: AgentInputState.QUEUED,
        },
      ],
    })
    const onDeleteQueueItem = vi.fn().mockResolvedValue(undefined)
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel
          agentId="a1"
          agent={agent()}
          repoGitStore={createRepoGitStore()}
          onSendMessage={() => {}}
          inputQueue={snapshot}
          queueClientId="client-a"
          onDeleteQueueItem={onDeleteQueueItem}
        />
      </PreferencesProvider>
    ))

    // Delete arms on the first click and deletes on the second.
    await fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm delete?' }))
    await Promise.resolve()

    expect(onDeleteQueueItem).toHaveBeenCalledWith('a1', expect.objectContaining({ id: 'queued-1' }))
    expect((await loadDraft(draftKey)).content).toBe('')
  })

  it('requires confirmation before it retries uncertain delivery', async () => {
    const onRetryQueueItem = vi.fn().mockResolvedValue(undefined)
    const snapshot = create(AgentInputQueueSnapshotSchema, {
      pauseReason: AgentInputQueuePauseReason.MANUAL,
      items: [
        {
          id: 'uncertain-1',
          text: 'possibly delivered',
          kind: AgentInputKind.USER_MESSAGE,
          state: AgentInputState.DELIVERY_UNCERTAIN,
        },
      ],
    })
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel
          agentId="a1"
          agent={agent()}
          repoGitStore={createRepoGitStore()}
          onSendMessage={() => {}}
          inputQueue={snapshot}
          queueClientId="client-a"
          onRetryQueueItem={onRetryQueueItem}
        />
      </PreferencesProvider>
    ))

    await fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    const dialog = screen.getByTestId('retry-uncertain-input-dialog')
    expect(dialog).toHaveTextContent('The provider can already have accepted this input.')
    expect(onRetryQueueItem).not.toHaveBeenCalled()
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Retry' }))
    expect(onRetryQueueItem).toHaveBeenCalledWith('a1', expect.objectContaining({ id: 'uncertain-1' }), true)
  })

  it('reloads an edit that the same browser client owns', () => {
    const onBeginQueueEdit = vi.fn(() => new Promise<never>(() => {}))
    const snapshot = create(AgentInputQueueSnapshotSchema, {
      pauseReason: AgentInputQueuePauseReason.MANUAL,
      items: [
        {
          id: 'owned-1',
          text: 'preview',
          kind: AgentInputKind.USER_MESSAGE,
          state: AgentInputState.QUEUED,
          editOwnerClientId: 'client-a',
        },
      ],
    })
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel
          agentId="a1"
          agent={agent()}
          repoGitStore={createRepoGitStore()}
          onSendMessage={() => {}}
          inputQueue={snapshot}
          queueClientId="client-a"
          onBeginQueueEdit={onBeginQueueEdit}
        />
      </PreferencesProvider>
    ))

    expect(onBeginQueueEdit).toHaveBeenCalledWith('a1', expect.objectContaining({ id: 'owned-1' }), false)
    expect(screen.getByRole('button', { name: 'Resume Edit' })).toBeInTheDocument()
  })

  it('loads the current agent edit while another agent edit request waits', async () => {
    const [agentId, setAgentId] = createSignal('a1')
    const queue = (id: string) => create(AgentInputQueueSnapshotSchema, {
      items: [
        {
          id: `owned-${id}`,
          text: id,
          kind: AgentInputKind.USER_MESSAGE,
          state: AgentInputState.QUEUED,
          editOwnerClientId: 'client-a',
        },
      ],
    })
    const onBeginQueueEdit = vi.fn(() => new Promise<never>(() => {}))
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel
          agentId={agentId()}
          agent={agent()}
          repoGitStore={createRepoGitStore()}
          onSendMessage={() => {}}
          inputQueue={queue(agentId())}
          queueClientId="client-a"
          onBeginQueueEdit={onBeginQueueEdit}
        />
      </PreferencesProvider>
    ))
    expect(onBeginQueueEdit).toHaveBeenCalledWith('a1', expect.objectContaining({ id: 'owned-a1' }), false)

    setAgentId('a2')
    await Promise.resolve()

    expect(onBeginQueueEdit).toHaveBeenCalledWith('a2', expect.objectContaining({ id: 'owned-a2' }), false)
  })

  // The defect this pins: the chip printed an absolute path while the sidebar
  // row for the SAME checkout printed a tilde one, because the panel read the
  // home dir off a field nothing populates.
  it('shortens the chip tooltip directory against the worker home dir', async () => {
    renderPanel()

    const tooltip = hoverForTooltip(screen.getByTestId('composer-branch-trigger'))
    expect(tooltip).not.toBeNull()
    expect(tooltip!.querySelector('[data-testid="working-tree-directory"]')!.textContent)
      .toBe('~/Workspaces/r-worktrees/feature')
  })

  it('shows the checkout kind on the chip', async () => {
    renderPanel()

    expect(screen.getByTestId('composer-branch-trigger').querySelector('[data-testid="worktree-icon"]'))
      .not
      .toBeNull()
  })

  // A worker the store knows nothing about reports no home dir. The absolute
  // path is correct there; a guessed short one would not be.
  it('leaves the directory absolute for a worker with no system info', async () => {
    renderPanel({ workerId: 'w-unknown' })

    const tooltip = hoverForTooltip(screen.getByTestId('composer-branch-trigger'))
    expect(tooltip!.querySelector('[data-testid="working-tree-directory"]')!.textContent)
      .toBe(WORKTREE_DIR)
  })
})

// The composer's schedule control: a draft under the composer's draft key,
// validated again at send time, seeded from the edited item, and never part
// of a control answer.
describe('agent editor panel schedule send', () => {
  const DAY_MS = 24 * 60 * 60 * 1000
  const futureAt = (days = 1) => new Date(Date.now() + days * DAY_MS)
  const futureField = () => localFieldValueOf(futureAt())

  /** A paused queue whose one item this client already edits, as after a reload. */
  const ownedEditQueue = (agentId: string, scheduledAt: string) => create(AgentInputQueueSnapshotSchema, {
    pauseReason: AgentInputQueuePauseReason.MANUAL,
    items: [
      {
        id: `owned-${agentId}`,
        text: `preview ${agentId}`,
        kind: AgentInputKind.USER_MESSAGE,
        state: AgentInputState.QUEUED,
        editOwnerClientId: 'client-a',
        scheduledAt,
      },
    ],
  })
  const editorText = () => document.querySelector('[data-testid="composer-editor"] .ProseMirror')

  /** Open the popover and apply one field value; the panel draft keeps it. */
  const applySchedule = (field: string) => {
    fireEvent.click(screen.getByTestId('schedule-send-trigger'))
    const input = screen.getByTestId('schedule-send-field') as HTMLInputElement
    fireEvent.input(input, {
      target: { value: field },
    })
    fireEvent.click(screen.getByTestId('schedule-send-apply'))
  }

  it('renders an unmarked trigger by default and sends immediate input with no schedule', async () => {
    vi.useRealTimers()
    const onSendMessage = vi.fn()
    renderPanel({ onSendMessage })
    await waitFor(() => expect(getEditorRef('a1')).toBeDefined())
    expect(screen.getByTestId('schedule-send-trigger')).not.toHaveAttribute('data-scheduled')
    getEditorRef('a1')?.set('hello now')
    fireEvent.click(screen.getByTestId('send-button'))
    await waitFor(() => expect(onSendMessage).toHaveBeenCalled())
    expect(onSendMessage.mock.calls[0]?.[2]).toBe('')
  })

  it('sends the instant that the draft chose, and clears the draft after success', async () => {
    vi.useRealTimers()
    const onSendMessage = vi.fn()
    renderPanel({ onSendMessage })
    await waitFor(() => expect(getEditorRef('a1')).toBeDefined())
    getEditorRef('a1')?.set('hello later')
    const at = futureAt()
    applySchedule(localFieldValueOf(at))
    expect(screen.getByTestId('schedule-send-trigger')).toHaveAttribute('data-scheduled', 'true')
    fireEvent.click(screen.getByTestId('send-button'))
    await waitFor(() => expect(onSendMessage).toHaveBeenCalled())
    expect(onSendMessage.mock.calls[0]?.[2]).toBe(at.toISOString())
    // A successful send clears the draft: the trigger is immediate again.
    await waitFor(() => expect(screen.getByTestId('schedule-send-trigger')).not.toHaveAttribute('data-scheduled'))
  })

  it('refuses an expired draft without losing the text or the schedule', async () => {
    vi.useFakeTimers({
      toFake: [
        'setTimeout',
        'clearTimeout',
        'setInterval',
        'clearInterval',
        'Date',
      ],
    })
    const onSendMessage = vi.fn()
    renderPanel({ onSendMessage })
    await waitFor(() => expect(getEditorRef('a1')).toBeDefined())
    getEditorRef('a1')?.set('hello too late')
    applySchedule(futureField())
    // The deadline passes while the draft sits in the composer.
    vi.setSystemTime(new Date(Date.now() + 48 * 60 * 60 * 1000))
    fireEvent.click(screen.getByTestId('send-button'))
    expect(onSendMessage).not.toHaveBeenCalled()
    // Data loss guards: the editor text and the schedule draft both survive.
    expect(getEditorRef('a1')?.get()).toBe('hello too late')
    expect(screen.getByTestId('schedule-send-trigger')).toHaveAttribute('data-scheduled', 'true')
  })

  it('re-sends an unanswered attempt after its schedule passed, with the schedule unchanged', async () => {
    vi.useFakeTimers({
      toFake: [
        'setTimeout',
        'clearTimeout',
        'setInterval',
        'clearInterval',
        'Date',
      ],
    })
    const onSendMessage = vi.fn()
    const at = futureAt()
    // The first send of this payload lost its answer, so the Worker may hold
    // it under the attempt's input ID.
    const onRetryPendingSend = vi.fn<NonNullable<AgentEditorPanelProps['onRetryPendingSend']>>(() => Promise.resolve(true))
    renderPanel({ onSendMessage, onRetryPendingSend })
    await waitFor(() => expect(getEditorRef('a1')).toBeDefined())
    getEditorRef('a1')?.set('hello again')
    applySchedule(localFieldValueOf(at))
    vi.setSystemTime(new Date(at.getTime() + 2 * DAY_MS))
    fireEvent.click(screen.getByTestId('send-button'))
    await waitFor(() => expect(onRetryPendingSend).toHaveBeenCalledWith('hello again', [], at.toISOString()))
    expect(onSendMessage).not.toHaveBeenCalled()
    await waitFor(() => expect(editorText()).toHaveTextContent(/^$/))
  })

  it('keeps the schedule draft when the send itself fails', async () => {
    vi.useRealTimers()
    const onSendMessage = vi.fn(() => Promise.reject(new Error('worker unreachable')))
    renderPanel({ onSendMessage })
    await waitFor(() => expect(getEditorRef('a1')).toBeDefined())
    getEditorRef('a1')?.set('try me')
    applySchedule(futureField())
    fireEvent.click(screen.getByTestId('send-button'))
    await waitFor(() => expect(onSendMessage).toHaveBeenCalled())
    await waitFor(() => expect(screen.getByTestId('schedule-send-trigger')).toHaveAttribute('data-scheduled', 'true'))
  })

  it('keeps one agent schedule out of another agent composer', async () => {
    vi.useRealTimers()
    const [agentId, setAgentId] = createSignal('a1')
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel
          agentId={agentId()}
          agent={agent()}
          repoGitStore={createRepoGitStore()}
          onSendMessage={() => {}}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(getEditorRef('a1')).toBeDefined())
    applySchedule(futureField())
    expect(screen.getByTestId('schedule-send-trigger')).toHaveAttribute('data-scheduled', 'true')

    setAgentId('a2')
    await waitFor(() => expect(getEditorRef('a2')).toBeDefined())
    expect(screen.getByTestId('schedule-send-trigger')).not.toHaveAttribute('data-scheduled')
  })

  it('keeps schedule readiness when reactive props repeat the same draft identity', async () => {
    vi.useRealTimers()
    const [view, setView] = createSignal({ agentId: 'a1' })
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel agentId={view().agentId} agent={agent()} repoGitStore={createRepoGitStore()} onSendMessage={() => {}} />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(getEditorRef('a1')).toBeDefined())
    await waitFor(() => expect(screen.getByTestId('schedule-send-trigger')).not.toBeDisabled())
    setView({ agentId: 'a1' })
    expect(screen.getByTestId('schedule-send-trigger')).not.toBeDisabled()
  })

  it('closes an open schedule popover when the composer changes agents', async () => {
    vi.useRealTimers()
    const [agentId, setAgentId] = createSignal('a1')
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel agentId={agentId()} agent={agent()} repoGitStore={createRepoGitStore()} onSendMessage={() => {}} />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(getEditorRef('a1')).toBeDefined())
    fireEvent.click(screen.getByTestId('schedule-send-trigger'))
    const previousPopover = screen.getByTestId('schedule-send-popover')
    fireEvent.input(screen.getByTestId('schedule-send-field'), {
      target: {
        value: futureField(),
      },
    })
    setAgentId('a2')
    await waitFor(() => expect(getEditorRef('a2')).toBeDefined())
    expect(previousPopover).not.toBeInTheDocument()
    expect(screen.getByTestId('schedule-send-trigger')).not.toHaveAttribute('data-scheduled')
    fireEvent.click(screen.getByTestId('schedule-send-trigger'))
    expect(screen.getByTestId('schedule-send-field')).toHaveValue('')
  })

  it('keeps an unchanged edit schedule by omitting it from the save', async () => {
    vi.useRealTimers()
    // A save that re-sent a value derived from the field would move a schedule
    // in the repeated hour of a fall-back day (scheduleSend.test.ts), and would
    // overwrite a deadline that a provider moved during the edit. An omitted
    // schedule keeps whatever the Worker holds.
    const stored = '2099-06-01T12:00:00.000Z'
    const onUpdateQueueItem = vi.fn<NonNullable<AgentEditorPanelProps['onUpdateQueueItem']>>(() => Promise.resolve())
    let send: (() => void) | undefined
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel
          agentId="a1"
          agent={agent()}
          repoGitStore={createRepoGitStore()}
          onSendMessage={() => {}}
          inputQueue={ownedEditQueue('a1', stored)}
          queueClientId="client-a"
          onBeginQueueEdit={() => Promise.resolve({
            snapshot: ownedEditQueue('a1', stored),
            attachments: [],
            text: 'full a1',
          })}
          onUpdateQueueItem={onUpdateQueueItem}
          triggerSendRef={(value) => { send = value }}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(editorText()).toHaveTextContent('full a1'))
    expect(screen.getByTestId('schedule-send-trigger')).toHaveAttribute('data-scheduled', 'true')
    send?.()
    await waitFor(() => expect(onUpdateQueueItem).toHaveBeenCalled())
    expect(onUpdateQueueItem).toHaveBeenCalledWith('a1', expect.objectContaining({ id: 'owned-a1' }), 'full a1', [], undefined)
  })

  it('clears an edit schedule by sending an empty one', async () => {
    vi.useRealTimers()
    const stored = '2099-06-01T12:00:00.000Z'
    const onUpdateQueueItem = vi.fn<NonNullable<AgentEditorPanelProps['onUpdateQueueItem']>>(() => Promise.resolve())
    let send: (() => void) | undefined
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel
          agentId="a1"
          agent={agent()}
          repoGitStore={createRepoGitStore()}
          onSendMessage={() => {}}
          inputQueue={ownedEditQueue('a1', stored)}
          queueClientId="client-a"
          onBeginQueueEdit={() => Promise.resolve({
            snapshot: ownedEditQueue('a1', stored),
            attachments: [],
            text: 'full a1',
          })}
          onUpdateQueueItem={onUpdateQueueItem}
          triggerSendRef={(value) => { send = value }}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(editorText()).toHaveTextContent('full a1'))
    fireEvent.click(screen.getByTestId('schedule-send-trigger'))
    fireEvent.click(screen.getByTestId('schedule-send-clear'))
    expect(screen.getByTestId('schedule-send-trigger')).not.toHaveAttribute('data-scheduled')

    send?.()
    await waitFor(() => expect(onUpdateQueueItem).toHaveBeenCalled())
    expect(onUpdateQueueItem.mock.calls[0]?.[4]).toBe('')
  })

  it('keeps an applied edit schedule across a switch to another agent and back', async () => {
    vi.useRealTimers()
    const [agentId, setAgentId] = createSignal('a1')
    const stored = futureAt(1).toISOString()
    const queueA1 = ownedEditQueue('a1', stored)
    const queueA2 = create(AgentInputQueueSnapshotSchema, { pauseReason: AgentInputQueuePauseReason.MANUAL, items: [] })
    const onUpdateQueueItem = vi.fn<NonNullable<AgentEditorPanelProps['onUpdateQueueItem']>>(() => Promise.resolve())
    let send: (() => void) | undefined
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel
          agentId={agentId()}
          agent={agent()}
          repoGitStore={createRepoGitStore()}
          onSendMessage={() => {}}
          inputQueue={agentId() === 'a1'
            ? queueA1
            : queueA2}
          queueClientId="client-a"
          onBeginQueueEdit={() => Promise.resolve({ snapshot: queueA1, attachments: [], text: 'full a1' })}
          onUpdateQueueItem={onUpdateQueueItem}
          triggerSendRef={(value) => { send = value }}
        />
      </PreferencesProvider>
    ))
    await waitFor(() => expect(editorText()).toHaveTextContent('full a1'))
    const applied = futureAt(2)
    applySchedule(localFieldValueOf(applied))

    setAgentId('a2')
    await waitFor(() => expect(getEditorRef('a2')).toBeDefined())
    expect(screen.getByTestId('schedule-send-trigger')).not.toHaveAttribute('data-scheduled')
    setAgentId('a1')
    await waitFor(() => expect(editorText()).toHaveTextContent('full a1'))

    await waitFor(() => expect(screen.getByTestId('schedule-send-trigger')).not.toBeDisabled())
    send?.()
    await waitFor(() => expect(onUpdateQueueItem).toHaveBeenCalled())
    expect(onUpdateQueueItem.mock.calls[0]?.[4]).toBe(applied.toISOString())
  })

  it('keeps schedule lifetime aligned with text edited days after the schedule was applied', async () => {
    vi.useRealTimers()
    renderPanel()
    await waitFor(() => expect(getEditorRef('a1')).toBeDefined())
    await waitFor(() => expect(screen.getByTestId('schedule-send-trigger')).not.toBeDisabled())
    vi.useFakeTimers({
      toFake: ['Date', 'setTimeout', 'clearTimeout'],
    })
    const started = Date.now()
    applySchedule(localFieldValueOf(new Date(started + 30 * DAY_MS)))
    getEditorRef('a1')?.set('original draft')
    await vi.runAllTimersAsync()
    await flushStorageWrites()
    vi.setSystemTime(started + 6 * DAY_MS)
    getEditorRef('a1')?.set('edited on day six')
    await vi.runAllTimersAsync()
    await flushStorageWrites()
    vi.setSystemTime(started + 8 * DAY_MS)
    setStorageAccountForTests('temporary-draft-account')
    setStorageAccountForTests(TEST_USER_ID)
    await waitFor(() => expect(screen.getByTestId('schedule-send-trigger')).not.toBeDisabled())
    expect((await loadDraft('a1')).content).toBe('edited on day six')
    expect(screen.getByTestId('schedule-send-trigger')).toHaveAttribute('data-scheduled', 'true')
  })

  it('keeps text lifetime aligned when only the schedule changes days later', async () => {
    vi.useRealTimers()
    renderPanel()
    await waitFor(() => expect(getEditorRef('a1')).toBeDefined())
    await waitFor(() => expect(screen.getByTestId('schedule-send-trigger')).not.toBeDisabled())
    vi.useFakeTimers({
      toFake: ['Date', 'setTimeout', 'clearTimeout'],
    })
    const started = Date.now()
    getEditorRef('a1')?.set('text before the schedule change')
    await vi.runAllTimersAsync()
    await flushStorageWrites()
    vi.setSystemTime(started + 6 * DAY_MS)
    applySchedule(localFieldValueOf(new Date(started + 30 * DAY_MS)))
    await flushStorageWrites()
    vi.setSystemTime(started + 8 * DAY_MS)
    setStorageAccountForTests('temporary-draft-account')
    setStorageAccountForTests(TEST_USER_ID)
    await waitFor(() => expect(screen.getByTestId('schedule-send-trigger')).not.toBeDisabled())
    expect((await loadDraft('a1')).content).toBe('text before the schedule change')
    expect(screen.getByTestId('schedule-send-trigger')).toHaveAttribute('data-scheduled', 'true')
  })

  it('keeps the ordinary schedule draft while the panel is unmounted', async () => {
    vi.useRealTimers()
    // The shell unmounts the panel whenever a non-agent tab has focus.
    const [shown, setShown] = createSignal(true)
    render(() => (
      <PreferencesProvider>
        <Show when={shown()}>
          <AgentEditorPanel agentId="a1" agent={agent()} repoGitStore={createRepoGitStore()} onSendMessage={() => {}} />
        </Show>
      </PreferencesProvider>
    ))
    await waitFor(() => expect(getEditorRef('a1')).toBeDefined())
    applySchedule(futureField())

    setShown(false)
    expect(screen.queryByTestId('schedule-send-trigger')).not.toBeInTheDocument()
    setShown(true)
    await waitFor(() => expect(screen.getByTestId('schedule-send-trigger')).toHaveAttribute('data-scheduled', 'true'))
  })

  it('offers no schedule control while a control request owns the composer', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, {
      requestId: 'perm-1',
      claimToken: 'claim-1',
      ...permissionRequest(),
    })
    renderPanel({
      controlStore,
      onControlAnswer: async () => true,
    })
    await vi.waitFor(() => expect(screen.getByTestId('control-banner')).toBeInTheDocument())
    // The control-response action cluster carries no schedule trigger: a reply
    // the user owes NOW inherits no ordinary schedule.
    expect(screen.queryByTestId('schedule-send-trigger')).not.toBeInTheDocument()
  })
})

// The info card reads the worker name from the worker store, because
// AgentInfo carries none. The store mock above holds a name for worker w1 only.
describe('agent editor panel info card worker row', () => {
  /** The panel for an agent with a session, so the status bar shows the info card. */
  function renderWithInfoCard(workerId: string) {
    render(() => (
      <PreferencesProvider>
        <AgentEditorPanel
          agentId="a1"
          agent={agent({ workerId, agentSessionId: 'session-1' })}
          repoGitStore={createRepoGitStore()}
          onSendMessage={() => {}}
        />
      </PreferencesProvider>
    ))
    return screen.getByTestId('agent-info-popover')
  }

  it('shows the worker name that the worker store holds', () => {
    const popover = renderWithInfoCard('w1')

    expect(within(popover).getByTestId('info-row-worker')).toHaveTextContent('build-box-1')
  })

  it('hides the Worker row for a worker the store holds no name for', () => {
    const popover = renderWithInfoCard('w-unknown')

    // The card itself renders, so the missing row is the name's absence.
    expect(within(popover).getByTestId('session-id-value')).toHaveTextContent('session-1')
    expect(within(popover).queryByTestId('info-row-worker')).toBeNull()
  })
})

/**
 * Render `AgentEditorPanel` with a signal-backed `agentId` so tests can move
 * the panel to another agent while a confirmation or request is open.
 */
function renderQueuePanel(
  options: Partial<Omit<AgentEditorPanelProps, 'agentId' | 'agent'>> & {
    queueHeadDispatch?: QueueHeadDispatch
  } = {},
) {
  const { queueHeadDispatch, ...rest } = options
  const [agentId, setAgentId] = createSignal('a1')
  const view = render(() => (
    <PreferencesProvider>
      <AgentEditorPanel
        agentId={agentId()}
        agent={agent(queueHeadDispatch !== undefined
          ? { queueHeadDispatch }
          : {})}
        repoGitStore={createRepoGitStore()}
        onSendMessage={() => {}}
        queueClientId="client-a"
        {...rest}
      />
    </PreferencesProvider>
  ))
  return { ...view, setAgentId }
}

// Each queue command carries the agent beside the item, because a queued
// item states no agent. These pin the agent each handler receives.
describe('agent editor panel queue commands', () => {
  /** A queue whose head the Worker marks steerable and preemptable. */
  const headQueue = (state = AgentInputState.QUEUED) => create(AgentInputQueueSnapshotSchema, {
    pauseReason: AgentInputQueuePauseReason.MANUAL,
    items: [
      {
        id: 'head-1',
        text: 'next turn',
        kind: AgentInputKind.USER_MESSAGE,
        state,
        canSteer: true,
        canPreempt: true,
      },
    ],
  })

  it('offers Steer for an agent that steers, and sends the panel agent with the item', async () => {
    const onSteerQueueItem = vi.fn().mockResolvedValue(undefined)
    const onPreemptQueueItem = vi.fn().mockResolvedValue(undefined)
    renderQueuePanel({
      inputQueue: headQueue(),
      queueHeadDispatch: QueueHeadDispatch.STEER,
      onSteerQueueItem,
      onPreemptQueueItem,
    })

    const row = screen.getByTestId('queued-input-head-1')
    expect(within(row).queryByRole('button', { name: 'Preempt' })).toBeNull()
    await fireEvent.click(within(row).getByRole('button', { name: 'Steer' }))

    expect(onSteerQueueItem).toHaveBeenCalledExactlyOnceWith('a1', expect.objectContaining({ id: 'head-1' }))
    expect(onPreemptQueueItem).not.toHaveBeenCalled()
  })

  it('offers Preempt for an agent that preempts, and sends the panel agent with the item', async () => {
    const onSteerQueueItem = vi.fn().mockResolvedValue(undefined)
    const onPreemptQueueItem = vi.fn().mockResolvedValue(undefined)
    renderQueuePanel({
      inputQueue: headQueue(),
      queueHeadDispatch: QueueHeadDispatch.PREEMPT,
      onSteerQueueItem,
      onPreemptQueueItem,
    })

    const row = screen.getByTestId('queued-input-head-1')
    expect(within(row).queryByRole('button', { name: 'Steer' })).toBeNull()
    await fireEvent.click(within(row).getByRole('button', { name: 'Preempt' }))

    expect(onPreemptQueueItem).toHaveBeenCalledExactlyOnceWith('a1', expect.objectContaining({ id: 'head-1' }))
    expect(onSteerQueueItem).not.toHaveBeenCalled()
  })

  // The panel hands its title lookup to the queue, which states the sender of a
  // message from another agent by the title of the sender's tab.
  it('states the sender of a queued agent message by the title that the panel resolves', () => {
    renderQueuePanel({
      inputQueue: create(AgentInputQueueSnapshotSchema, {
        items: [
          {
            id: 'from-agent',
            text: 'Run the tests',
            kind: AgentInputKind.AGENT_MESSAGE,
            state: AgentInputState.QUEUED,
            sender: { tabId: 'sender-tab', provider: AgentProvider.CLAUDE_CODE },
            editRefusal: 'an agent wrote this message, so it cannot be edited; delete it and send your own',
          },
        ],
      }),
      tabTitle: tabId => (tabId === 'sender-tab'
        ? 'Fix flaky spec'
        : undefined),
    })

    expect(screen.getByTestId('queued-input-sender-from-agent')).toHaveTextContent('Message from Fix flaky spec (Claude Code)')
  })

  // An agent that states no dispatch reads as NONE, so neither action shows
  // even for a head the Worker marks steerable and preemptable.
  it('offers neither action for an agent that states no head dispatch', () => {
    renderQueuePanel({
      inputQueue: headQueue(),
    })

    const row = screen.getByTestId('queued-input-head-1')
    expect(within(row).queryByRole('button', { name: 'Steer' })).toBeNull()
    expect(within(row).queryByRole('button', { name: 'Preempt' })).toBeNull()
  })

  it('retries a failed input without asking, for the panel agent', async () => {
    const onRetryQueueItem = vi.fn().mockResolvedValue(undefined)
    renderQueuePanel({
      inputQueue: headQueue(AgentInputState.FAILED),
      onRetryQueueItem,
    })

    await fireEvent.click(within(screen.getByTestId('queued-input-head-1')).getByRole('button', { name: 'Retry' }))

    expect(screen.queryByTestId('retry-uncertain-input-dialog')).toBeNull()
    expect(onRetryQueueItem).toHaveBeenCalledExactlyOnceWith('a1', expect.objectContaining({ id: 'head-1' }), false)
  })

  // The confirmation holds the agent it opened for. A tab switch while the
  // dialog is open must not send the retry to the agent the panel shows now.
  it('retries an uncertain input for the agent it asked about, after the panel moves', async () => {
    const onRetryQueueItem = vi.fn().mockResolvedValue(undefined)
    const { setAgentId } = renderQueuePanel({
      inputQueue: headQueue(AgentInputState.DELIVERY_UNCERTAIN),
      onRetryQueueItem,
    })

    await fireEvent.click(within(screen.getByTestId('queued-input-head-1')).getByRole('button', { name: 'Retry' }))
    setAgentId('a2')
    const dialog = screen.getByTestId('retry-uncertain-input-dialog')
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Retry' }))

    expect(onRetryQueueItem).toHaveBeenCalledExactlyOnceWith('a1', expect.objectContaining({ id: 'head-1' }), true)
  })

  it('catches synchronous throws from onSetQueuePaused and marks pause in flight before calling it', async () => {
    const onSetQueuePaused = vi.fn()
    renderQueuePanel({
      inputQueue: headQueue(),
      onSetQueuePaused,
    })
    onSetQueuePaused.mockImplementationOnce(() => {
      fireEvent.click(screen.getByTestId('queue-pause-button'))
      throw new Error('sync pause throw')
    })
    onSetQueuePaused.mockResolvedValueOnce(undefined)

    await fireEvent.click(screen.getByTestId('queue-pause-banner-resume'))
    expect(onSetQueuePaused).toHaveBeenCalledExactlyOnceWith('a1', false)

    await fireEvent.click(screen.getByTestId('queue-pause-banner-resume'))
    expect(onSetQueuePaused).toHaveBeenCalledTimes(2)
    expect(onSetQueuePaused).toHaveBeenNthCalledWith(2, 'a1', false)
  })

  it('disables pause controls while an advance request is in flight for the panel agent', () => {
    const actionStateStore = createAgentComposerActionStateStore()
    const onSetQueuePaused = vi.fn().mockResolvedValue(undefined)
    renderQueuePanel({
      inputQueue: headQueue(),
      actionStateStore,
      onSetQueuePaused,
    })

    actionStateStore.forAgent('a1').setAdvanceInFlight(true)
    expect(screen.getByTestId('queue-pause-button')).toBeDisabled()
    expect(screen.getByTestId('queue-pause-banner-resume')).toBeDisabled()

    actionStateStore.forAgent('a1').setAdvanceInFlight(false)
    expect(screen.getByTestId('queue-pause-button')).not.toBeDisabled()
    expect(screen.getByTestId('queue-pause-banner-resume')).not.toBeDisabled()
  })

  it('registers the panel handle at mount with pending input true until the editor builds', () => {
    renderQueuePanel({
      inputQueue: headQueue(),
    })

    const handle = getActiveChatPanel()
    expect(handle).toBeDefined()
    expect(handle!.hasPendingInput()).toBe(true)
    expect(handle!.send()).toBeUndefined()
  })
})
