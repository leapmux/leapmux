import type { FileAttachment } from './attachments'
import type { ControlResponseHandlingProps } from './controlResponseHandling'
import type { AsyncLocalKey } from '~/lib/browserStorage'
import type { ControlRequest } from '~/stores/control.store'
import { batch, createRenderEffect, createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { showWarnToast } from '~/components/common/Toast'
import { AgentActivityState, AgentProvider, ControlResponseState } from '~/generated/proto/leapmux/v1/agent_pb'
import { flushStorageWrites, localStorageLoad, localStorageStore, PREFIX_CONTROL_STATE } from '~/lib/browserStorage'
import { useTestStorage } from '~/test-support/persistentStorage'
import { useControlResponseHandling } from './controlResponseHandling'
import { createControlAnswerState } from './controls/types'

// The asynchronous storage tier has no in-memory mirror, so these round-trips
// need a database to round-trip through.
useTestStorage()

// The no-plugin bail surfaces a toast; mock the module so it doesn't reach the
// runtime `window.ot` global (absent in jsdom) and we can assert it fired.
vi.mock('~/components/common/Toast', () => ({
  showWarnToast: vi.fn(),
  showInfoToast: vi.fn(),
  showErrorToast: vi.fn(),
}))

function setup(overrides?: Partial<ControlResponseHandlingProps>) {
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
  )
  return { result, onSendMessage, resetEditorHeight }
}

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

function makeControlRequest(requestId: string, agentId: string, payload: Record<string, unknown> = { tool_name: 'Bash', tool_input: {} }): ControlRequest {
  return { requestId, agentId, payload }
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
    const attachments = [makeAttachment()]
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
    const attachments = [makeAttachment()]
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
  function askRequest(requestId: string, claimToken: string): ControlRequest {
    return {
      requestId,
      agentId: 'test-agent',
      claimToken,
      payload: {
        request: {
          tool_name: 'AskUserQuestion',
          input: { questions: [{ header: 'Task', question: 'Pick a task', options: [{ label: 'Build' }, { label: 'Test' }] }] },
        },
      },
    }
  }

  function answerKey(request: ControlRequest): AsyncLocalKey {
    return `${PREFIX_CONTROL_STATE}test-agent:${request.requestId}:${request.claimToken}`
  }

  it('fills the state in from the saved answers of the active request', async () => {
    const request = askRequest('ask-1', 'tok-1')
    localStorageStore(answerKey(request), { selections: { 0: ['Test'] }, currentPage: 0, customTexts: {}, switches: {} })
    await flushStorageWrites()

    const answerState = createControlAnswerState()
    const dispose = createRoot((disposeRoot) => {
      useControlResponseHandling(
        { agentId: 'test-agent', controlRequests: [request], onSendMessage: vi.fn() },
        answerState,
        () => undefined,
        vi.fn(),
      )
      return disposeRoot
    })

    await vi.waitFor(() => expect(answerState.selections()).toEqual({ 0: ['Test'] }))
    dispose()
  })

  it('restores a pill group choice, and a choices-only record still lands', async () => {
    // A record carrying nothing but a pill selection is not blank: the restore
    // must land it, or a reload would silently reset the user's preset pick.
    const request = askRequest('ask-3', 'tok-3')
    localStorageStore(answerKey(request), { choices: { 'control-permissions-pill': 'bypass' } })
    await flushStorageWrites()

    const answerState = createControlAnswerState()
    const dispose = createRoot((disposeRoot) => {
      useControlResponseHandling(
        { agentId: 'test-agent', controlRequests: [request], onSendMessage: vi.fn() },
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
    localStorageStore(answerKey(first), { selections: { 0: ['Build'] }, currentPage: 0, customTexts: {}, switches: {} })
    localStorageStore(answerKey(second), { selections: { 0: ['Test'] }, currentPage: 0, customTexts: {}, switches: {} })
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

    await vi.waitFor(() => expect(answerState.selections()).toEqual({ 0: ['Test'] }))
    // And it STAYS. The superseded read resolving afterwards must change
    // nothing at all.
    await flushStorageWrites()
    await new Promise(resolve => setTimeout(resolve, 0))
    expect(answerState.selections()).toEqual({ 0: ['Test'] })

    // The outgoing request's answers are still on disk: a swap is not an
    // answer, so nothing may overwrite them with the blank state the restore
    // installs on its way in.
    expect(await localStorageLoad(answerKey(first)))
      .toMatchObject({ selections: { 0: ['Build'] } })
    dispose()
  })
})

describe('handleControlSend', () => {
  it('keeps plan settings with the captured request through the composer handler', async () => {
    const request = makeControlRequest('plan', 'test-agent')
    request.payload = { request: { tool_name: 'ExitPlanMode' } }
    const onControlResponse = vi.fn().mockResolvedValue(true)
    const { result, dispose } = createRoot(dispose => ({
      dispose,
      result: setup({ controlRequests: [request], onControlResponse }).result,
    }))
    try {
      const bytes = new TextEncoder().encode('{"response":{"request_id":"plan","response":{"behavior":"allow"}}}')
      const options = { planApproval: { permissionMode: '', clearContext: false } }
      await result.respondTo(request)(bytes, options)
      expect(onControlResponse).toHaveBeenCalledExactlyOnceWith(request, bytes, options)
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
    const first: ControlRequest = { ...makeControlRequest('request', 'agent-A'), claimToken: 'token', agentProvider: AgentProvider.CLAUDE_CODE }
    const second: ControlRequest = { ...first, agentId: 'agent-B' }
    const firstKey: AsyncLocalKey = `${PREFIX_CONTROL_STATE}agent-A:request:token`
    const secondKey: AsyncLocalKey = `${PREFIX_CONTROL_STATE}agent-B:request:token`
    await localStorageStore(firstKey, { choices: { scope: 'once' } }).durable
    await localStorageStore(secondKey, { choices: { scope: 'session' } }).durable
    const [agentId, setAgentId] = createSignal('agent-A')
    const [requests, setRequests] = createSignal([first])
    const state = createControlAnswerState()
    const reset = vi.fn()
    const { result, dispose } = createRoot(dispose => ({
      dispose,
      result: useControlResponseHandling({
        get agentId() { return agentId() },
        get controlRequests() { return requests() },
        onControlResponse: () => delivery,
        onSendMessage: vi.fn(),
      }, state, () => undefined, reset),
    }))
    try {
      await vi.waitFor(() => expect(state.choices()).toEqual({ scope: 'once' }))
      const sending = result.respondTo(first)(new Uint8Array())
      batch(() => {
        setAgentId('agent-B')
        setRequests([second])
      })
      await vi.waitFor(() => expect(state.choices()).toEqual({ scope: 'session' }))
      finish()
      await sending
      await flushStorageWrites()
      expect(await localStorageLoad(firstKey)).toBeUndefined()
      expect(await localStorageLoad(secondKey)).toMatchObject({ choices: { scope: 'session' } })
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
    const onControlResponse = vi.fn().mockReturnValueOnce(delivery).mockResolvedValue(undefined)
    const request: ControlRequest = { requestId: 'pending', agentId: 'test-agent', agentProvider: AgentProvider.CLAUDE_CODE, claimToken: 'token', payload: { request: { tool_name: 'Bash', input: { command: 'pwd' } } } }
    const key: AsyncLocalKey = `${PREFIX_CONTROL_STATE}test-agent:pending:token`
    const state = createControlAnswerState()
    const reset = vi.fn()
    const { result, dispose } = createRoot(dispose => ({ dispose, result: useControlResponseHandling({ agentId: 'test-agent', controlRequests: [request], onControlResponse, onSendMessage: vi.fn() }, state, () => undefined, reset) }))
    try {
      await vi.waitFor(() => expect(state.ready()).toBe(true))
      state.setChoices({ scope: 'once' })
      await flushStorageWrites()
      const sending = Promise.resolve(result.handleControlSend('Use a safer command')).catch(() => {})
      await flushStorageWrites()
      expect(await localStorageLoad(key)).toMatchObject({ choices: { scope: 'once' } })
      expect(reset).not.toHaveBeenCalled()
      rejectDelivery(new Error('offline'))
      await sending
      state.setChoices({ scope: 'session' })
      await flushStorageWrites()
      expect(await localStorageLoad(key)).toMatchObject({ choices: { scope: 'session' } })
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
    const answerState = createControlAnswerState({ selections: { 0: ['Build'] } })
    const onControlResponse = vi.fn().mockResolvedValue(undefined)
    const key = `${PREFIX_CONTROL_STATE}test-agent:ask-1:tok-1`
    const props: ControlResponseHandlingProps = {
      agentId: 'test-agent',
      agent: { agentProvider: AgentProvider.CLAUDE_CODE },
      controlRequests: [{
        requestId: 'ask-1',
        agentId: 'test-agent',
        claimToken: 'tok-1',
        payload: {
          request: {
            tool_name: 'AskUserQuestion',
            input: { questions: [{ header: 'Task', question: 'Pick a task', options: [{ label: 'Build' }] }] },
          },
        },
      }],
      onControlResponse,
      onSendMessage: vi.fn(),
    }
    let result!: ReturnType<typeof useControlResponseHandling>
    const dispose = createRoot((disposeRoot) => {
      result = useControlResponseHandling(props, answerState, () => undefined, vi.fn())
      return disposeRoot
    })

    // OUTSIDE the root, so the restore effect has already claimed the request as
    // the owner of the answers -- which is the state a real session answers in.
    answerState.setSelections({ 0: ['Build'] })
    expect(await localStorageLoad(key)).toBeDefined()
    await vi.waitFor(() => expect(answerState.ready()).toBe(true))

    await batch(() => result.handleControlSend(''))

    expect(onControlResponse).toHaveBeenCalledOnce()
    expect(await localStorageLoad(key)).toBeUndefined()

    dispose()
  })

  it('uses Claude AskUserQuestion response format keyed by question text', async () => {
    createRoot((dispose) => {
      const onControlResponse = vi.fn().mockResolvedValue(undefined)
      const answerState = createControlAnswerState()
      answerState.setSelections({ 0: ['Build'] })
      const props: ControlResponseHandlingProps = {
        agentId: 'test-agent',
        agent: { agentProvider: AgentProvider.CLAUDE_CODE },
        controlRequests: [makeControlRequest('req-1', 'test-agent', {
          request: {
            tool_name: 'AskUserQuestion',
            input: {
              questions: [
                { header: 'Task', question: 'Pick a task', options: [{ label: 'Build' }, { label: 'Test' }] },
              ],
            },
          },
        })],
        onControlResponse,
        onSendMessage: vi.fn(),
      }
      const result = useControlResponseHandling(props, answerState, () => undefined, vi.fn())

      result.handleControlSend('')

      expect(onControlResponse).toHaveBeenCalledOnce()
      const sendCall = onControlResponse.mock.calls[0]
      expect(sendCall).toBeDefined()
      const [, bytes] = sendCall ?? []
      const parsed = JSON.parse(new TextDecoder().decode(bytes as Uint8Array))
      expect(parsed).toMatchObject({
        type: 'control_response',
        response: {
          request_id: 'req-1',
          response: {
            behavior: 'allow',
            updatedInput: {
              answers: {
                'Pick a task': 'Build',
              },
            },
          },
        },
      })
      expect(parsed.response.response.updatedInput.answers).not.toHaveProperty('Task')
      dispose()
    })
  })

  it('threads the active request\'s per-instance claimToken to onControlResponse', async () => {
    createRoot((dispose) => {
      const onControlResponse = vi.fn().mockResolvedValue(undefined)
      const props: ControlResponseHandlingProps = {
        agentId: 'test-agent',
        agent: { agentProvider: AgentProvider.CLAUDE_CODE },
        controlRequests: [{ requestId: 'req-1', agentId: 'test-agent', payload: { request: { tool_name: 'Bash', tool_input: {} } }, claimToken: 'instance-token-7' }],
        onControlResponse,
        onSendMessage: vi.fn(),
      }
      const result = useControlResponseHandling(props, createControlAnswerState(), () => undefined, vi.fn())

      result.handleControlSend('please stop')

      // The answer carries the whole request, so its claim token is the answered
      // instance's own. The worker's idempotency claim then keys on THIS instance,
      // and no store re-derivation can pair it with a sibling that reuses the id.
      expect(onControlResponse).toHaveBeenCalledOnce()
      // Called once above; `?.` is the type-level guard alone.
      expect(onControlResponse.mock.calls[0]?.[0].claimToken).toBe('instance-token-7')
      dispose()
    })
  })

  it('sends Codex approval feedback after the native cancel response', async () => {
    await createRoot(async (dispose) => {
      try {
        let finishResponse!: () => void
        const onControlResponse = vi.fn().mockReturnValue(new Promise<void>((resolve) => {
          finishResponse = resolve
        }))
        const onSendMessage = vi.fn()
        const onSendControlFeedback = vi.fn()
        const props: ControlResponseHandlingProps = {
          agentId: 'test-agent',
          agent: { agentProvider: AgentProvider.CODEX },
          controlRequests: [makeControlRequest('7', 'test-agent', {
            method: 'item/commandExecution/requestApproval',
            params: { availableDecisions: ['accept', 'cancel'] },
          })],
          onControlResponse,
          onSendMessage,
          onSendControlFeedback,
        }
        const result = useControlResponseHandling(props, createControlAnswerState(), () => undefined, vi.fn())

        result.handleControlSend('Use a safer command')

        const sendCall = onControlResponse.mock.calls[0]
        expect(sendCall).toBeDefined()
        const [, bytes] = sendCall ?? []
        expect(JSON.parse(new TextDecoder().decode(bytes))).toMatchObject({ result: { decision: 'cancel' } })
        expect(onSendMessage).not.toHaveBeenCalled()
        finishResponse()
        await vi.waitFor(() => expect(onSendControlFeedback).toHaveBeenCalledWith('Use a safer command'))
        expect(onSendMessage).not.toHaveBeenCalled()
      }
      finally {
        dispose()
      }
    })
  })

  it('sends Pi plan feedback only after the native stay decision', async () => {
    await createRoot(async (dispose) => {
      try {
        let finishResponse!: () => void
        const onControlResponse = vi.fn().mockReturnValue(new Promise<void>((resolve) => {
          finishResponse = resolve
        }))
        const onSendControlFeedback = vi.fn()
        const { result } = setup({
          agent: { agentProvider: AgentProvider.PI },
          controlRequests: [makeControlRequest('plan', 'test-agent', {
            type: 'extension_ui_request',
            method: 'select',
            title: 'Proposed plan ready. What next?',
            options: ['Implement here', 'Start fresh and implement', 'Stay in Plan mode'],
          })],
          onControlResponse,
          onSendControlFeedback,
        })
        result.handleControlSend('Revise the second step.')
        const sendCall = onControlResponse.mock.calls[0]
        expect(sendCall).toBeDefined()
        const [, bytes] = sendCall ?? []
        expect(JSON.parse(new TextDecoder().decode(bytes))).toEqual({ type: 'extension_ui_response', id: 'plan', value: 'Stay in Plan mode' })
        expect(onSendControlFeedback).not.toHaveBeenCalled()
        finishResponse()
        await vi.waitFor(() => expect(onSendControlFeedback).toHaveBeenCalledWith('Revise the second step.'))
      }
      finally {
        dispose()
      }
    })
  })

  it('does not duplicate Codex plan feedback that the worker forwards', async () => {
    await createRoot(async (dispose) => {
      const onControlResponse = vi.fn().mockResolvedValue(undefined)
      const onSendMessage = vi.fn()
      const props: ControlResponseHandlingProps = {
        agentId: 'test-agent',
        agent: { agentProvider: AgentProvider.CODEX },
        controlRequests: [makeControlRequest('plan-7', 'test-agent', {
          request: { tool_name: 'CodexPlanModePrompt', input: {} },
        })],
        onControlResponse,
        onSendMessage,
      }
      const result = useControlResponseHandling(props, createControlAnswerState(), () => undefined, vi.fn())

      result.handleControlSend('Revise the plan')

      await vi.waitFor(() => expect(onControlResponse).toHaveBeenCalledOnce())
      expect(onSendMessage).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('does not pass attachments to control responses', async () => {
    const onControlResponse = vi.fn().mockResolvedValue(undefined)
    const attachments = [makeAttachment()]
    const onSendMessage = vi.fn()
    const props: ControlResponseHandlingProps = {
      agentId: 'test-agent',
      agent: { agentProvider: AgentProvider.CLAUDE_CODE },
      controlRequests: [makeControlRequest('req-1', 'test-agent')],
      onControlResponse,
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
    // onControlResponse should have been called (the allow response).
    expect(onControlResponse).toHaveBeenCalled()
  })

  it('refuses to send a control response when the agent provider has no plugin', async () => {
    // No agent provider -> no plugin. We removed the Claude fallback, so rather
    // than encoding the response through the wrong provider's builder we bail
    // (returning false to keep the editor content), surface a toast so the send
    // is not a silent no-op, and send nothing.
    vi.mocked(showWarnToast).mockClear()
    const onControlResponse = vi.fn().mockResolvedValue(undefined)
    const props: ControlResponseHandlingProps = {
      agentId: 'test-agent',
      controlRequests: [makeControlRequest('req-1', 'test-agent')],
      onControlResponse,
      onSendMessage: vi.fn(),
    }
    const answerState = createControlAnswerState()
    const result = useControlResponseHandling(props, answerState, () => undefined, vi.fn())
    await vi.waitFor(() => expect(answerState.ready()).toBe(true))
    expect(result.handleControlSend('')).toBe(false)
    expect(onControlResponse).not.toHaveBeenCalled()
    expect(showWarnToast).toHaveBeenCalledWith(expect.stringContaining('unsupported agent provider'))
  })

  it('uses Pi-native extension_ui_response values for select prompts', async () => {
    createRoot((dispose) => {
      const onControlResponse = vi.fn().mockResolvedValue(undefined)
      const answerState = createControlAnswerState()
      answerState.setSelections({ 0: ['Block'] })
      const props: ControlResponseHandlingProps = {
        agentId: 'test-agent',
        agent: { agentProvider: AgentProvider.PI },
        controlRequests: [makeControlRequest('req-1', 'test-agent', {
          type: 'extension_ui_request',
          id: 'req-1',
          method: 'select',
          title: 'Allow dangerous command?',
          options: ['Allow', 'Block'],
        })],
        onControlResponse,
        onSendMessage: vi.fn(),
      }
      const result = useControlResponseHandling(props, answerState, () => undefined, vi.fn())

      result.handleControlSend('')

      expect(onControlResponse).toHaveBeenCalledOnce()
      const sendCall = onControlResponse.mock.calls[0]
      expect(sendCall).toBeDefined()
      const [, bytes] = sendCall ?? []
      const parsed = JSON.parse(new TextDecoder().decode(bytes as Uint8Array))
      expect(parsed).toMatchObject({
        type: 'extension_ui_response',
        id: 'req-1',
        value: 'Block',
      })
      dispose()
    })
  })

  it.each(['7', '007', '9007199254740993'])('preserves worker control ID %s in Codex question responses', (requestId) => {
    createRoot((dispose) => {
      const onControlResponse = vi.fn().mockResolvedValue(undefined)
      const answerState = createControlAnswerState()
      answerState.setSelections({ 0: ['Build'] })
      const props: ControlResponseHandlingProps = {
        agentId: 'test-agent',
        agent: { agentProvider: AgentProvider.CODEX },
        controlRequests: [makeControlRequest(requestId, 'test-agent', {
          method: 'item/tool/requestUserInput',
          params: {
            questions: [
              { id: 'q1', header: 'Action', question: 'What next?', options: [{ label: 'Build' }] },
            ],
          },
        })],
        onControlResponse,
        onSendMessage: vi.fn(),
      }
      const result = useControlResponseHandling(
        props,
        answerState,
        () => undefined,
        vi.fn(),
      )

      result.handleControlSend('')

      expect(onControlResponse).toHaveBeenCalledOnce()
      const sendCall = onControlResponse.mock.calls[0]
      expect(sendCall).toBeDefined()
      const [, bytes] = sendCall ?? []
      const parsed = JSON.parse(new TextDecoder().decode(bytes as Uint8Array))
      expect(parsed).toMatchObject({
        jsonrpc: '2.0',
        id: requestId,
        result: {
          answers: {
            q1: { answers: ['Build'] },
          },
        },
      })
      dispose()
    })
  })

  it('advances Codex multi-question requests instead of submitting incomplete answers', async () => {
    createRoot((dispose) => {
      const onControlResponse = vi.fn().mockResolvedValue(undefined)
      const answerState = createControlAnswerState()
      const editorContentRef = {
        get: () => 'Build',
        set: vi.fn(),
      }
      const props: ControlResponseHandlingProps = {
        agentId: 'test-agent',
        agent: { agentProvider: AgentProvider.CODEX },
        controlRequests: [makeControlRequest('7', 'test-agent', {
          method: 'item/tool/requestUserInput',
          params: {
            questions: [
              { id: 'q1', header: 'Action', question: 'What next?', options: [{ label: 'Build' }] },
              { id: 'q2', header: 'Env', question: 'Where?', options: [{ label: 'Dev' }] },
            ],
          },
        })],
        onControlResponse,
        onSendMessage: vi.fn(),
      }
      const result = useControlResponseHandling(
        props,
        answerState,
        () => editorContentRef,
        vi.fn(),
      )

      const submitted = result.handleControlSend('Build')

      expect(submitted).toBe(false)
      expect(answerState.currentPage()).toBe(1)
      expect(editorContentRef.set).toHaveBeenCalledWith('')
      expect(onControlResponse).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('uses OpenCode-native question responses', async () => {
    createRoot((dispose) => {
      const onControlResponse = vi.fn().mockResolvedValue(undefined)
      const answerState = createControlAnswerState()
      answerState.setSelections({ 0: ['Build'] })
      answerState.setCustomTexts({ 1: 'Dev' })
      const props: ControlResponseHandlingProps = {
        agentId: 'test-agent',
        agent: { agentProvider: AgentProvider.OPENCODE },
        controlRequests: [makeControlRequest('que-1', 'test-agent', {
          type: 'question.asked',
          properties: {
            questions: [
              { header: 'Task', question: 'Pick a task', options: [{ label: 'Build' }] },
              { header: 'Env', question: 'Pick an env', options: [{ label: 'Dev' }], custom: true },
            ],
          },
        })],
        onControlResponse,
        onSendMessage: vi.fn(),
      }
      const result = useControlResponseHandling(props, answerState, () => undefined, vi.fn())

      result.handleControlSend('')

      expect(onControlResponse).toHaveBeenCalledOnce()
      const sendCall = onControlResponse.mock.calls[0]
      expect(sendCall).toBeDefined()
      const [, bytes] = sendCall ?? []
      const parsed = JSON.parse(new TextDecoder().decode(bytes as Uint8Array))
      expect(parsed).toMatchObject({
        jsonrpc: '2.0',
        id: 'que-1',
        result: {
          answers: [['Build'], ['Dev']],
        },
      })
      dispose()
    })
  })

  it('advances OpenCode multi-question requests instead of submitting incomplete answers', async () => {
    createRoot((dispose) => {
      const onControlResponse = vi.fn().mockResolvedValue(undefined)
      const answerState = createControlAnswerState()
      const editorContentRef = {
        get: () => 'Build',
        set: vi.fn(),
      }
      const props: ControlResponseHandlingProps = {
        agentId: 'test-agent',
        agent: { agentProvider: AgentProvider.OPENCODE },
        controlRequests: [makeControlRequest('que-1', 'test-agent', {
          type: 'question.asked',
          properties: {
            questions: [
              { header: 'Task', question: 'Pick a task', options: [{ label: 'Build' }] },
              { header: 'Env', question: 'Pick an env', options: [{ label: 'Dev' }] },
            ],
          },
        })],
        onControlResponse,
        onSendMessage: vi.fn(),
      }
      const result = useControlResponseHandling(props, answerState, () => editorContentRef, vi.fn())

      const submitted = result.handleControlSend('Build')

      expect(submitted).toBe(false)
      expect(answerState.currentPage()).toBe(1)
      expect(editorContentRef.set).toHaveBeenCalledWith('')
      expect(onControlResponse).not.toHaveBeenCalled()
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
  const pendingRequest = () =>
    ({ requestId: 'r1', payload: {}, agentId: 'test-agent' } as unknown as ControlRequest)

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
        controlRequests: [pendingRequest()],
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
        controlRequests: [pendingRequest()],
      })
      expect(result.showInterrupt()).toBe(false)
      dispose()
    })
  })

  it('hides the button for a pending request on an agent that cannot be interrupted', async () => {
    createRoot((dispose) => {
      const { result } = setup({
        agentActivity: AgentActivityState.WAITING_FOR_USER,
        controlRequests: [pendingRequest()],
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
// cannot classify the same request differently, and one graph answers all three
// readers. See `createControlSurface`.
describe('activeControlSurface and activeControlProvider', () => {
  const question = {
    request: {
      tool_name: 'AskUserQuestion',
      input: { questions: [{ header: 'Task', question: 'Pick a task', options: [{ label: 'Build' }] }] },
    },
  }

  it('classifies the active request and reports its provider', () => {
    createRoot((dispose) => {
      const { result } = setup({
        agent: { agentProvider: AgentProvider.CLAUDE_CODE },
        controlRequests: [makeControlRequest('req-1', 'test-agent', question)],
      })
      expect(result.activeControlProvider()).toBe(AgentProvider.CLAUDE_CODE)
      expect(result.activeControlSurface()?.kind).toBe('question')
      expect(result.isAskUserQuestion()).toBe(true)
      dispose()
    })
  })

  it('reads a payload no shared form answers as a permission', () => {
    createRoot((dispose) => {
      const { result } = setup({
        agent: { agentProvider: AgentProvider.CLAUDE_CODE },
        controlRequests: [makeControlRequest('req-1', 'test-agent')],
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
      const { result } = setup({ agent: { agentProvider: AgentProvider.CLAUDE_CODE }, controlRequests: [] })
      expect(result.activeControlSurface()).toBeUndefined()
      dispose()
    })
  })

  // ONE graph: both halves of the banner and the composer read the same memo,
  // so a read from three places recomputes nothing.
  it('classifies once for every reader of the same request', () => {
    createRoot((dispose) => {
      const { result } = setup({
        agent: { agentProvider: AgentProvider.CLAUDE_CODE },
        controlRequests: [makeControlRequest('req-1', 'test-agent', question)],
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

    setControlRequests([head, makeControlRequest('req-queued', 'agent-A')])
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
    const first = makeControlRequest('req-1', 'agent-A', { tool_name: 'AskUserQuestion', tool_input: {} })
    first.claimToken = 'claim-1'
    const second = makeControlRequest('req-1', 'agent-A', { tool_name: 'AskUserQuestion', tool_input: {} })
    second.claimToken = 'claim-2'
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

    answerState.setSelections({ 0: ['Postgres'] })
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
})

describe('a request whose payload LeapMux cannot read', () => {
  // The banner offers no decision for a faulted request, so the composer must not offer
  // to send one either: a rejection reason still builds a DENY, and composing one needs
  // the option list or decision vocabulary the unreadable payload was carrying. The stop
  // is the way out. See RL-002.
  it('offers no editor, and sends nothing when one is driven anyway', async () => {
    const onControlResponse = vi.fn().mockResolvedValue(undefined)
    const request: ControlRequest = {
      agentId: 'test-agent',
      requestId: 'faulted',
      payload: {},
      payloadFault: 'malformed',
      originalPayload: new TextEncoder().encode('{not json'),
    }
    const state = createControlAnswerState()
    const { handling, dispose } = createRoot(dispose => ({ dispose, handling: useControlResponseHandling({ agentId: 'test-agent', agent: { agentProvider: AgentProvider.GOOSE }, controlRequests: [request], onControlResponse, onSendMessage: vi.fn() }, state, () => undefined, vi.fn()) }))
    try {
      await vi.waitFor(() => expect(state.ready()).toBe(true))
      expect(handling.editorPurpose()).toBe('none')
      expect(handling.editorPlaceholder()).toBeUndefined()
      expect(handling.handleControlSend('why did this fail')).toBe(false)
      expect(onControlResponse).not.toHaveBeenCalled()
    }
    finally {
      dispose()
    }
  })

  // The turn is still blocked, and a payload nobody can read leaves the stop as
  // the ONLY way out -- there is no question to answer. The Worker says the turn
  // is blocked the same way it does for a readable prompt, so the button reads
  // its level and the fault changes nothing here.
  it('still offers the stop', async () => {
    const request: ControlRequest = { agentId: 'test-agent', requestId: 'faulted', payload: {}, payloadFault: 'malformed' }
    const state = createControlAnswerState()
    const { handling, dispose } = createRoot(dispose => ({ dispose, handling: useControlResponseHandling({ agentId: 'test-agent', agent: { agentProvider: AgentProvider.GOOSE }, controlRequests: [request], agentActivity: AgentActivityState.WAITING_FOR_USER, onControlResponse: vi.fn(), onSendMessage: vi.fn() }, state, () => undefined, vi.fn()) }))
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
    const onControlResponse = vi.fn().mockResolvedValue(undefined)
    const request: ControlRequest = { agentId: 'test-agent', requestId: 'form-submit', claimToken: 'form-token', payload: {
      method: 'elicitation/create',
      params: { mode: 'form', requestedSchema: { type: 'object', required: ['count'], properties: { count: { type: 'integer' } } } },
    } }
    const state = createControlAnswerState()
    const { handling, dispose } = createRoot(dispose => ({ dispose, handling: useControlResponseHandling({ agentId: 'test-agent', agent: { agentProvider: AgentProvider.GOOSE }, controlRequests: [request], onControlResponse, onSendMessage: vi.fn() }, state, () => undefined, vi.fn()) }))
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
  const denyRequest = (): ControlRequest => ({
    requestId: 'open',
    agentId: 'test-agent',
    agentProvider: AgentProvider.CLAUDE_CODE,
    payload: { request: { tool_name: 'Bash', input: { command: 'pwd' } } },
  })

  async function sendWith(completed: boolean | undefined) {
    const request = denyRequest()
    const onControlResponse = vi.fn().mockResolvedValue(completed)
    const state = createControlAnswerState()
    const { result, dispose } = createRoot(dispose => ({
      dispose,
      result: useControlResponseHandling(
        { agentId: 'test-agent', controlRequests: [request], onControlResponse, onSendMessage: vi.fn() },
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
  const faulted = (): ControlRequest => ({
    requestId: 'faulted',
    agentId: 'test-agent',
    agentProvider: AgentProvider.CLAUDE_CODE,
    payload: {},
    payloadFault: 'malformed',
    responseState: ControlResponseState.READY,
  })

  it('refuses an answer and still admits the recording', async () => {
    const request = faulted()
    const onControlResponse = vi.fn().mockResolvedValue(true)
    const { result, dispose } = createRoot(dispose => ({
      dispose,
      result: setup({ controlRequests: [request], onControlResponse }).result,
    }))
    try {
      await expect(result.respondTo(request)(new TextEncoder().encode('{}'))).rejects.toThrow()
      expect(onControlResponse).not.toHaveBeenCalled()
      await result.recordResponse(request)
      expect(onControlResponse).toHaveBeenCalledOnce()
      // Called once above; `?.` is the type-level guard alone.
      expect(onControlResponse.mock.calls[0]?.[2]).toMatchObject({ recordOnly: true })
    }
    finally {
      dispose()
    }
  })
})
