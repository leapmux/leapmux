import type { FileAttachment } from './attachments'
import type { ControlResponseHandlingProps } from './controlResponseHandling'
import type { ControlAnswer, ControlAnswerHandler } from './controls/types'
import type { ControlQuestion } from './model/question'
import type { ControlDecision } from '~/generated/proto/leapmux/v1/agent_control_pb'
import type { AsyncLocalKey } from '~/lib/browserStorage'
import type { ControlRequest } from '~/stores/control.store'
import { batch, createRenderEffect, createRoot, createSignal, untrack } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { CLAUDE_MODE } from '~/generated/contracts/claude-protocol'
import { CLINE_PERMISSION_MODE } from '~/generated/contracts/cline-protocol'
import { AgentActivityState, AgentProvider, ControlResponseState } from '~/generated/proto/leapmux/v1/agent_pb'
import * as browserStorage from '~/lib/browserStorage'
import { flushStorageWrites, localStorageLoad, localStorageStore, PREFIX_CONTROL_STATE } from '~/lib/browserStorage'
import { testControlRequest } from '~/test-support/controlRequests'
import { useTestStorage } from '~/test-support/persistentStorage'
import { useControlResponseHandling } from './controlResponseHandling'
import { approvePlanDecision, permissionDecision, questionDecision } from './controls/controlDecision'
import { createControlAnswerState } from './controls/types'

// The asynchronous storage tier has no in-memory mirror, so these round-trips
// need a database to round-trip through.
useTestStorage()

// A failed answer to a request that is no longer shown surfaces a toast. Mock the
// module so it does not reach the runtime `window.ot` global, which jsdom lacks.
vi.mock('~/components/common/Toast', () => ({
  showWarnToast: vi.fn(),
  showInfoToast: vi.fn(),
  showErrorToast: vi.fn(),
}))

/** A question request with these questions, as the Worker sends it. */
function questionRequest(requestId: string, questions: ControlQuestion[], fields: Partial<ControlRequest> = {}): ControlRequest {
  return testControlRequest({
    requestId,
    agentId: 'test-agent',
    prompt: { kind: 'question', questions },
    ...fields,
  })
}

/** A handler that records each answer and reports `completed` for it. */
function answerHandler(completed: boolean | void = true) {
  return vi.fn<ControlAnswerHandler>().mockResolvedValue(completed)
}

/** The answer of one call of a handler. */
function answerOf(handler: ReturnType<typeof answerHandler>, call = 0): ControlAnswer {
  const answer = handler.mock.calls[call]?.[1]
  if (!answer)
    throw new Error(`the handler received no answer in call ${call}`)
  return answer
}

/** The decision of one call of a handler, or a failure for a call that recorded. */
function decisionOf(handler: ReturnType<typeof answerHandler>, call = 0): ControlDecision {
  const answer = answerOf(handler, call)
  if (answer.kind !== 'decision')
    throw new Error(`call ${call} recorded rather than decided`)
  return answer.decision
}

/**
 * Waits until the saved answers of the active request are restored.
 *
 * A fresh answer state starts ready, and the restore effect marks it not ready only
 * when the root that holds the hook finishes its first run. One yield lets that run
 * end, so the wait then observes the restore and not the initial state.
 */
async function answersRestored(state: ReturnType<typeof createControlAnswerState>) {
  await Promise.resolve()
  await vi.waitFor(() => expect(state.ready()).toBe(true))
}

function setup(overrides?: Partial<ControlResponseHandlingProps>, answerState = createControlAnswerState()) {
  const onSendMessage = vi.fn()
  const props: ControlResponseHandlingProps = {
    agentId: 'test-agent',
    onSendMessage,
    ...overrides,
  }
  const resetEditorHeight = vi.fn()
  const result = useControlResponseHandling(
    props,
    answerState,
    () => undefined,
    resetEditorHeight,
  )
  return { result, onSendMessage, resetEditorHeight }
}

describe('questions with selected answers only', () => {
  /** One multiple-choice question whose runtime selects option '0' by itself. */
  function nativeDefaultQuestion(requestId: string, fields: Partial<ControlRequest>, options = [{ value: '0', label: 'Alpha' }]): ControlRequest {
    return questionRequest(requestId, [
      {
        id: 'style',
        question: 'Choose a style',
        multiSelect: true,
        allowEmpty: true,
        allowCustomText: false,
        initialSelections: ['0'],
        options,
      },
    ], fields)
  }

  it('applies native defaults when a question arrives after the composer mounts', async () => {
    const request = nativeDefaultQuestion('late-default-question', { agentId: 'late-default-agent', claimToken: 'late-default-token' })
    const [requests, setRequests] = createSignal<ControlRequest[]>([])
    const state = createControlAnswerState()
    const { dispose } = createRoot((dispose) => {
      useControlResponseHandling({
        agentId: request.agentId,
        get controlRequests() { return requests() },
        questionsOpen: true,
        onSendMessage: vi.fn(),
      }, state, () => undefined, vi.fn())
      return { dispose }
    })
    try {
      expect(state.selections()).toEqual({})
      setRequests([request])
      await vi.waitFor(() => {
        expect(state.ready()).toBe(true)
        expect(state.selections()).toEqual({
          0: ['0'],
        })
      })
    }
    finally {
      dispose()
    }
  })

  it.each([
    {
      label: 'a fresh request',
      draft: undefined,
      expected: ['0'],
      readFailure: false,
    },
    {
      label: 'an explicit empty draft',
      draft: [],
      expected: [],
      readFailure: false,
    },
    {
      label: 'a different saved selection',
      draft: ['1'],
      expected: ['1'],
      readFailure: false,
    },
    {
      label: 'a failed storage read',
      draft: undefined,
      expected: ['0'],
      readFailure: true,
    },
  ])('restores native choice defaults for $label', async ({ label, draft, expected, readFailure }) => {
    const request = nativeDefaultQuestion(`native-default-${label}`, { agentId: 'native-default-agent', claimToken: 'native-default-token' }, [
      { value: '0', label: 'Alpha' },
      { value: '1', label: 'Beta' },
    ])
    if (draft !== undefined) {
      localStorageStore(`${PREFIX_CONTROL_STATE}${request.agentId}:${request.requestId}:${request.claimToken}`, {
        selections: { 0: draft },
      })
      await flushStorageWrites()
    }
    const state = createControlAnswerState()
    const failedRead = readFailure
      ? vi.spyOn(browserStorage, 'localStorageLoad').mockRejectedValueOnce(new Error('The stored answer is unavailable.'))
      : undefined
    const readySelections: Array<Record<number, string[]>> = []
    let restoring = false
    const { dispose } = createRoot((dispose) => {
      useControlResponseHandling({
        agentId: request.agentId,
        controlRequests: [request],
        questionsOpen: true,
        onSendMessage: vi.fn(),
      }, state, () => undefined, vi.fn())
      createRenderEffect(() => {
        if (!state.ready()) {
          restoring = true
          return
        }
        if (restoring) {
          readySelections.push({
            ...untrack(state.selections),
          })
        }
      })
      return { dispose }
    })
    try {
      await vi.waitFor(() => {
        expect(state.ready()).toBe(true)
        expect(state.selections()).toEqual({ 0: expected })
      })
      expect(readySelections).toEqual([
        { 0: expected },
      ])
      if (readFailure)
        expect(failedRead).toHaveBeenCalledExactlyOnceWith(`${PREFIX_CONTROL_STATE}${request.agentId}:${request.requestId}:${request.claimToken}`)
    }
    finally {
      dispose()
      failedRead?.mockRestore()
    }
  })

  const selectOnly: ControlQuestion[] = [
    {
      question: 'Choose',
      allowCustomText: false,
      options: [
        { value: 'first', label: 'First' },
      ],
    },
  ]

  it('restores a selected answer without hidden custom text', async () => {
    const request = questionRequest('cursor-restored', selectOnly, { claimToken: 'cursor-token', agentProvider: AgentProvider.CURSOR })
    localStorageStore(`${PREFIX_CONTROL_STATE}test-agent:cursor-restored:cursor-token`, {
      selections: {
        0: ['first'],
      },
      customTexts: { 0: 'An old draft' },
      currentPage: 0,
    })
    await flushStorageWrites()
    const state = createControlAnswerState()
    const { handling, dispose } = createRoot(dispose => ({
      dispose,
      handling: useControlResponseHandling({
        agentId: 'test-agent',
        agent: { agentProvider: AgentProvider.CURSOR },
        controlRequests: [request],
        onSendMessage: vi.fn(),
      }, state, () => undefined, vi.fn()),
    }))
    try {
      await vi.waitFor(() => expect(state.selections()).toEqual({
        0: ['first'],
      }))
      expect(handling.editorPurpose()).toBe('none')
      expect(state.customTexts()[0] ?? '').toBe('')
      const decision = questionDecision(state, { questions: selectOnly })
      expect(decision.kind.case === 'question'
        ? decision.kind.value.answers
        : []).toMatchObject([
        {
          selected: ['first'],
          customText: '',
        },
      ])
    }
    finally {
      dispose()
    }
  })

  it('offers no custom editor and refuses an indirect text submission', async () => {
    const request = questionRequest('cursor-choice', selectOnly, { agentProvider: AgentProvider.CURSOR })
    const state = createControlAnswerState()
    const respond = answerHandler()
    const { handling, dispose } = createRoot(dispose => ({
      dispose,
      handling: useControlResponseHandling({
        agentId: 'test-agent',
        agent: { agentProvider: AgentProvider.CURSOR },
        controlRequests: [request],
        onControlAnswer: respond,
        onSendMessage: vi.fn(),
      }, state, () => undefined, vi.fn()),
    }))
    try {
      await vi.waitFor(() => expect(state.ready()).toBe(true))
      expect(handling.editorPurpose()).toBe('none')
      expect(handling.handleControlSend('Keep this answer.')).toBe(false)
      expect(respond).not.toHaveBeenCalled()
    }
    finally {
      dispose()
    }
  })
})

function setupWithAttachments(
  attachments: FileAttachment[],
  overrides?: Partial<ControlResponseHandlingProps>,
) {
  const onSendMessage = vi.fn()
  const props: ControlResponseHandlingProps = {
    agentId: 'test-agent',
    onSendMessage,
    ...overrides,
  }
  const resetEditorHeight = vi.fn()
  const result = useControlResponseHandling(
    props,
    createControlAnswerState(),
    () => undefined,
    resetEditorHeight,
    () => attachments,
  )
  return { result, onSendMessage, resetEditorHeight }
}

function makeAttachment(overrides: Partial<FileAttachment> = {}): FileAttachment {
  return {
    id: 'att-1',
    file: new File([], 'test.png'),
    filename: 'test.png',
    mimeType: 'image/png',
    data: new Uint8Array([0x89, 0x50]),
    size: 100,
    ...overrides,
  }
}

/** A permission request with LeapMux's own Allow and Deny, as the Worker reads an unknown tool. */
function makeControlRequest(requestId: string, agentId: string, payload: Record<string, unknown> = { tool_name: 'Bash', tool_input: {} }): ControlRequest {
  return testControlRequest({ requestId, agentId, payload })
}

describe('handleSend', () => {
  it('returns false for empty string', async () => {
    const { result, onSendMessage } = setup()
    expect(result.handleSend('')).toBe(false)
    expect(onSendMessage).not.toHaveBeenCalled()
  })

  it('returns false for whitespace-only string', async () => {
    const { result, onSendMessage } = setup()
    expect(result.handleSend('   ')).toBe(false)
    expect(onSendMessage).not.toHaveBeenCalled()
  })

  it('does not reset hasContent when the active control request changes on a tab switch', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const reqA = makeControlRequest('req-A', 'agent-A')
          const [controlRequests, setControlRequests] = createSignal<ControlRequest[]>([reqA])
          const [hasContent, setHasContent] = createSignal(false)

          const props: ControlResponseHandlingProps = {
            agentId: 'agent-A',
            get controlRequests() { return controlRequests() },
            onSendMessage: vi.fn(),
          }

          useControlResponseHandling(
            props,
            createControlAnswerState(),
            () => undefined,
            vi.fn(),
          )

          // Let the initial createEffect run (deferred in SolidJS 1.9+).
          await Promise.resolve()

          // Simulate user typing feedback — editor has content.
          setHasContent(true)
          expect(hasContent()).toBe(true)

          // Simulate switching to tab B (no control requests).
          setControlRequests([])
          // Let the active-request effect run.
          await Promise.resolve()

          // Simulate switching back to tab A (control request reappears).
          setControlRequests([reqA])
          // Let the active-request effect run.
          await Promise.resolve()

          // hasContent must NOT have been reset to false by the effect.
          // The MarkdownEditor's own content change listener is the
          // authoritative source for hasContent.
          expect(hasContent()).toBe(true)

          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e)
        }
      })
    }))

  it.each([
    ['single character', 'a'],
    ['single character with surrounding whitespace', '  x  '],
    ['2-character message', 'hi'],
    ['longer message', 'hello world'],
    ['single emoji', '😀'],
    ['multi-byte characters', '你好'],
  ])('calls onSendMessage for %s', (_, content) => {
    const { result, onSendMessage, resetEditorHeight } = setup()
    result.handleSend(content)
    expect(onSendMessage).toHaveBeenCalledWith(content, undefined)
    expect(resetEditorHeight).toHaveBeenCalled()
  })

  it('does not submit a second message while an asynchronous send is pending', async () => {
    let finish!: () => void
    const pending = new Promise<void>((resolve) => {
      finish = resolve
    })
    const onSendMessage = vi.fn().mockReturnValue(pending)
    const { result } = setup({ onSendMessage })

    const first = result.handleSend('first')
    expect(result.handleSend('second')).toBe(false)
    expect(onSendMessage).toHaveBeenCalledOnce()

    finish()
    await first
    result.handleSend('third')
    expect(onSendMessage).toHaveBeenCalledTimes(2)
  })

  it('passes attachments when present', async () => {
    const attachments = [
      makeAttachment(),
    ]
    const { result, onSendMessage } = setupWithAttachments(attachments)
    result.handleSend('look at this')
    expect(onSendMessage).toHaveBeenCalledWith('look at this', attachments)
  })

  it('passes undefined attachments when array is empty', async () => {
    const { result, onSendMessage } = setupWithAttachments([])
    result.handleSend('hello')
    expect(onSendMessage).toHaveBeenCalledWith('hello', undefined)
  })

  it('allows sending with empty text when attachments present', async () => {
    const attachments = [
      makeAttachment(),
    ]
    const { result, onSendMessage } = setupWithAttachments(attachments)
    const returned = result.handleSend('')
    // Should NOT return false — the send should proceed
    expect(returned).not.toBe(false)
    expect(onSendMessage).toHaveBeenCalledWith('', attachments)
  })

  it('blocks sending with empty text and no attachments', async () => {
    const { result, onSendMessage } = setupWithAttachments([])
    expect(result.handleSend('')).toBe(false)
    expect(onSendMessage).not.toHaveBeenCalled()
  })
})

// Saved answers survive a reload and a tab switch, and the read that restores
// them is asynchronous. Two effects therefore run against a moving target: the
// restore, which resets the state and then fills it in, and the persist, which
// writes whatever the state holds.
describe('restoring saved answers', () => {
  /** A control request whose answers this hook will persist under its own key. */
  function askRequest(requestId: string, claimToken: string, questions: ControlQuestion[] = [
    {
      header: 'Task',
      question: 'Pick a task',
      options: [
        { label: 'Build' },
        { label: 'Test' },
      ],
    },
  ]): ControlRequest {
    return questionRequest(requestId, questions, { claimToken })
  }

  function answerKey(request: ControlRequest): AsyncLocalKey {
    return `${PREFIX_CONTROL_STATE}test-agent:${request.requestId}:${request.claimToken}`
  }

  it('preserves a valid later page and its permitted custom answer', async () => {
    const request = askRequest('later-page', 'later-token', [
      {
        question: 'First question',
        options: [
          { label: 'Build' },
        ],
      },
      {
        question: 'Second question',
        options: [
          { label: 'Test' },
        ],
      },
    ])
    localStorageStore(answerKey(request), {
      selections: {
        0: ['Build'],
      },
      customTexts: { 1: 'A custom answer.' },
      currentPage: 1,
    })
    await flushStorageWrites()
    const state = createControlAnswerState()
    const dispose = createRoot((dispose) => {
      useControlResponseHandling({
        agentId: 'test-agent',
        agent: { agentProvider: AgentProvider.CLAUDE_CODE },
        controlRequests: [request],
        onSendMessage: vi.fn(),
      }, state, () => undefined, vi.fn())
      return dispose
    })
    try {
      await vi.waitFor(() => expect(state.customTexts()).toEqual({ 1: 'A custom answer.' }))
      expect(state.currentPage()).toBe(1)
      expect(state.selections()).toEqual({
        0: ['Build'],
      })
    }
    finally {
      dispose()
    }
  })

  it('rejects malformed stored fields while preserving valid falsy values', async () => {
    const request = askRequest('malformed-fields', 'fields-token')
    localStorageStore(answerKey(request), {
      selections: { 0: 1 },
      customTexts: { 0: false },
      currentPage: {},
      switches: { valid: false, invalid: 'false' },
      choices: { valid: '', invalid: 0 },
    })
    await flushStorageWrites()
    const state = createControlAnswerState()
    const dispose = createRoot((dispose) => {
      useControlResponseHandling({
        agentId: 'test-agent',
        controlRequests: [request],
        onSendMessage: vi.fn(),
      }, state, () => undefined, vi.fn())
      return dispose
    })
    try {
      await vi.waitFor(() => expect(state.switches()).toEqual({ valid: false }))
      expect(state.choices()).toEqual({ valid: '' })
      expect(state.selections()).toEqual({})
      expect(state.customTexts()).toEqual({})
      expect(state.currentPage()).toBe(0)
    }
    finally {
      dispose()
    }
  })

  it.each([
    -1,
    1,
    1.5,
    1_000_000,
    '0',
    null,
    false,
  ])('restores the first question for invalid saved page %s', async (currentPage) => {
    const request = askRequest('invalid-page', 'page-token')
    localStorageStore(answerKey(request), {
      selections: {
        0: ['Test'],
      },
      currentPage,
      customTexts: {},
    })
    await flushStorageWrites()
    const state = createControlAnswerState()
    const { handling, dispose } = createRoot(dispose => ({
      dispose,
      handling: useControlResponseHandling({
        agentId: 'test-agent',
        agent: { agentProvider: AgentProvider.CLAUDE_CODE },
        controlRequests: [request],
        onSendMessage: vi.fn(),
        onControlAnswer: answerHandler(),
      }, state, () => undefined, vi.fn()),
    }))
    try {
      await vi.waitFor(() => expect(state.selections()).toEqual({
        0: ['Test'],
      }))
      expect(state.currentPage()).toBe(0)
      expect(handling.editorPurpose()).toBe('answer')
      expect(await handling.handleControlSend('')).toBe(true)
    }
    finally {
      dispose()
    }
  })

  it('fills the state in from the saved answers of the active request', async () => {
    const request = askRequest('ask-1', 'tok-1')
    localStorageStore(answerKey(request), {
      selections: {
        0: ['Test'],
      },
      currentPage: 0,
      customTexts: {},
      switches: {},
    })
    await flushStorageWrites()

    const answerState = createControlAnswerState()
    const dispose = createRoot((disposeRoot) => {
      useControlResponseHandling(
        {
          agentId: 'test-agent',
          controlRequests: [request],
          onSendMessage: vi.fn(),
        },
        answerState,
        () => undefined,
        vi.fn(),
      )
      return disposeRoot
    })

    await vi.waitFor(() => expect(answerState.selections()).toEqual({
      0: ['Test'],
    }))
    dispose()
  })

  it('restores a pill group choice, and a choices-only record still lands', async () => {
    // A record carrying nothing but a pill selection is not blank: the restore
    // must land it, or a reload would silently reset the user's preset pick.
    const request = askRequest('ask-3', 'tok-3')
    localStorageStore(answerKey(request), {
      choices: { 'control-permissions-pill': 'bypass' },
    })
    await flushStorageWrites()

    const answerState = createControlAnswerState()
    const dispose = createRoot((disposeRoot) => {
      useControlResponseHandling(
        {
          agentId: 'test-agent',
          controlRequests: [request],
          onSendMessage: vi.fn(),
        },
        answerState,
        () => undefined,
        vi.fn(),
      )
      return disposeRoot
    })

    await vi.waitFor(() => expect(answerState.choices()).toEqual({ 'control-permissions-pill': 'bypass' }))
    dispose()
  })

  // THE RESTORE GUARD. A user clicking between two prompts swaps the active
  // request while the first one's answers are still being read. Whichever read
  // the database answers last must not decide what is on screen: landing the
  // OUTGOING request's answers under the INCOMING prompt is how a user submits
  // a choice they made for a different question.
  it('lands the newer request\'s answers when two swaps arrive together', async () => {
    const first = askRequest('ask-1', 'tok-1')
    const second = askRequest('ask-2', 'tok-2')
    localStorageStore(answerKey(first), {
      selections: {
        0: ['Build'],
      },
      currentPage: 0,
      customTexts: {},
      switches: {},
    })
    localStorageStore(answerKey(second), {
      selections: {
        0: ['Test'],
      },
      currentPage: 0,
      customTexts: {},
      switches: {},
    })
    await flushStorageWrites()

    const answerState = createControlAnswerState()
    const [requests, setRequests] = createSignal<ControlRequest[]>([first])
    const dispose = createRoot((disposeRoot) => {
      useControlResponseHandling(
        {
          agentId: 'test-agent',
          get controlRequests() { return requests() },
          onSendMessage: vi.fn(),
        },
        answerState,
        () => undefined,
        vi.fn(),
      )
      return disposeRoot
    })

    // The swap lands before the first restore's read can resolve, which is the
    // ordering the token exists for.
    setRequests([second])

    await vi.waitFor(() => expect(answerState.selections()).toEqual({
      0: ['Test'],
    }))
    // And it STAYS. The superseded read resolving afterwards must change
    // nothing at all.
    await flushStorageWrites()
    await new Promise(resolve => setTimeout(resolve, 0))
    expect(answerState.selections()).toEqual({
      0: ['Test'],
    })

    // The outgoing request's answers are still on disk: a swap is not an
    // answer, so nothing may overwrite them with the blank state the restore
    // installs on its way in.
    expect(await localStorageLoad(answerKey(first)))
      .toMatchObject({
        selections: {
          0: ['Build'],
        },
      })
    dispose()
  })
})

describe('handleControlSend', () => {
  it('sends a decision for the captured request through the answer handler', async () => {
    const request = testControlRequest({
      requestId: 'plan',
      agentId: 'test-agent',
      prompt: { kind: 'plan', text: '1. Do it' },
    })
    const onControlAnswer = answerHandler()
    const { result, dispose } = createRoot(dispose => ({
      dispose,
      result: setup({
        controlRequests: [request],
        onControlAnswer,
      }).result,
    }))
    try {
      const decision = approvePlanDecision({ permissionMode: '', clearContext: false })
      await result.decideFor(request)(decision)
      expect(onControlAnswer).toHaveBeenCalledExactlyOnceWith(request, { kind: 'decision', decision })
    }
    finally {
      dispose()
    }
  })

  it('records through the answer handler with no decision', async () => {
    const request = makeControlRequest('record', 'test-agent')
    const onControlAnswer = answerHandler()
    const { result, dispose } = createRoot(dispose => ({
      dispose,
      result: setup({
        controlRequests: [request],
        onControlAnswer,
      }).result,
    }))
    try {
      await result.recordResponse(request)
      expect(onControlAnswer).toHaveBeenCalledExactlyOnceWith(request, { kind: 'record' })
    }
    finally {
      dispose()
    }
  })

  it('cleans only the captured agent after the editor switches during delivery', async () => {
    let finish!: () => void
    const delivery = new Promise<void>((resolve) => {
      finish = resolve
    })
    const first: ControlRequest = {
      ...makeControlRequest('request', 'agent-A'),
      claimToken: 'token',
      agentProvider: AgentProvider.CLAUDE_CODE,
    }
    const second: ControlRequest = { ...first, agentId: 'agent-B' }
    const firstKey: AsyncLocalKey = `${PREFIX_CONTROL_STATE}agent-A:request:token`
    const secondKey: AsyncLocalKey = `${PREFIX_CONTROL_STATE}agent-B:request:token`
    await localStorageStore(firstKey, {
      choices: { scope: 'once' },
    }).durable
    await localStorageStore(secondKey, {
      choices: { scope: 'session' },
    }).durable
    const [agentId, setAgentId] = createSignal('agent-A')
    const [requests, setRequests] = createSignal([first])
    const state = createControlAnswerState()
    const reset = vi.fn()
    const { result, dispose } = createRoot(dispose => ({
      dispose,
      result: useControlResponseHandling({
        get agentId() { return agentId() },
        get controlRequests() { return requests() },
        onControlAnswer: () => delivery,
        onSendMessage: vi.fn(),
      }, state, () => undefined, reset),
    }))
    try {
      await vi.waitFor(() => expect(state.choices()).toEqual({ scope: 'once' }))
      const sending = result.decideFor(first)(permissionDecision('leapmux:allow'))
      batch(() => {
        setAgentId('agent-B')
        setRequests([second])
      })
      await vi.waitFor(() => expect(state.choices()).toEqual({ scope: 'session' }))
      finish()
      await sending
      await flushStorageWrites()
      expect(await localStorageLoad(firstKey)).toBeUndefined()
      expect(await localStorageLoad(secondKey)).toMatchObject({
        choices: { scope: 'session' },
      })
      expect(reset).not.toHaveBeenCalled()
    }
    finally {
      finish()
      dispose()
    }
  })

  it('keeps saved answers until delivery succeeds and permits retry after failure', async () => {
    let resolveDelivery!: () => void
    let rejectDelivery!: (error: Error) => void
    const delivery = new Promise<void>((resolve, reject) => {
      resolveDelivery = resolve
      rejectDelivery = reject
    })
    const onControlAnswer = vi.fn<ControlAnswerHandler>().mockReturnValueOnce(delivery).mockResolvedValue(undefined)
    const request = testControlRequest({
      requestId: 'pending',
      agentId: 'test-agent',
      agentProvider: AgentProvider.CLAUDE_CODE,
      claimToken: 'token',
      payload: {
        request: {
          tool_name: 'Bash',
          input: { command: 'pwd' },
        },
      },
    })
    const key: AsyncLocalKey = `${PREFIX_CONTROL_STATE}test-agent:pending:token`
    const state = createControlAnswerState()
    const reset = vi.fn()
    const { result, dispose } = createRoot(dispose => ({
      dispose,
      result: useControlResponseHandling({
        agentId: 'test-agent',
        controlRequests: [request],
        onControlAnswer,
        onSendMessage: vi.fn(),
      }, state, () => undefined, reset),
    }))
    try {
      await vi.waitFor(() => expect(state.ready()).toBe(true))
      state.setChoices({ scope: 'once' })
      await flushStorageWrites()
      const sending = Promise.resolve(result.handleControlSend('Use a safer command')).catch(() => {})
      await flushStorageWrites()
      expect(await localStorageLoad(key)).toMatchObject({
        choices: { scope: 'once' },
      })
      expect(reset).not.toHaveBeenCalled()
      rejectDelivery(new Error('offline'))
      await sending
      state.setChoices({ scope: 'session' })
      await flushStorageWrites()
      expect(await localStorageLoad(key)).toMatchObject({
        choices: { scope: 'session' },
      })
      await result.handleControlSend('Use a safer command')
      await flushStorageWrites()
      expect(await localStorageLoad(key)).toBeUndefined()
      expect(reset).toHaveBeenCalledOnce()
    }
    finally {
      resolveDelivery()
      dispose()
    }
  })

  // Answering discards the saved answers of the answered instance, and the
  // outcome must not depend on whether a caller batches.
  //
  // `trySubmitAskUserQuestion` writes the answers on its way in. Unbatched, the
  // persist effect runs at once, before the cleanup deletes the key. Inside a
  // batch it runs at the END, after the cleanup, and re-writes the key that the
  // cleanup just deleted. The answers of an instance the user already answered
  // would then outlive it. The cleanup therefore releases the ownership too, so
  // the effect writes nothing back in either arrangement.
  it('leaves no saved answers behind after answering a question', async () => {
    const answerState = createControlAnswerState({
      selections: {
        0: ['Build'],
      },
    })
    const onControlAnswer = answerHandler()
    const key = `${PREFIX_CONTROL_STATE}test-agent:ask-1:tok-1`
    const props: ControlResponseHandlingProps = {
      agentId: 'test-agent',
      agent: { agentProvider: AgentProvider.CLAUDE_CODE },
      controlRequests: [
        questionRequest('ask-1', [
          {
            header: 'Task',
            question: 'Pick a task',
            options: [
              { label: 'Build' },
            ],
          },
        ], { claimToken: 'tok-1' }),
      ],
      onControlAnswer,
      onSendMessage: vi.fn(),
    }
    let result!: ReturnType<typeof useControlResponseHandling>
    const dispose = createRoot((disposeRoot) => {
      result = useControlResponseHandling(props, answerState, () => undefined, vi.fn())
      return disposeRoot
    })

    // OUTSIDE the root, so the restore effect has already claimed the request as
    // the owner of the answers -- which is the state a real session answers in.
    answerState.setSelections({
      0: ['Build'],
    })
    expect(await localStorageLoad(key)).toBeDefined()
    await vi.waitFor(() => expect(answerState.ready()).toBe(true))

    await batch(() => result.handleControlSend(''))

    expect(onControlAnswer).toHaveBeenCalledOnce()
    expect(await localStorageLoad(key)).toBeUndefined()

    dispose()
  })

  it('sends the answers of a question as a question decision', async () => {
    createRoot((dispose) => {
      const onControlAnswer = answerHandler()
      const answerState = createControlAnswerState()
      answerState.setSelections({
        0: ['Build'],
      })
      const props: ControlResponseHandlingProps = {
        agentId: 'test-agent',
        agent: { agentProvider: AgentProvider.CLAUDE_CODE },
        controlRequests: [
          questionRequest('req-1', [
            {
              header: 'Task',
              question: 'Pick a task',
              options: [
                { label: 'Build' },
                { label: 'Test' },
              ],
            },
          ]),
        ],
        onControlAnswer,
        onSendMessage: vi.fn(),
      }
      const result = useControlResponseHandling(props, answerState, () => undefined, vi.fn())

      result.handleControlSend('')

      expect(onControlAnswer).toHaveBeenCalledOnce()
      const decision = decisionOf(onControlAnswer)
      expect(decision.kind.case === 'question'
        ? decision.kind.value.answers.map(({ selected, customText }) => ({ selected, customText }))
        : undefined).toEqual([
        {
          selected: ['Build'],
          customText: '',
        },
      ])
      dispose()
    })
  })

  // The user's own requirement: on a multiple choice, choosing A and typing X answers
  // A AND X, so the editor text joins the chosen options instead of replacing them.
  it('sends the chosen options and the typed text of a multiple choice together', async () => {
    await createRoot(async (dispose) => {
      try {
        const onControlAnswer = answerHandler()
        const answerState = createControlAnswerState({
          selections: {
            0: ['Red'],
          },
        })
        const { result } = setup({
          controlRequests: [
            questionRequest('colors', [
              {
                question: 'Which colors?',
                multiSelect: true,
                options: [
                  { label: 'Red' },
                  { label: 'Blue' },
                ],
              },
            ]),
          ],
          onControlAnswer,
        }, answerState)
        await answersRestored(answerState)
        expect(result.editorTextReplacesOptions()).toBe(false)

        await result.handleControlSend('Green')

        const decision = decisionOf(onControlAnswer)
        expect(decision.kind.case === 'question'
          ? decision.kind.value.answers.map(({ selected, customText }) => ({ selected, customText }))
          : undefined).toEqual([
          {
            selected: ['Red'],
            customText: 'Green',
          },
        ])
      }
      finally {
        dispose()
      }
    })
  })

  it('threads the active request\'s per-instance claimToken to onControlAnswer', async () => {
    createRoot((dispose) => {
      const onControlAnswer = answerHandler()
      const props: ControlResponseHandlingProps = {
        agentId: 'test-agent',
        agent: { agentProvider: AgentProvider.CLAUDE_CODE },
        controlRequests: [
          testControlRequest({
            requestId: 'req-1',
            agentId: 'test-agent',
            payload: {
              request: { tool_name: 'Bash', tool_input: {} },
            },
            claimToken: 'instance-token-7',
          }),
        ],
        onControlAnswer,
        onSendMessage: vi.fn(),
      }
      const result = useControlResponseHandling(props, createControlAnswerState(), () => undefined, vi.fn())

      result.handleControlSend('please stop')

      // The answer carries the whole request, so its claim token is the answered
      // instance's own. The worker's idempotency claim then keys on THIS instance,
      // and no store re-derivation can pair it with a sibling that reuses the id.
      expect(onControlAnswer).toHaveBeenCalledOnce()
      // Called once above; `?.` is the type-level guard alone.
      expect(onControlAnswer.mock.calls[0]?.[0].claimToken).toBe('instance-token-7')
      dispose()
    })
  })

  // The typed reason goes INSIDE the refusal. The Worker forwards it to a runtime that
  // takes no reason, so the composer sends no message of its own.
  it('refuses a permission with the typed text as the reason, and sends no message', async () => {
    await createRoot(async (dispose) => {
      try {
        const onControlAnswer = answerHandler()
        const onSendMessage = vi.fn()
        const { result } = setup({
          agent: { agentProvider: AgentProvider.CODEX },
          controlRequests: [
            testControlRequest({
              requestId: '7',
              agentId: 'test-agent',
              prompt: {
                kind: 'permission',
                permission: {
                  options: [
                    { optionId: 'accept', kind: 'allow_once', name: 'Allow' },
                    { optionId: 'cancel', kind: 'reject_once', name: 'Cancel' },
                  ],
                },
              },
            }),
          ],
          onControlAnswer,
          onSendMessage,
        })

        await result.handleControlSend('Use a safer command')

        const decision = decisionOf(onControlAnswer)
        expect(decision.kind.case === 'permission'
          ? decision.kind.value
          : undefined).toMatchObject({ optionId: 'cancel', feedback: 'Use a safer command' })
        expect(onSendMessage).not.toHaveBeenCalled()
      }
      finally {
        dispose()
      }
    })
  })

  it('approves a permission once for an empty send', async () => {
    await createRoot(async (dispose) => {
      try {
        const onControlAnswer = answerHandler()
        const { result } = setup({
          controlRequests: [
            makeControlRequest('req-1', 'test-agent'),
          ],
          onControlAnswer,
        })

        await result.handleControlSend('')

        const decision = decisionOf(onControlAnswer)
        expect(decision.kind.case === 'permission'
          ? decision.kind.value
          : undefined).toMatchObject({ optionId: 'leapmux:allow', feedback: '' })
      }
      finally {
        dispose()
      }
    })
  })

  it('refuses a plan with the typed text, and sends no message', async () => {
    await createRoot(async (dispose) => {
      try {
        const onControlAnswer = answerHandler()
        const onSendMessage = vi.fn()
        const { result } = setup({
          agent: { agentProvider: AgentProvider.PI },
          controlRequests: [
            testControlRequest({
              requestId: 'plan',
              agentId: 'test-agent',
              prompt: {
                kind: 'plan',
                text: '1. Do it',
                choices: [
                  { id: 'stay', label: 'Stay in Plan mode', effect: 'refuse' },
                ],
              },
            }),
          ],
          onControlAnswer,
          onSendMessage,
        })

        await result.handleControlSend('Revise the second step.')

        const decision = decisionOf(onControlAnswer)
        expect(decision.kind.case === 'plan'
          ? decision.kind.value
          : undefined).toMatchObject({ approve: false, feedback: 'Revise the second step.', choiceId: '' })
        expect(onSendMessage).not.toHaveBeenCalled()
      }
      finally {
        dispose()
      }
    })
  })

  it('sends nothing from the composer for a permission that offers no refusal', async () => {
    await createRoot(async (dispose) => {
      try {
        const onControlAnswer = answerHandler()
        const answerState = createControlAnswerState()
        const { result } = setup({
          controlRequests: [
            testControlRequest({
              requestId: 'allow-only',
              agentId: 'test-agent',
              prompt: {
                kind: 'permission',
                permission: {
                  options: [
                    { optionId: 'ok', kind: 'allow_once' },
                  ],
                },
              },
            }),
          ],
          onControlAnswer,
        }, answerState)
        await answersRestored(answerState)

        expect(result.handleControlSend('no')).toBe(false)
        expect(onControlAnswer).not.toHaveBeenCalled()
        // The same request takes an empty send, which proves that the refusal above
        // comes from the missing option and not from a request that is not ready.
        await expect(result.handleControlSend('')).resolves.toBe(true)
        expect(decisionOf(onControlAnswer).kind.value).toMatchObject({ optionId: 'ok' })
      }
      finally {
        dispose()
      }
    })
  })

  it('does not pass attachments to control responses', async () => {
    const onControlAnswer = answerHandler()
    const attachments = [
      makeAttachment(),
    ]
    const onSendMessage = vi.fn()
    const props: ControlResponseHandlingProps = {
      agentId: 'test-agent',
      agent: { agentProvider: AgentProvider.CLAUDE_CODE },
      controlRequests: [
        makeControlRequest('req-1', 'test-agent'),
      ],
      onControlAnswer,
      onSendMessage,
    }
    const resetEditorHeight = vi.fn()
    const answerState = createControlAnswerState()
    const result = useControlResponseHandling(
      props,
      answerState,
      () => undefined,
      resetEditorHeight,
      () => attachments,
    )
    await vi.waitFor(() => expect(answerState.ready()).toBe(true))
    // handleControlSend builds a control response — it should NOT include attachments.
    result.handleControlSend('')
    // onSendMessage should NOT have been called (it's a control response, not a user message).
    expect(onSendMessage).not.toHaveBeenCalled()
    // onControlAnswer should have been called (the allow decision).
    expect(onControlAnswer).toHaveBeenCalled()
  })

  // The Worker reads every provider's request, so the composer answers a request of an
  // agent whose provider the browser does not know yet.
  it('answers a request of an agent whose provider is not known yet', async () => {
    const onControlAnswer = answerHandler()
    const props: ControlResponseHandlingProps = {
      agentId: 'test-agent',
      controlRequests: [
        makeControlRequest('req-1', 'test-agent'),
      ],
      onControlAnswer,
      onSendMessage: vi.fn(),
    }
    const answerState = createControlAnswerState()
    const result = useControlResponseHandling(props, answerState, () => undefined, vi.fn())
    await vi.waitFor(() => expect(answerState.ready()).toBe(true))
    await expect(result.handleControlSend('')).resolves.toBe(true)
    expect(onControlAnswer).toHaveBeenCalledOnce()
  })

  it('advances multi-question requests instead of submitting incomplete answers', async () => {
    createRoot((dispose) => {
      const onControlAnswer = answerHandler()
      const answerState = createControlAnswerState()
      const editorContentRef = {
        get: () => 'Build',
        set: vi.fn(),
      }
      const props: ControlResponseHandlingProps = {
        agentId: 'test-agent',
        agent: { agentProvider: AgentProvider.CODEX },
        controlRequests: [
          questionRequest('7', [
            {
              header: 'Action',
              question: 'What next?',
              options: [
                { label: 'Build' },
              ],
            },
            {
              header: 'Env',
              question: 'Where?',
              options: [
                { label: 'Dev' },
              ],
            },
          ]),
        ],
        onControlAnswer,
        onSendMessage: vi.fn(),
      }
      const result = useControlResponseHandling(
        props,
        answerState,
        () => editorContentRef,
        vi.fn(),
      )

      expect(result.editorPurpose()).toBe('answer')
      const submitted = result.handleControlSend('Build')

      expect(submitted).toBe(false)
      expect(answerState.currentPage()).toBe(1)
      expect(editorContentRef.set).toHaveBeenCalledWith('')
      expect(onControlAnswer).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('sends a selection on one page and typed text on another', async () => {
    createRoot((dispose) => {
      const onControlAnswer = answerHandler()
      const answerState = createControlAnswerState()
      answerState.setSelections({
        0: ['Build'],
      })
      answerState.setCustomTexts({ 1: 'Dev' })
      const props: ControlResponseHandlingProps = {
        agentId: 'test-agent',
        agent: { agentProvider: AgentProvider.OPENCODE },
        controlRequests: [
          questionRequest('que-1', [
            {
              header: 'Task',
              question: 'Pick a task',
              options: [
                { label: 'Build' },
              ],
            },
            {
              header: 'Env',
              question: 'Pick an env',
              options: [
                { label: 'Dev' },
              ],
            },
          ]),
        ],
        onControlAnswer,
        onSendMessage: vi.fn(),
      }
      const result = useControlResponseHandling(props, answerState, () => undefined, vi.fn())

      expect(result.editorPurpose()).toBe('answer')
      result.handleControlSend('')

      expect(onControlAnswer).toHaveBeenCalledOnce()
      const decision = decisionOf(onControlAnswer)
      expect(decision.kind.case === 'question'
        ? decision.kind.value.answers.map(({ selected, customText }) => ({ selected, customText }))
        : undefined).toEqual([
        {
          selected: ['Build'],
          customText: '',
        },
        { selected: [], customText: 'Dev' },
      ])
      dispose()
    })
  })
})

/**
 * The Interrupt button is offered only when the click can actually land. Two
 * conditions decide that, and the cases below cover both.
 *
 * The Worker states whether anything is running, and its level is the only
 * input: it owns the turn bookkeeping, the background-task registry, the
 * pending prompts and the process state, so a second answer assembled here
 * would disagree with the one that the click reaches.
 *
 * And a subagent tab whose provider cannot interrupt one subagent gets no
 * button: the worker routes a child interrupt through a separate child
 * capability, and a provider without that capability returns
 * FailedPrecondition.
 */
describe('showInterrupt', () => {
  const pendingRequest = () => testControlRequest({ requestId: 'r1', agentId: 'test-agent' })

  it('shows the button while the agent works and nothing is being asked', async () => {
    createRoot((dispose) => {
      const { result } = setup({ agentActivity: AgentActivityState.WORKING })
      expect(result.showInterrupt()).toBe(true)
      dispose()
    })
  })

  it('hides the button when the agent is idle', async () => {
    createRoot((dispose) => {
      const { result } = setup({ agentActivity: AgentActivityState.IDLE })
      expect(result.showInterrupt()).toBe(false)
      dispose()
    })
  })

  it('hides the button for an agent nothing has reported on yet', async () => {
    createRoot((dispose) => {
      const { result } = setup({ agentActivity: undefined })
      expect(result.showInterrupt()).toBe(false)
      dispose()
    })
  })

  // An agent blocked on a question is mid-turn, and stopping it ends the turn
  // behind the question. That is when a reader most wants out, so the button
  // stays even though the indicator does not spin.
  it('shows the button while the agent is blocked on a question', async () => {
    createRoot((dispose) => {
      const { result } = setup({
        agentActivity: AgentActivityState.WAITING_FOR_USER,
        controlRequests: [
          pendingRequest(),
        ],
      })
      expect(result.showInterrupt()).toBe(true)
      dispose()
    })
  })

  // The case the request list alone cannot tell apart. A background shell can
  // ask for permission after its agent's turn ended, and the Worker calls that
  // IDLE: there is no turn left, so the button would offer a stop that stops
  // nothing.
  it('hides the button for a question with no turn behind it', async () => {
    createRoot((dispose) => {
      const { result } = setup({
        agentActivity: AgentActivityState.IDLE,
        controlRequests: [
          pendingRequest(),
        ],
      })
      expect(result.showInterrupt()).toBe(false)
      dispose()
    })
  })

  it('hides the button for a pending request on an agent that cannot be interrupted', async () => {
    createRoot((dispose) => {
      const { result } = setup({
        agentActivity: AgentActivityState.WAITING_FOR_USER,
        controlRequests: [
          pendingRequest(),
        ],
        canInterrupt: false,
      })
      expect(result.showInterrupt()).toBe(false)
      dispose()
    })
  })

  it('hides the button when this agent cannot be interrupted on its own', async () => {
    createRoot((dispose) => {
      const { result } = setup({ agentActivity: AgentActivityState.WORKING, canInterrupt: false })
      expect(result.showInterrupt()).toBe(false)
      dispose()
    })
  })

  it('treats an unset capability as interruptible (the root-agent default)', async () => {
    createRoot((dispose) => {
      const { result } = setup({ agentActivity: AgentActivityState.WORKING, canInterrupt: undefined })
      expect(result.showInterrupt()).toBe(true)
      dispose()
    })
  })

  it('shows the button again when an ignored interrupt restores working activity', () => {
    createRoot((dispose) => {
      const [activity, setActivity] = createSignal(AgentActivityState.WORKING)
      const result = useControlResponseHandling(
        {
          agentId: 'test-agent',
          onSendMessage: vi.fn(),
          get agentActivity() { return activity() },
        },
        createControlAnswerState(),
        () => undefined,
        vi.fn(),
      )

      expect(result.showInterrupt()).toBe(true)
      setActivity(AgentActivityState.IDLE)
      expect(result.showInterrupt()).toBe(false)
      setActivity(AgentActivityState.WORKING)
      expect(result.showInterrupt()).toBe(true)
      dispose()
    })
  })
})

// Both halves of the banner take these two, so the composer and the banner
// cannot read the same request differently, and one graph answers all three
// readers.
describe('activeControlSurface and activeControlProvider', () => {
  const question = (): ControlRequest => questionRequest('req-1', [
    {
      header: 'Task',
      question: 'Pick a task',
      options: [
        { label: 'Build' },
      ],
    },
  ])

  it('reads the prompt of the active request and reports its provider', () => {
    createRoot((dispose) => {
      const { result } = setup({
        agent: { agentProvider: AgentProvider.CLAUDE_CODE },
        controlRequests: [
          question(),
        ],
      })
      expect(result.activeControlProvider()).toBe(AgentProvider.CLAUDE_CODE)
      expect(result.activeControlSurface()?.kind).toBe('question')
      expect(result.isAskUserQuestion()).toBe(true)
      dispose()
    })
  })

  it('reads the generic prompt of an unknown request as a permission', () => {
    createRoot((dispose) => {
      const { result } = setup({
        agent: { agentProvider: AgentProvider.CLAUDE_CODE },
        controlRequests: [
          makeControlRequest('req-1', 'test-agent'),
        ],
      })
      expect(result.activeControlSurface()?.kind).toBe('permission')
      dispose()
    })
  })

  // The request's own provider wins over the agent's, so a queued request from
  // another provider reaches its own plugin.
  it('prefers the provider the request carries', () => {
    createRoot((dispose) => {
      const request = makeControlRequest('req-1', 'test-agent')
      request.agentProvider = AgentProvider.CODEX
      const { result } = setup({
        agent: { agentProvider: AgentProvider.CLAUDE_CODE },
        controlRequests: [request],
      })
      expect(result.activeControlProvider()).toBe(AgentProvider.CODEX)
      dispose()
    })
  })

  it('reports no surface once the store removes the request', () => {
    createRoot((dispose) => {
      const { result } = setup({
        agent: { agentProvider: AgentProvider.CLAUDE_CODE },
        controlRequests: [],
      })
      expect(result.activeControlSurface()).toBeUndefined()
      dispose()
    })
  })

  // ONE graph: both halves of the banner and the composer read the same memo,
  // so a read from three places recomputes nothing.
  it('answers every reader of the same request with one prompt', () => {
    createRoot((dispose) => {
      const { result } = setup({
        agent: { agentProvider: AgentProvider.CLAUDE_CODE },
        controlRequests: [
          question(),
        ],
      })
      expect(result.activeControlSurface()).toBe(result.activeControlSurface())
      dispose()
    })
  })
})

describe('activeControlRequest', () => {
  // `setup` spreads its overrides, which reads a getter once and freezes it, so
  // this suite builds a reactive `controlRequests` inline here.
  function reactiveController(initial: ControlRequest[] | undefined) {
    const [controlRequests, setControlRequests] = createSignal(initial)
    const props: ControlResponseHandlingProps = {
      agentId: 'agent-A',
      get controlRequests() { return controlRequests() },
      onSendMessage: vi.fn(),
    }
    const result = useControlResponseHandling(props, createControlAnswerState(), () => undefined, vi.fn())
    return { result, setControlRequests }
  }

  // Each write to the list notifies every reader of it, and the composer keys
  // its control slots on this value. A plain thunk would therefore rebuild the
  // banner and the footer each time an unrelated request joins or leaves the
  // queue. That rebuild discards the plan switches that the user already
  // checked.
  it('notifies on a new head only, not on every write to the list', async () => {
    const head = makeControlRequest('req-head', 'agent-A')
    // A RENDER effect, because that is how the composer subscribes: `insert()`
    // builds one. Solid queues it in `Effects`, and `completeUpdates` drains
    // `Effects` before `runUpdates` returns, so each assertion reads the runs of
    // the write above it. The writes stay OUTSIDE the root, because
    // `createRoot` runs its callback inside `runUpdates`: it batches a write
    // made in there, and the effect then flushes only after every assertion.
    const runs: Array<ControlRequest | null> = []
    let setControlRequests!: (reqs: ControlRequest[]) => void
    const dispose = createRoot((disposeRoot) => {
      const controller = reactiveController([head])
      setControlRequests = controller.setControlRequests
      createRenderEffect(() => runs.push(controller.result.activeControlRequest()))
      return disposeRoot
    })

    expect(runs).toEqual([head])

    setControlRequests([
      head,
      makeControlRequest('req-queued', 'agent-A'),
    ])
    expect(runs).toEqual([head])

    setControlRequests([head])
    expect(runs).toEqual([head])

    const next = makeControlRequest('req-next', 'agent-A')
    setControlRequests([next])
    expect(runs).toEqual([head, next])

    dispose()
  })

  // A cancel and re-ask reuses the request_id with a FRESH claim token, so the
  // store holds two instances of one id (`control.store.ts` addRequest). The
  // reset must key on the INSTANCE: an id dependency does not notify for that
  // swap, and the new prompt then opens with the answers the user gave for the
  // instance that went away.
  it('resets the ask state for a sibling that reuses the request id', async () => {
    const questions: ControlQuestion[] = [
      {
        question: 'Which database?',
        options: [
          { label: 'Postgres' },
        ],
      },
      {
        question: 'Which region?',
        options: [
          { label: 'EU' },
        ],
      },
    ]
    const first = questionRequest('req-1', questions, { agentId: 'agent-A', claimToken: 'claim-1' })
    const second = questionRequest('req-1', questions, { agentId: 'agent-A', claimToken: 'claim-2' })
    const answerState = createControlAnswerState()
    let setControlRequests!: (reqs: ControlRequest[]) => void
    const dispose = createRoot((disposeRoot) => {
      const [controlRequests, setter] = createSignal<ControlRequest[]>([first, second])
      setControlRequests = setter
      const props: ControlResponseHandlingProps = {
        agentId: 'agent-A',
        get controlRequests() { return controlRequests() },
        onSendMessage: vi.fn(),
      }
      useControlResponseHandling(props, answerState, () => undefined, vi.fn())
      return disposeRoot
    })

    answerState.setSelections({
      0: ['Postgres'],
    })
    answerState.setCustomTexts({ 0: 'my own answer' })
    answerState.setCurrentPage(1)

    setControlRequests([second])

    expect(answerState.selections()).toEqual({})
    expect(answerState.customTexts()).toEqual({})
    expect(answerState.currentPage()).toBe(0)

    dispose()
  })

  it('reports no active request for an empty or absent list', async () => {
    createRoot((dispose) => {
      const { result, setControlRequests } = reactiveController([])

      expect(result.activeControlRequest()).toBeNull()

      setControlRequests(undefined)
      expect(result.activeControlRequest()).toBeNull()

      dispose()
    })
  })

  /** One non-blocking question: the agent continues and the question waits. */
  function nonBlockingQuestion(): ControlRequest {
    return questionRequest('q1', [
      {
        question: 'Choose',
        options: [
          { label: 'First' },
        ],
      },
    ], { claimToken: 'tok-q1', nonBlocking: true })
  }

  // The one rule for a non-blocking question: the composer shows it only
  // after the reader opens the questions. Until then the composer stays free
  // and the question waits behind it.
  it('keeps a lone non-blocking question closed until the reader opens the questions', () => {
    createRoot((dispose) => {
      const [questionsOpen, setQuestionsOpen] = createSignal(false)
      const request = nonBlockingQuestion()
      // A getter, not a plain field: the composer reads the flag live, exactly
      // as the panel does from its store-backed prop.
      const props: ControlResponseHandlingProps = {
        agentId: 'test-agent',
        get questionsOpen() { return questionsOpen() },
        onQuestionsOpenChange: open => setQuestionsOpen(open),
        controlRequests: [request],
        onSendMessage: vi.fn(),
      }
      const result = useControlResponseHandling(props, createControlAnswerState(), () => undefined, vi.fn())

      expect(result.activeControlRequest()).toBeNull()
      expect(result.pendingQuestionCount()).toBe(1)

      result.openQuestions()
      expect(result.activeControlRequest()).toBe(request)

      result.closeQuestions()
      expect(result.activeControlRequest()).toBeNull()
      dispose()
    })
  })

  it('returns a blocking request at once', () => {
    createRoot((dispose) => {
      const request = makeControlRequest('b1', 'test-agent')
      const result = useControlResponseHandling({
        agentId: 'test-agent',
        controlRequests: [request],
        questionsOpen: false,
        onSendMessage: vi.fn(),
      }, createControlAnswerState(), () => undefined, vi.fn())
      expect(result.activeControlRequest()).toBe(request)
      expect(result.pendingQuestionCount()).toBe(0)
      dispose()
    })
  })
})

describe('a request whose payload LeapMux cannot read', () => {
  // The banner offers no decision for a faulted request, so the composer must not offer
  // to send one either: a rejection reason still builds a DENY, and composing one needs
  // the option list or decision vocabulary the unreadable payload was carrying. The stop
  // is the way out. See RL-002.
  it('offers no editor, and sends nothing when one is driven anyway', async () => {
    const onControlAnswer = answerHandler()
    // The Worker states no editor for a request that it cannot read.
    const request = testControlRequest({
      agentId: 'test-agent',
      requestId: 'faulted',
      editorRole: 'none',
      payloadFault: 'malformed',
      originalPayload: new TextEncoder().encode('{not json'),
    })
    const state = createControlAnswerState()
    const { handling, dispose } = createRoot(dispose => ({
      dispose,
      handling: useControlResponseHandling({
        agentId: 'test-agent',
        agent: { agentProvider: AgentProvider.GOOSE },
        controlRequests: [request],
        onControlAnswer,
        onSendMessage: vi.fn(),
      }, state, () => undefined, vi.fn()),
    }))
    try {
      await vi.waitFor(() => expect(state.ready()).toBe(true))
      expect(handling.editorPurpose()).toBe('none')
      expect(handling.editorPlaceholder()).toBeUndefined()
      expect(handling.handleControlSend('why did this fail')).toBe(false)
      expect(onControlAnswer).not.toHaveBeenCalled()
    }
    finally {
      dispose()
    }
  })

  // The turn is still blocked, and a payload nobody can read leaves the stop as
  // the ONLY way out -- there is no question to answer. The Worker says the turn
  // is blocked the same way it does for a readable prompt, so the button reads
  // its level and the fault changes nothing here.
  // A fault offers no editor even under an editor role that states one.
  it('offers no editor for a fault whatever the editor role says', async () => {
    const request = testControlRequest({
      agentId: 'test-agent',
      requestId: 'faulted',
      editorRole: 'feedback',
      payloadFault: 'not-an-object',
    })
    const state = createControlAnswerState()
    const { handling, dispose } = createRoot(dispose => ({
      dispose,
      handling: useControlResponseHandling({
        agentId: 'test-agent',
        controlRequests: [request],
        onControlAnswer: answerHandler(),
        onSendMessage: vi.fn(),
      }, state, () => undefined, vi.fn()),
    }))
    try {
      await vi.waitFor(() => expect(state.ready()).toBe(true))
      expect(handling.editorPurpose()).toBe('none')
    }
    finally {
      dispose()
    }
  })

  it('still offers the stop', async () => {
    const request = testControlRequest({
      agentId: 'test-agent',
      requestId: 'faulted',
      editorRole: 'none',
      payloadFault: 'malformed',
    })
    const state = createControlAnswerState()
    const { handling, dispose } = createRoot(dispose => ({
      dispose,
      handling: useControlResponseHandling({
        agentId: 'test-agent',
        agent: { agentProvider: AgentProvider.GOOSE },
        controlRequests: [request],
        agentActivity: AgentActivityState.WAITING_FOR_USER,
        onControlAnswer: answerHandler(),
        onSendMessage: vi.fn(),
      }, state, () => undefined, vi.fn()),
    }))
    try {
      await vi.waitFor(() => expect(state.ready()).toBe(true))
      expect(handling.showInterrupt()).toBe(true)
    }
    finally {
      dispose()
    }
  })
})

describe('elicitation composer submission', () => {
  it('prevents a hidden editor from submitting form answers', async () => {
    const onControlResponse = answerHandler()
    const request = testControlRequest({
      agentId: 'test-agent',
      requestId: 'form-submit',
      claimToken: 'form-token',
      prompt: {
        kind: 'elicitation',
        elicitation: {
          mode: 'form',
          message: '',
          schema: {
            type: 'object',
            required: ['count'],
            properties: {
              count: { type: 'integer' },
            },
          },
        },
      },
    })
    const state = createControlAnswerState()
    const { handling, dispose } = createRoot(dispose => ({
      dispose,
      handling: useControlResponseHandling({
        agentId: 'test-agent',
        agent: { agentProvider: AgentProvider.GOOSE },
        controlRequests: [request],
        onControlAnswer: onControlResponse,
        onSendMessage: vi.fn(),
      }, state, () => undefined, vi.fn()),
    }))
    try {
      await vi.waitFor(() => expect(state.ready()).toBe(true))
      expect(handling.editorPurpose()).toBe('none')
      expect(handling.editorPlaceholder()).toBeUndefined()
      expect(handling.handleControlSend('Keep this note')).toBe(false)
      expect(onControlResponse).not.toHaveBeenCalled()
      expect(handling.handleControlSend('')).toBe(false)
      state.setChoices({ 'elicitation:"count"': '0' })
      expect(handling.handleControlSend('')).toBe(false)
      expect(onControlResponse).not.toHaveBeenCalled()
    }
    finally {
      dispose()
    }
  })
})

// A control response that the worker RECORDS but does not complete leaves the
// request open. `handleControlSend` resolved with nothing for it, and the editor
// refuses only on a literal `false` -- so the composer cleared the text the user
// still had to send. See SCAN-S8-1.
describe('handleControlSend completion', () => {
  const denyRequest = (): ControlRequest => testControlRequest({
    requestId: 'open',
    agentId: 'test-agent',
    agentProvider: AgentProvider.CLAUDE_CODE,
    payload: {
      request: {
        tool_name: 'Bash',
        input: { command: 'pwd' },
      },
    },
  })

  async function sendWith(completed: boolean | undefined) {
    const request = denyRequest()
    const onControlResponse = answerHandler(completed)
    const state = createControlAnswerState()
    const { result, dispose } = createRoot(dispose => ({
      dispose,
      result: useControlResponseHandling(
        {
          agentId: 'test-agent',
          controlRequests: [request],
          onControlAnswer: onControlResponse,
          onSendMessage: vi.fn(),
        },
        state,
        () => undefined,
        vi.fn(),
      ),
    }))
    try {
      await vi.waitFor(() => expect(state.ready()).toBe(true))
      return await result.handleControlSend('No, use a safer command')
    }
    finally {
      dispose()
    }
  }

  it('refuses the send when the worker leaves the request open', async () => {
    expect(await sendWith(false)).toBe(false)
  })

  it('reports the send when the worker completes the response', async () => {
    expect(await sendWith(true)).toBe(true)
  })

  // A handler that answers nothing is the LEGACY shape, and it means completed.
  it('treats a handler that reports nothing as completed', async () => {
    expect(await sendWith(undefined)).toBe(true)
  })
})

// The three presentation surfaces all refuse to offer a decision for a request
// whose bytes LeapMux could not read, and the one place that SENDS tested the
// delivery state alone -- so a faulted request in the READY state passed it.
// See ALTITUDE-FE-4.
describe('submitResponse payload fault', () => {
  const faulted = (): ControlRequest => testControlRequest({
    requestId: 'faulted',
    agentId: 'test-agent',
    agentProvider: AgentProvider.CLAUDE_CODE,
    editorRole: 'none',
    payloadFault: 'malformed',
    responseState: ControlResponseState.READY,
  })

  it('refuses an answer and still admits the recording', async () => {
    const request = faulted()
    const onControlResponse = answerHandler()
    const { result, dispose } = createRoot(dispose => ({
      dispose,
      result: setup({
        controlRequests: [request],
        onControlAnswer: onControlResponse,
      }).result,
    }))
    try {
      await expect(result.decideFor(request)(permissionDecision('leapmux:allow'))).rejects.toThrow('Check the saved response state before sending an answer.')
      expect(onControlResponse).not.toHaveBeenCalled()
      await result.recordResponse(request)
      expect(onControlResponse).toHaveBeenCalledOnce()
      expect(answerOf(onControlResponse)).toEqual({ kind: 'record' })
    }
    finally {
      dispose()
    }
  })
})

// Shift+Tab leaves Plan for the mode the agent left it from. The panel serves
// several agents in turn, and an agent can load already in Plan, so the target
// must belong to the agent and fall back to ITS provider's default.
describe('togglePlanMode', () => {
  type AgentView = ControlResponseHandlingProps['agent']

  function setupToggle(initial: { agentId: string, agent: AgentView, settingsLoading?: boolean }) {
    const [agentId, setAgentId] = createSignal(initial.agentId)
    const [agent, setAgent] = createSignal<AgentView>(initial.agent)
    const onSettingChange = vi.fn()
    const { result, dispose } = createRoot(dispose => ({
      dispose,
      result: useControlResponseHandling({
        get agentId() { return agentId() },
        get agent() { return agent() },
        settingsLoading: initial.settingsLoading ?? false,
        onSettingChange,
        onSendMessage: vi.fn(),
      }, createControlAnswerState(), () => undefined, vi.fn()),
    }))
    /** Show another agent in the panel, as a tab switch does. */
    const show = (id: string, view: AgentView) => batch(() => {
      setAgentId(id)
      setAgent(view)
    })
    /** The mode the last toggle asked for. */
    const lastRequestedMode = () => onSettingChange.mock.lastCall?.[0]?.sets.permissionMode
    return {
      result,
      dispose,
      onSettingChange,
      setAgent,
      show,
      lastRequestedMode,
    }
  }

  const cline = (permissionMode: string): AgentView => ({
    agentProvider: AgentProvider.CLINE,
    optionValues: { permissionMode },
  })
  const claude = (permissionMode: string): AgentView => ({
    agentProvider: AgentProvider.CLAUDE_CODE,
    optionValues: { permissionMode },
  })

  it('enters plan mode from the current mode', () => {
    const toggle = setupToggle({
      agentId: 'agent-claude',
      agent: claude(CLAUDE_MODE.AcceptEdits),
    })
    try {
      toggle.result.togglePlanMode()
      expect(toggle.onSettingChange).toHaveBeenCalledExactlyOnceWith({
        sets: { permissionMode: CLAUDE_MODE.Plan },
      })
    }
    finally {
      toggle.dispose()
    }
  })

  it('returns to the mode the agent left plan mode from', () => {
    const toggle = setupToggle({
      agentId: 'agent-claude',
      agent: claude(CLAUDE_MODE.AcceptEdits),
    })
    try {
      toggle.result.togglePlanMode()
      toggle.setAgent(claude(CLAUDE_MODE.Plan))
      toggle.result.togglePlanMode()
      expect(toggle.lastRequestedMode()).toBe(CLAUDE_MODE.AcceptEdits)
    }
    finally {
      toggle.dispose()
    }
  })

  it('leaves plan mode for the provider default when the agent loads already in plan mode', () => {
    // A reload mounts the panel before the agent hydrates, so the first read
    // knows no provider at all.
    const toggle = setupToggle({ agentId: 'agent-cline', agent: undefined })
    try {
      toggle.setAgent(cline(CLINE_PERMISSION_MODE.Plan))
      toggle.result.togglePlanMode()
      expect(toggle.onSettingChange).toHaveBeenCalledExactlyOnceWith({
        sets: { permissionMode: CLINE_PERMISSION_MODE.Act },
      })
    }
    finally {
      toggle.dispose()
    }
  })

  it('does not carry one agent\'s remembered mode to another agent', () => {
    const toggle = setupToggle({
      agentId: 'agent-claude',
      agent: claude(CLAUDE_MODE.AcceptEdits),
    })
    try {
      toggle.result.togglePlanMode()
      toggle.setAgent(claude(CLAUDE_MODE.Plan))

      toggle.show('agent-cline', cline(CLINE_PERMISSION_MODE.Plan))
      toggle.result.togglePlanMode()

      expect(toggle.lastRequestedMode()).toBe(CLINE_PERMISSION_MODE.Act)
    }
    finally {
      toggle.dispose()
    }
  })

  it('keeps each agent\'s remembered mode across a switch to another agent and back', () => {
    const toggle = setupToggle({
      agentId: 'agent-claude',
      agent: claude(CLAUDE_MODE.AcceptEdits),
    })
    try {
      toggle.result.togglePlanMode()
      toggle.setAgent(claude(CLAUDE_MODE.Plan))
      toggle.show('agent-cline', cline(CLINE_PERMISSION_MODE.AutoApprove))
      toggle.show('agent-claude', claude(CLAUDE_MODE.Plan))

      toggle.result.togglePlanMode()

      expect(toggle.lastRequestedMode()).toBe(CLAUDE_MODE.AcceptEdits)
    }
    finally {
      toggle.dispose()
    }
  })

  it('remembers a mode the agent reached without the toggle', () => {
    const toggle = setupToggle({
      agentId: 'agent-cline',
      agent: cline(CLINE_PERMISSION_MODE.Act),
    })
    try {
      // The settings menu, not Shift+Tab, chose Auto-approve and then Plan.
      toggle.setAgent(cline(CLINE_PERMISSION_MODE.AutoApprove))
      toggle.setAgent(cline(CLINE_PERMISSION_MODE.Plan))
      toggle.result.togglePlanMode()
      expect(toggle.lastRequestedMode()).toBe(CLINE_PERMISSION_MODE.AutoApprove)
    }
    finally {
      toggle.dispose()
    }
  })

  it('sends nothing before the provider is known', () => {
    const toggle = setupToggle({ agentId: 'agent-cline', agent: undefined })
    try {
      toggle.result.togglePlanMode()
      expect(toggle.onSettingChange).not.toHaveBeenCalled()
    }
    finally {
      toggle.dispose()
    }
  })

  it('sends nothing while the settings load', () => {
    const toggle = setupToggle({
      agentId: 'agent-cline',
      agent: cline(CLINE_PERMISSION_MODE.Plan),
      settingsLoading: true,
    })
    try {
      toggle.result.togglePlanMode()
      expect(toggle.onSettingChange).not.toHaveBeenCalled()
    }
    finally {
      toggle.dispose()
    }
  })
})
