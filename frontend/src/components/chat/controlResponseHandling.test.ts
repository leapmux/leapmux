import type { FileAttachment } from './attachments'
import type { ControlResponseHandlingProps } from './controlResponseHandling'
import type { AsyncLocalKey } from '~/lib/browserStorage'
import type { ControlRequest } from '~/stores/control.store'
import { batch, createRenderEffect, createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { showWarnToast } from '~/components/common/Toast'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
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

    batch(() => result.handleControlSend(''))

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
      const [, bytes] = onControlResponse.mock.calls[0]
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
      expect(onControlResponse.mock.calls[0][0].claimToken).toBe('instance-token-7')
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

        const [, bytes] = onControlResponse.mock.calls[0]
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
      const [, bytes] = onControlResponse.mock.calls[0]
      const parsed = JSON.parse(new TextDecoder().decode(bytes as Uint8Array))
      expect(parsed).toMatchObject({
        type: 'extension_ui_response',
        id: 'req-1',
        value: 'Block',
      })
      dispose()
    })
  })

  it('uses Codex-native request_user_input responses', async () => {
    createRoot((dispose) => {
      const onControlResponse = vi.fn().mockResolvedValue(undefined)
      const answerState = createControlAnswerState()
      answerState.setSelections({ 0: ['Build'] })
      const props: ControlResponseHandlingProps = {
        agentId: 'test-agent',
        agent: { agentProvider: AgentProvider.CODEX },
        controlRequests: [makeControlRequest('7', 'test-agent', {
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
      const [, bytes] = onControlResponse.mock.calls[0]
      const parsed = JSON.parse(new TextDecoder().decode(bytes as Uint8Array))
      expect(parsed).toMatchObject({
        jsonrpc: '2.0',
        id: 7,
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
      const [, bytes] = onControlResponse.mock.calls[0]
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
 * The Interrupt button is offered only when the click can actually land. A
 * subagent tab whose provider cannot interrupt one subagent gets no button:
 * the worker routes a child interrupt through the same ChildSteerer as a child
 * message, so the request would come back FailedPrecondition.
 */
describe('showInterrupt', () => {
  it('shows the button while the agent works and nothing is being asked', async () => {
    createRoot((dispose) => {
      const { result } = setup({ agentWorking: true })
      expect(result.showInterrupt()).toBe(true)
      dispose()
    })
  })

  it('hides the button when the agent is idle', async () => {
    createRoot((dispose) => {
      const { result } = setup({ agentWorking: false })
      expect(result.showInterrupt()).toBe(false)
      dispose()
    })
  })

  it('hides the button while a control request is pending', async () => {
    createRoot((dispose) => {
      const request = { requestId: 'r1', payload: {}, agentId: 'test-agent' } as unknown as ControlRequest
      const { result } = setup({ agentWorking: true, controlRequests: [request] })
      expect(result.showInterrupt()).toBe(false)
      dispose()
    })
  })

  it('hides the button when this agent cannot be interrupted on its own', async () => {
    createRoot((dispose) => {
      const { result } = setup({ agentWorking: true, canInterrupt: false })
      expect(result.showInterrupt()).toBe(false)
      dispose()
    })
  })

  it('treats an unset capability as interruptible (the root-agent default)', async () => {
    createRoot((dispose) => {
      const { result } = setup({ agentWorking: true, canInterrupt: undefined })
      expect(result.showInterrupt()).toBe(true)
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
