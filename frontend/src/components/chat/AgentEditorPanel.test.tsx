import type { AgentEditorPanelProps } from './AgentEditorPanel'
import type { AgentInfo } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ControlRequest } from '~/stores/control.store'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen, waitFor, within } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { AgentInputKind, AgentInputQueueSnapshotSchema, AgentInputState, AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { localStorageGet, localStorageSet, PREFIX_CONTROL_STATE } from '~/lib/browserStorage'
import { clearDraft, loadDraft, saveDraft } from '~/lib/editor/draftPersistence'
import { createControlStore } from '~/stores/control.store'
import { repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { stubBranchMenuActions } from '~/test-support/branchMenu'
import { hoverForTooltip } from '~/test-support/clipStub'
import { AgentEditorPanel } from './AgentEditorPanel'
import { clearAttachments, getAttachments, setAttachments } from './attachments'
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
  for (const id of ['a1', 'a2', 'a1-queue-owned-a1', 'a2-queue-owned-a2']) {
    clearDraft(id)
    clearAttachments(id)
  }
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
  agentProvider?: AgentProvider
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
  return render(() => (
    <PreferencesProvider>
      <AgentEditorPanel
        agentId="a1"
        agent={agent({ workerId, agentProvider: options.agentProvider ?? AgentProvider.CLAUDE_CODE })}
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

/** A control request payload that specifies the tool the agent asks permission to run. */
function toolRequestPayload(toolName: string): Record<string, unknown> {
  return { request: { tool_name: toolName, input: {} } }
}

/**
 * A two-question AskUserQuestion payload, so the paging and the per-page draft
 * keys the editor really writes are live. `toolRequestPayload` cannot stand in:
 * `activeDraftKey` adds the `-q-<page>` suffix only for a question request.
 */
function questionRequestPayload(): Record<string, unknown> {
  return {
    request: {
      tool_name: 'AskUserQuestion',
      input: {
        questions: [
          { id: 'q1', question: 'Which database?', options: [{ label: 'Postgres' }, { label: 'MySQL' }] },
          { id: 'q2', question: 'Which runtime?', options: [{ label: 'Bun' }, { label: 'Node' }] },
        ],
      },
    },
  }
}

function addControlRequest(
  controlStore: ReturnType<typeof createControlStore>,
  request: Omit<ControlRequest, 'agentId'>,
) {
  controlStore.addRequest('a1', { agentId: 'a1', ...request })
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
  // silently unchecks them. The slot compares the request by IDENTITY, and a
  // queued sibling does not change the answered request's identity. A plain
  // read of the store list would instead rebuild the footer on every write.
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
  // writes, so the panel rebuilds the footer whatever the slot keys on.)
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

    const [request, content] = onControlResponse.mock.calls[0]
    const claimToken = request.claimToken
    expect(claimToken).toBe('claim-2')
    // `buildAllowResponse` adds the key only for a checked switch, so its
    // absence is what proves the cancelled instance's choice did not carry.
    expect(JSON.parse(new TextDecoder().decode(content as Uint8Array))).not.toHaveProperty('clearContext')
  })

  // The footer answers with the request instance it RENDERED, so the worker's
  // idempotency claim keys on the answered instance. Reading the store again at
  // click time would lose both values as soon as the store changed.
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
    const [request, content] = onControlResponse.mock.calls[0]
    expect(request.agentId).toBe('a1')
    expect(request.requestId).toBe('plan-1')
    expect(request.claimToken).toBe('claim-1')
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
    const [request] = onControlResponse.mock.calls[0]
    expect(request.requestId).toBe('plan-1')
    expect(request.claimToken).toBeUndefined()
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

  // Answering discards the drafts of the ANSWERED request only: its editor text
  // and its saved selection state. A draft belonging to a queued sibling must
  // survive. That pins the cleanup to the rendered request's id and not to a
  // wider key.
  it('clears only the answered request drafts and ask state', () => {
    const controlStore = createControlStore()
    const onControlResponse = vi.fn().mockResolvedValue(undefined)
    addControlRequest(controlStore, { requestId: 'plan-1', payload: toolRequestPayload('ExitPlanMode') })
    addControlRequest(controlStore, { requestId: 'bash-1', payload: toolRequestPayload('Bash') })
    saveDraft('a1-ctrl-plan-1', 'rejection reason', 0)
    saveDraft('a1-ctrl-bash-1', 'queued sibling reason', 0)
    // A plan request has no questions, so the editor can never write a page key
    // for it. The cleanup derives the page count from the request, so it must
    // leave this key alone rather than sweep a guessed range of page indices.
    saveDraft('a1-ctrl-plan-1-q-3', 'not a key this request can write', 0)
    localStorageSet(`${PREFIX_CONTROL_STATE}a1:plan-1`, { selections: { 0: ['Postgres'] } })
    localStorageSet(`${PREFIX_CONTROL_STATE}a1:bash-1`, { selections: { 0: ['MySQL'] } })
    renderPanel({ controlStore, onControlResponse })

    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    expect(loadDraft('a1-ctrl-plan-1').content).toBe('')
    expect(loadDraft('a1-ctrl-plan-1-q-3').content).toBe('not a key this request can write')
    expect(localStorageGet(`${PREFIX_CONTROL_STATE}a1:plan-1`)).toBeUndefined()
    expect(loadDraft('a1-ctrl-bash-1').content).toBe('queued sibling reason')
    expect(localStorageGet(`${PREFIX_CONTROL_STATE}a1:bash-1`)).toEqual({ selections: { 0: ['MySQL'] } })
  })

  // The per-page keys are derived from the question set, not from a fixed range,
  // so the cleanup clears exactly the pages the editor could have written. A
  // plan request has no questions and therefore no page keys at all -- a draft
  // under a page key it could never write must not be swept away with it.
  it('clears every per-page draft of the answered question set', () => {
    const controlStore = createControlStore()
    const onControlResponse = vi.fn().mockResolvedValue(undefined)
    addControlRequest(controlStore, { requestId: 'ask-1', payload: questionRequestPayload() })
    addControlRequest(controlStore, { requestId: 'bash-1', payload: toolRequestPayload('Bash') })
    saveDraft('a1-ctrl-ask-1-q-0', 'note for the first question', 0)
    saveDraft('a1-ctrl-ask-1-q-1', 'note for the second question', 0)
    saveDraft('a1-ctrl-bash-1', 'queued sibling reason', 0)
    renderPanel({ controlStore, onControlResponse })

    // One click per question: a single-select answer advances the page itself.
    fireEvent.click(screen.getByTestId('question-option-Postgres'))
    fireEvent.click(screen.getByTestId('question-option-Bun'))
    fireEvent.click(screen.getByTestId('control-submit-btn'))

    expect(onControlResponse).toHaveBeenCalledOnce()
    expect(loadDraft('a1-ctrl-ask-1-q-0').content).toBe('')
    expect(loadDraft('a1-ctrl-ask-1-q-1').content).toBe('')
    expect(loadDraft('a1-ctrl-bash-1').content).toBe('queued sibling reason')
  })

  // A remount is routine: the composer is rebuilt whenever the focused agent
  // changes, because `AppShell` renders it through a getter on `focusedAgentId`.
  // The switch belongs to the request INSTANCE, not to the component that drew
  // it, so it must come back checked -- otherwise Approve silently omits the
  // choice the user made.
  it('restores the plan switches of the rendered request instance after a remount', () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, { requestId: 'plan-1', payload: toolRequestPayload('ExitPlanMode'), claimToken: 'claim-1' })
    const clearContext = () => screen.getByTestId('plan-clear-context-checkbox').querySelector('input[type="checkbox"]')!
    const first = renderPanel({ controlStore })

    fireEvent.click(clearContext())
    expect(clearContext()).toBeChecked()

    first.unmount()
    renderPanel({ controlStore })

    expect(clearContext()).toBeChecked()
  })

  // The same record covers every control's switches, not the plan pair alone.
  // A Codex permission prompt draws Remember from it, so one mechanism serves
  // both and a new switch needs no further work.
  it('restores a Codex permission prompt switch after a remount', () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, {
      requestId: 'perm-1',
      claimToken: 'claim-1',
      payload: { method: 'item/permissions/requestApproval', params: { permissions: { read: ['/repo'] } } },
    })
    const remember = () => screen.getByTestId('control-remember-checkbox').querySelector('input[type="checkbox"]')!
    const first = renderPanel({ controlStore, agentProvider: AgentProvider.CODEX })

    fireEvent.click(remember())
    expect(remember()).toBeChecked()

    first.unmount()
    renderPanel({ controlStore, agentProvider: AgentProvider.CODEX })

    expect(remember()).toBeChecked()
  })

  // The record is written for EVERY control request now, not only a question,
  // because a permission prompt and a plan approval carry switches. Answering
  // must therefore discard it: a record that outlived its request would be
  // storage that nothing can ever read again.
  it('discards the persisted switches of the answered request', () => {
    const controlStore = createControlStore()
    const onControlResponse = vi.fn().mockResolvedValue(undefined)
    addControlRequest(controlStore, { requestId: 'plan-1', payload: toolRequestPayload('ExitPlanMode'), claimToken: 'claim-1' })
    renderPanel({ controlStore, onControlResponse })
    const key = `${PREFIX_CONTROL_STATE}a1:plan-1:claim-1`

    fireEvent.click(screen.getByTestId('plan-clear-context-checkbox').querySelector('input[type="checkbox"]')!)
    expect(localStorageGet<{ switches?: Record<string, boolean> }>(key)?.switches)
      .toEqual({ 'plan-clear-context-checkbox': true })

    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    expect(localStorageGet(key)).toBeUndefined()
    // The choice still reached the response; only the saved copy is gone.
    const [, content] = onControlResponse.mock.calls[0]
    expect(JSON.parse(new TextDecoder().decode(content as Uint8Array))).toHaveProperty('clearContext', true)
  })

  // A cancel and re-ask reuses the request id with a FRESH claim token, so the
  // queue holds two instances of one id. Answering the head makes the second one
  // active without changing that id. Two things have to key on the instance for
  // this to come back empty: the effect that resets the answers, and the key
  // those answers are saved under. An id alone leaves the new question already
  // answered, and one Submit click then sends what the user chose for the
  // instance that went away.
  it('empties the answers for a question that reuses the request id', () => {
    const controlStore = createControlStore()
    const revised = questionRequestPayload()
    ;(revised.request as { input: { questions: { question: string }[] } }).input.questions[0].question = 'Which cache?'
    addControlRequest(controlStore, { requestId: 'ask-1', payload: questionRequestPayload(), claimToken: 'claim-1' })
    addControlRequest(controlStore, { requestId: 'ask-1', payload: revised, claimToken: 'claim-2' })
    renderPanel({ controlStore })

    fireEvent.click(screen.getByTestId('question-option-Postgres'))
    expect(screen.getByTestId('control-question-group')).toHaveTextContent('Which runtime?')

    controlStore.removeRequest('a1', 'ask-1')

    expect(screen.getByTestId('control-question-group')).toHaveTextContent('Which cache?')
    expect(screen.getByTestId('control-submit-btn')).toBeDisabled()
  })

  // The saved answers follow the instance too, not only the reset. A remount
  // must restore the instance's OWN page and selections -- that is what the
  // saved copy is for -- and must not restore a sibling instance's.
  it('restores the saved answers of the rendered request instance only', () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, { requestId: 'ask-1', payload: questionRequestPayload(), claimToken: 'claim-1' })
    localStorageSet(`${PREFIX_CONTROL_STATE}a1:ask-1:claim-1`, { selections: { 0: ['MySQL'] }, currentPage: 1 })
    localStorageSet(`${PREFIX_CONTROL_STATE}a1:ask-1:claim-2`, { selections: { 0: ['Postgres'] }, currentPage: 0 })
    renderPanel({ controlStore })

    expect(screen.getByTestId('control-question-group')).toHaveTextContent('Which runtime?')
  })
})

describe('agent editor panel', () => {
  it('always shows the queue pause control', () => {
    renderPanel()
    expect(screen.getByTestId('queue-pause-button')).toHaveTextContent('Pause Queue')
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

    expect(onSendMessage).toHaveBeenCalledWith('', [attachment])
    expect(screen.getByTestId('file-input')).toBeDisabled()
    finishEnqueue?.()
    await waitFor(() => expect(screen.getByTestId('file-input')).not.toBeDisabled())
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
    send?.()
    expect(onSendMessage).toHaveBeenCalledWith('draft a', [attachmentA])

    setAgentId('a2')
    await waitFor(() => expect(document.querySelector('[data-testid="chat-editor"] .ProseMirror')).toHaveTextContent('draft b'))
    finishEnqueue?.()
    await waitFor(() => expect(screen.getByTestId('file-input')).not.toBeDisabled())

    expect(document.querySelector('[data-testid="chat-editor"] .ProseMirror')).toHaveTextContent('draft b')
    expect(loadDraft('a1').content).toBe('')
    expect(loadDraft('a2').content).toBe('draft b')
    expect(getAttachments('a1')).toEqual([])
    expect(getAttachments('a2')).toEqual([attachmentB])
  })

  it('does not clear a new agent queue edit when the old save finishes', async () => {
    vi.useRealTimers()
    const [agentId, setAgentId] = createSignal('a1')
    const queue = (id: string) => create(AgentInputQueueSnapshotSchema, {
      agentId: id,
      paused: true,
      items: [{
        id: `owned-${id}`,
        agentId: id,
        text: `preview ${id}`,
        kind: AgentInputKind.USER_MESSAGE,
        state: AgentInputState.QUEUED,
        editOwnerClientId: 'client-a',
      }],
    })
    const onBeginQueueEdit = vi.fn((item: { agentId: string }) => Promise.resolve({
      snapshot: queue(item.agentId),
      attachments: [],
      text: `full ${item.agentId}`,
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
    await waitFor(() => expect(document.querySelector('[data-testid="chat-editor"] .ProseMirror')).toHaveTextContent('saved a1 edit'))
    send?.()
    expect(onUpdateQueueItem).toHaveBeenCalledWith(expect.objectContaining({ agentId: 'a1' }), 'saved a1 edit', [])

    setAgentId('a2')
    await waitFor(() => expect(document.querySelector('[data-testid="chat-editor"] .ProseMirror')).toHaveTextContent('full a2'))
    finishSave?.()
    await waitFor(() => expect(screen.getByTestId('file-input')).not.toBeDisabled())

    expect(onBeginQueueEdit.mock.calls.filter(([item]) => item.agentId === 'a2')).toHaveLength(1)
    expect(document.querySelector('[data-testid="chat-editor"] .ProseMirror')).toHaveTextContent('full a2')
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
      agentId: 'a1',
      revision: owner ? 2n : 1n,
      paused: true,
      items: [{
        id: 'queued-1',
        agentId: 'a1',
        text: 'queued preview',
        kind: AgentInputKind.USER_MESSAGE,
        state: AgentInputState.QUEUED,
        editOwnerClientId: owner,
      }],
    })
    const [snapshot, setSnapshot] = createSignal(queueSnapshot(''))
    const edited = queueSnapshot('client-a')
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
          onCancelQueueEdit={() => {
            setSnapshot(queueSnapshot(''))
            return Promise.resolve()
          }}
        />
      </PreferencesProvider>
    ))

    await fireEvent.click(screen.getByRole('button', { name: 'Edit' }))
    await Promise.resolve()
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel Edit' }))
    await Promise.resolve()
    expect(getAttachments('a1')).toEqual([normalAttachment])
  })

  it('requires confirmation before it retries uncertain delivery', async () => {
    const onRetryQueueItem = vi.fn().mockResolvedValue(undefined)
    const snapshot = create(AgentInputQueueSnapshotSchema, {
      agentId: 'a1',
      paused: true,
      items: [{
        id: 'uncertain-1',
        agentId: 'a1',
        text: 'possibly delivered',
        kind: AgentInputKind.USER_MESSAGE,
        state: AgentInputState.DELIVERY_UNCERTAIN,
      }],
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
    expect(onRetryQueueItem).toHaveBeenCalledWith(expect.objectContaining({ id: 'uncertain-1' }), true)
  })

  it('reloads an edit that the same browser client owns', () => {
    const onBeginQueueEdit = vi.fn(() => new Promise<never>(() => {}))
    const snapshot = create(AgentInputQueueSnapshotSchema, {
      agentId: 'a1',
      paused: true,
      items: [{
        id: 'owned-1',
        agentId: 'a1',
        text: 'preview',
        kind: AgentInputKind.USER_MESSAGE,
        state: AgentInputState.QUEUED,
        editOwnerClientId: 'client-a',
      }],
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

    expect(onBeginQueueEdit).toHaveBeenCalledWith(expect.objectContaining({ id: 'owned-1' }), false)
    expect(screen.getByRole('button', { name: 'Resume Edit' })).toBeInTheDocument()
  })

  it('loads the current agent edit while another agent edit request waits', async () => {
    const [agentId, setAgentId] = createSignal('a1')
    const queue = (id: string) => create(AgentInputQueueSnapshotSchema, {
      agentId: id,
      items: [{
        id: `owned-${id}`,
        agentId: id,
        text: id,
        kind: AgentInputKind.USER_MESSAGE,
        state: AgentInputState.QUEUED,
        editOwnerClientId: 'client-a',
      }],
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
    expect(onBeginQueueEdit).toHaveBeenCalledWith(expect.objectContaining({ id: 'owned-a1' }), false)

    setAgentId('a2')
    await Promise.resolve()

    expect(onBeginQueueEdit).toHaveBeenCalledWith(expect.objectContaining({ id: 'owned-a2' }), false)
  })

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

  it('shows the checkout kind on the chip', () => {
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
