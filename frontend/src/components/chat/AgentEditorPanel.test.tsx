import type { AgentEditorPanelProps } from './AgentEditorPanel'
import type { AgentInfo } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ControlRequest } from '~/stores/control.store'
import { create } from '@bufbuild/protobuf'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { CLAUDE_MODE } from '~/generated/contracts/claude-protocol'
import { AgentInputKind, AgentInputQueuePauseReason, AgentInputQueueSnapshotSchema, AgentInputState, AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { localStorageLoad, localStorageStore, PREFIX_CONTROL_STATE } from '~/lib/browserStorage'
import { clearDraft, loadDraft, saveDraft } from '~/lib/editor/draftPersistence'
import { createControlStore } from '~/stores/control.store'
import { getEditorRef } from '~/stores/editorRef.store'
import { getActiveChatPanel } from '~/stores/focusedChatPanel.store'
import { repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { stubBranchMenuActions } from '~/test-support/branchMenu'
import { hoverForTooltip } from '~/test-support/clipStub'
import { useTestStorage } from '~/test-support/persistentStorage'
import { AgentEditorPanel } from './AgentEditorPanel'
import { clearAttachments, getAttachments, queueEditDraftKey, setAttachments } from './attachments'
import '~/components/chat/providers'

// The asynchronous storage tier has no in-memory mirror, so these round-trips
// need a database to round-trip through.
useTestStorage()

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
  // `setImmediate` stays REAL. fake-indexeddb schedules its request callbacks on
  // it, and the saved-answer record these cases exercise lives in IndexedDB, so
  // freezing it would leave every read pending for the length of the test.
  // Everything the component itself schedules is still under the fake clock.
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] })
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
  onSettingChange?: AgentEditorPanelProps['onSettingChange']
  optionGroups?: AgentInfo['optionGroups']
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
        agent={agent({
          workerId,
          agentProvider: options.agentProvider ?? AgentProvider.CLAUDE_CODE,
          optionGroups: options.optionGroups,
        })}
        repoGitStore={repoGitStore}
        gitTab={gitTab}
        onSendMessage={() => {}}
        controlRequests={options.controlStore?.getRequests('a1')}
        onControlResponse={options.onControlResponse}
        onSettingChange={options.onSettingChange}
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
  it('removes the active control request without reading a null request', async () => {
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

  it('renders the next queued control request after removing the active request', async () => {
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
  it('keeps the active plan switches checked when a request queues behind it', async () => {
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
  it('empties the plan switches for a re-ask of the same request id', async () => {
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
  it('answers with the rendered request id and its per-instance claim token', async () => {
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
  it('answers with no claim token when the rendered request carries none', async () => {
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
  it('answers without a response handler and still clears the draft', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, { requestId: 'plan-1', payload: toolRequestPayload('ExitPlanMode') })
    saveDraft('a1-ctrl-plan-1', 'no handler', 0)
    renderPanel({ controlStore })

    expect(() => fireEvent.click(screen.getByTestId('plan-approve-btn'))).not.toThrow()

    expect((await loadDraft('a1-ctrl-plan-1')).content).toBe('')
  })

  // Answering discards the drafts of the ANSWERED request only: its editor text
  // and its saved selection state. A draft belonging to a queued sibling must
  // survive. That pins the cleanup to the rendered request's id and not to a
  // wider key.
  it('clears only the answered request drafts and ask state', async () => {
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
    localStorageStore(`${PREFIX_CONTROL_STATE}a1:plan-1`, { selections: { 0: ['Postgres'] } })
    localStorageStore(`${PREFIX_CONTROL_STATE}a1:bash-1`, { selections: { 0: ['MySQL'] } })
    renderPanel({ controlStore, onControlResponse })

    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    expect((await loadDraft('a1-ctrl-plan-1')).content).toBe('')
    expect((await loadDraft('a1-ctrl-plan-1-q-3')).content).toBe('not a key this request can write')
    expect(await localStorageLoad(`${PREFIX_CONTROL_STATE}a1:plan-1`)).toBeUndefined()
    expect((await loadDraft('a1-ctrl-bash-1')).content).toBe('queued sibling reason')
    expect(await localStorageLoad(`${PREFIX_CONTROL_STATE}a1:bash-1`)).toEqual({ selections: { 0: ['MySQL'] } })
  })

  // The per-page keys are derived from the question set, not from a fixed range,
  // so the cleanup clears exactly the pages the editor could have written. A
  // plan request has no questions and therefore no page keys at all -- a draft
  // under a page key it could never write must not be swept away with it.
  it('clears every per-page draft of the answered question set', async () => {
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
    addControlRequest(controlStore, { requestId: 'plan-1', payload: toolRequestPayload('ExitPlanMode'), claimToken: 'claim-1' })
    const modeGroup = {
      id: 'permissionMode',
      label: 'Approval',
      order: 30,
      mutable: true,
      defaultValue: CLAUDE_MODE.Default,
      currentValue: CLAUDE_MODE.Default,
      options: Object.values(CLAUDE_MODE).map(mode => ({ id: mode, name: mode })),
    } as unknown as AgentInfo['optionGroups'][number]
    renderPanel({ controlStore, onSettingChange: vi.fn(), optionGroups: [modeGroup] })
    await waitFor(() => expect(screen.getByRole('radiogroup', { name: 'Permissions' })).toBeInTheDocument())

    // The catalog drops the permissionMode group: no preset the banner could
    // offer is applicable, so the group must disappear rather than draw pills
    // that would silently do nothing.
    cleanup()
    renderPanel({ controlStore, onSettingChange: vi.fn(), optionGroups: [] })
    await waitFor(() => expect(screen.queryByTestId('control-permissions-pill-group')).not.toBeInTheDocument())
  })

  // The panel is the only scope that holds BOTH halves the opening choice needs
  // -- the live catalog and the confirmed values -- so it is the only place the
  // wiring from `activePermissionPreset` to the drawn pill can be checked.
  it('opens an ordinary request on the preset the session already has on', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, { requestId: 'bash-1', payload: toolRequestPayload('Bash'), claimToken: 'claim-1' })
    const modeGroup = (currentValue: string) => ({
      id: 'permissionMode',
      label: 'Approval',
      order: 30,
      mutable: true,
      defaultValue: CLAUDE_MODE.Default,
      currentValue,
      options: Object.values(CLAUDE_MODE).map(mode => ({ id: mode, name: mode })),
    } as unknown as AgentInfo['optionGroups'][number])
    const pill = () => within(screen.getByRole('radiogroup', { name: 'Permissions' }))

    // The session runs on Claude's bypass mode, so the group opens there and an
    // Allow keeps it there instead of dropping the session back to asking.
    const running = renderPanel({
      controlStore,
      onSettingChange: vi.fn(),
      optionGroups: [modeGroup(CLAUDE_MODE.BypassPermissions)],
    })
    await waitFor(() => expect(pill().getByRole('radio', { name: 'Bypass' })).toBeChecked())

    // Nothing on: the group opens on the option that changes nothing. Bypass
    // must never arrive without the user choosing it.
    running.unmount()
    cleanup()
    renderPanel({
      controlStore,
      onSettingChange: vi.fn(),
      optionGroups: [modeGroup(CLAUDE_MODE.Default)],
    })
    await waitFor(() => expect(pill().getByRole('radio', { name: 'Unchanged' })).toBeChecked())
    expect(pill().getByRole('radio', { name: 'Bypass' })).not.toBeChecked()
  })

  // The pill choice belongs to the request INSTANCE like a switch: a rebuild of
  // the control component must not reset it to Default.
  it('restores the permission pill choice of the rendered request instance after a remount', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, { requestId: 'plan-1', payload: toolRequestPayload('ExitPlanMode'), claimToken: 'claim-1' })
    const modeGroup = {
      id: 'permissionMode',
      label: 'Approval',
      order: 30,
      mutable: true,
      defaultValue: CLAUDE_MODE.Default,
      currentValue: CLAUDE_MODE.Default,
      options: Object.values(CLAUDE_MODE).map(mode => ({ id: mode, name: mode })),
    } as unknown as AgentInfo['optionGroups'][number]
    const bypassRadio = () => within(screen.getByRole('radiogroup', { name: 'Permissions' })).getByRole('radio', { name: 'Bypass' })
    const first = renderPanel({ controlStore, onSettingChange: vi.fn(), optionGroups: [modeGroup] })

    fireEvent.click(bypassRadio())
    expect(bypassRadio()).toBeChecked()

    first.unmount()
    renderPanel({ controlStore, onSettingChange: vi.fn(), optionGroups: [modeGroup] })

    // Polled: the saved record is read after the remount, not during it.
    await waitFor(() => expect(bypassRadio()).toBeChecked())
  })

  it('restores the plan switches of the rendered request instance after a remount', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, { requestId: 'plan-1', payload: toolRequestPayload('ExitPlanMode'), claimToken: 'claim-1' })
    const clearContext = () => screen.getByTestId('plan-clear-context-checkbox').querySelector('input[type="checkbox"]')!
    const first = renderPanel({ controlStore })

    fireEvent.click(clearContext())
    expect(clearContext()).toBeChecked()

    first.unmount()
    renderPanel({ controlStore })

    // Polled: the saved record is read after the remount, not during it.
    await waitFor(() => expect(clearContext()).toBeChecked())
  })

  // The same record covers every control's switches, not the plan pair alone.
  // A Codex permission prompt draws Remember from it, so one mechanism serves
  // both and a new switch needs no further work.
  it('restores a Codex permission prompt switch after a remount', async () => {
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

    // Polled: the saved record is read after the remount, not during it.
    await waitFor(() => expect(remember()).toBeChecked())
  })

  // The record is written for EVERY control request now, not only a question,
  // because a permission prompt and a plan approval carry switches. Answering
  // must therefore discard it: a record that outlived its request would be
  // storage that nothing can ever read again.
  it('discards the persisted switches of the answered request', async () => {
    const controlStore = createControlStore()
    const onControlResponse = vi.fn().mockResolvedValue(undefined)
    addControlRequest(controlStore, { requestId: 'plan-1', payload: toolRequestPayload('ExitPlanMode'), claimToken: 'claim-1' })
    renderPanel({ controlStore, onControlResponse })
    const key = `${PREFIX_CONTROL_STATE}a1:plan-1:claim-1`

    fireEvent.click(screen.getByTestId('plan-clear-context-checkbox').querySelector('input[type="checkbox"]')!)
    await waitFor(async () => {
      expect((await localStorageLoad<{ switches?: Record<string, boolean> }>(key))?.switches)
        .toEqual({ 'plan-clear-context-checkbox': true })
    })

    fireEvent.click(screen.getByTestId('plan-approve-btn'))

    expect(await localStorageLoad(key)).toBeUndefined()
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
  it('empties the answers for a question that reuses the request id', async () => {
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
  it('restores the saved answers of the rendered request instance only', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, { requestId: 'ask-1', payload: questionRequestPayload(), claimToken: 'claim-1' })
    localStorageStore(`${PREFIX_CONTROL_STATE}a1:ask-1:claim-1`, { selections: { 0: ['MySQL'] }, currentPage: 1 })
    localStorageStore(`${PREFIX_CONTROL_STATE}a1:ask-1:claim-2`, { selections: { 0: ['Postgres'] }, currentPage: 0 })
    renderPanel({ controlStore })

    await waitFor(() =>
      expect(screen.getByTestId('control-question-group')).toHaveTextContent('Which runtime?'))
  })
})

// What the keyboard layer asks the panel before it claims $mod+Enter. Getting
// this wrong loses messages, so each case pins one input that must count as
// "something to submit".
describe('agent editor panel composer emptiness', () => {
  /**
   * The handle the panel registered. Read through `getActiveChatPanel`, which
   * is the lookup the emptiness context uses -- it consults no focus, because
   * the steer action is about the current TAB and works from the transcript.
   *
   * Awaited because registration rides the editor's imperative refs, which land
   * after the first render.
   */
  async function registeredHandle() {
    await waitFor(() => expect(getActiveChatPanel()).toBeDefined())
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
      (getByRole('button', { name: /send/i }) as HTMLButtonElement).disabled,
      'the debounced signal has not caught up yet, which is the point',
    ).toBe(true)
  })

  it('reports something to submit while a control request waits for approval', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, { requestId: 'r1', payload: toolRequestPayload('Bash') })
    renderPanel({ controlStore })
    expect((await registeredHandle()).hasPendingInput()).toBe(true)
  })

  // An empty submit ANSWERS a permission prompt, but it answers an ask-user
  // question with nothing -- so that one is not something to submit.
  it('reports nothing to submit while an ask-user question waits', async () => {
    const controlStore = createControlStore()
    addControlRequest(controlStore, { requestId: 'r1', payload: questionRequestPayload() })
    renderPanel({ controlStore })
    expect((await registeredHandle()).hasPendingInput()).toBe(false)
  })
})

describe('agent editor panel', () => {
  it('always shows the queue pause control', () => {
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

  it('sizes every composer action with the shared small class', () => {
    // The footer slot's own rule states no size, so a button that omits this
    // class falls back to Oat's full-size metrics and breaks the row it shares
    // with the `[+]` button, whose height is derived from `.small`.
    renderPanel()

    expect(screen.getByTestId('queue-pause-button')).toHaveClass('outline', 'small')
    expect(screen.getByTestId('send-button')).toHaveClass('small')
    expect(screen.getByTestId('send-button')).not.toHaveClass('outline')
  })

  it('shows no pause banner while the queue runs', () => {
    renderPanel()
    expect(screen.queryByTestId('queue-pause-banner')).not.toBeInTheDocument()
  })

  it('turns the pause toggle into Resume Queue, in the label and in the name', () => {
    const snapshot = create(AgentInputQueueSnapshotSchema, {
      agentId: 'a1',
      paused: true,
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

  it('says Send will queue while the queue is paused', () => {
    const snapshot = create(AgentInputQueueSnapshotSchema, {
      agentId: 'a1',
      paused: true,
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
      agentId: 'a1',
      paused: true,
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
    expect(onSetQueuePaused).toHaveBeenCalledWith(false)
  })

  it('sends one pause RPC for two fast presses of Resume', async () => {
    let settle: () => void = () => {}
    const onSetQueuePaused = vi.fn(() => new Promise<void>((resolve) => {
      settle = resolve
    }))
    const snapshot = create(AgentInputQueueSnapshotSchema, {
      agentId: 'a1',
      paused: true,
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

    expect(onSendMessage).toHaveBeenCalledWith('', [attachment])
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

    // Fake `setTimeout` alone, so the 1000 ms debounce cannot fire on its own
    // while every assertion below runs. A full fake clock also stops the
    // FileReader that `addFiles` awaits, and the read never completes.
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    send?.()
    await waitFor(() => expect(onSendMessage).toHaveBeenCalledWith('queued text', undefined))
    expect(screen.getByTestId('file-input')).toBeDisabled()
    expect(screen.getByTestId('send-spinner')).toBeInTheDocument()

    finishEnqueue?.()
    await waitFor(() => expect(screen.getByTestId('file-input')).not.toBeDisabled())

    // The enqueue settled, so every attachment path opens again -- while the
    // spinner still shows, which is what proves the two are separate.
    expect(screen.getByTestId('send-spinner')).toBeInTheDocument()
    await expect(addFiles?.([new File(['hello'], 'hello.txt', { type: 'text/plain' })])).resolves.toBe(1)

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
    send?.()
    expect(onSendMessage).toHaveBeenCalledWith('draft a', [attachmentA])

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
    await waitFor(() => expect(document.querySelector('[data-testid="composer-editor"] .ProseMirror')).toHaveTextContent('saved a1 edit'))
    send?.()
    expect(onUpdateQueueItem).toHaveBeenCalledWith(expect.objectContaining({ agentId: 'a1' }), 'saved a1 edit', [])

    setAgentId('a2')
    await waitFor(() => expect(document.querySelector('[data-testid="composer-editor"] .ProseMirror')).toHaveTextContent('full a2'))
    finishSave?.()
    await waitFor(() => expect(screen.getByTestId('file-input')).not.toBeDisabled())

    expect(onBeginQueueEdit.mock.calls.filter(([item]) => item.agentId === 'a2')).toHaveLength(1)
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

  // The delete path and the cancel path share one cleanup helper. This pins the
  // delete call site: no edit is loaded here, so only the helper can drop the
  // draft that the deleted input left behind.
  it('forgets the draft of a deleted queued input', async () => {
    const draftKey = queueEditDraftKey('a1', 'queued-1')
    saveDraft(draftKey, 'stale edit', -1)
    const snapshot = create(AgentInputQueueSnapshotSchema, {
      agentId: 'a1',
      paused: true,
      items: [{
        id: 'queued-1',
        agentId: 'a1',
        text: 'queued preview',
        kind: AgentInputKind.USER_MESSAGE,
        state: AgentInputState.QUEUED,
      }],
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

    expect(onDeleteQueueItem).toHaveBeenCalledWith(expect.objectContaining({ id: 'queued-1' }))
    expect((await loadDraft(draftKey)).content).toBe('')
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
