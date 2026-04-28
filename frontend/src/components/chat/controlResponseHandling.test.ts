import type { FileAttachment } from './attachments'
import type { ControlResponseHandlingProps } from './controlResponseHandling'
import type { AskQuestionState } from './controls/types'
import type { ControlRequest } from '~/stores/control.store'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { useControlResponseHandling } from './controlResponseHandling'

function createMinimalAskState(): AskQuestionState {
  const [selections, setSelections] = createSignal<Record<number, string[]>>({})
  const [customTexts, setCustomTexts] = createSignal<Record<number, string>>({})
  const [currentPage, setCurrentPage] = createSignal(0)
  return { selections, setSelections, customTexts, setCustomTexts, currentPage, setCurrentPage }
}

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
    createMinimalAskState(),
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
    createMinimalAskState(),
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
  it('returns false for empty string', () => {
    const { result, onSendMessage } = setup()
    expect(result.handleSend('')).toBe(false)
    expect(onSendMessage).not.toHaveBeenCalled()
  })

  it('returns false for whitespace-only string', () => {
    const { result, onSendMessage } = setup()
    expect(result.handleSend('   ')).toBe(false)
    expect(onSendMessage).not.toHaveBeenCalled()
  })

  it('does not reset hasContent when activeRequestId changes due to tab switch', () =>
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
            createMinimalAskState(),
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
          // Let the activeRequestId effect run.
          await Promise.resolve()

          // Simulate switching back to tab A (control request reappears).
          setControlRequests([reqA])
          // Let the activeRequestId effect run.
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

  it('passes attachments when present', () => {
    const attachments = [makeAttachment()]
    const { result, onSendMessage } = setupWithAttachments(attachments)
    result.handleSend('look at this')
    expect(onSendMessage).toHaveBeenCalledWith('look at this', attachments)
  })

  it('passes undefined attachments when array is empty', () => {
    const { result, onSendMessage } = setupWithAttachments([])
    result.handleSend('hello')
    expect(onSendMessage).toHaveBeenCalledWith('hello', undefined)
  })

  it('allows sending with empty text when attachments present', () => {
    const attachments = [makeAttachment()]
    const { result, onSendMessage } = setupWithAttachments(attachments)
    const returned = result.handleSend('')
    // Should NOT return false — the send should proceed
    expect(returned).not.toBe(false)
    expect(onSendMessage).toHaveBeenCalledWith('', attachments)
  })

  it('blocks sending with empty text and no attachments', () => {
    const { result, onSendMessage } = setupWithAttachments([])
    expect(result.handleSend('')).toBe(false)
    expect(onSendMessage).not.toHaveBeenCalled()
  })
})

describe('handleControlSend', () => {
  it('uses Claude AskUserQuestion response format keyed by question text', () => {
    createRoot((dispose) => {
      const onControlResponse = vi.fn().mockResolvedValue(undefined)
      const askState = createMinimalAskState()
      askState.setSelections({ 0: ['Build'] })
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
      const result = useControlResponseHandling(props, askState, () => undefined, vi.fn())

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

  it('does not pass attachments to control responses', () => {
    const onControlResponse = vi.fn().mockResolvedValue(undefined)
    const attachments = [makeAttachment()]
    const onSendMessage = vi.fn()
    const props: ControlResponseHandlingProps = {
      agentId: 'test-agent',
      controlRequests: [makeControlRequest('req-1', 'test-agent')],
      onControlResponse,
      onSendMessage,
    }
    const resetEditorHeight = vi.fn()
    const result = useControlResponseHandling(
      props,
      createMinimalAskState(),
      () => undefined,
      resetEditorHeight,
      () => attachments,
    )
    // handleControlSend builds a control response — it should NOT include attachments.
    result.handleControlSend('')
    // onSendMessage should NOT have been called (it's a control response, not a user message).
    expect(onSendMessage).not.toHaveBeenCalled()
    // onControlResponse should have been called (the allow response).
    expect(onControlResponse).toHaveBeenCalled()
  })

  it('uses Pi-native extension_ui_response values for select prompts', () => {
    createRoot((dispose) => {
      const onControlResponse = vi.fn().mockResolvedValue(undefined)
      const askState = createMinimalAskState()
      askState.setSelections({ 0: ['Block'] })
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
      const result = useControlResponseHandling(props, askState, () => undefined, vi.fn())

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

  it('uses Codex-native request_user_input responses', () => {
    createRoot((dispose) => {
      const onControlResponse = vi.fn().mockResolvedValue(undefined)
      const askState = createMinimalAskState()
      askState.setSelections({ 0: ['Build'] })
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
        askState,
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

  it('advances Codex multi-question requests instead of submitting incomplete answers', () => {
    createRoot((dispose) => {
      const onControlResponse = vi.fn().mockResolvedValue(undefined)
      const askState = createMinimalAskState()
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
        askState,
        () => editorContentRef,
        vi.fn(),
      )

      const submitted = result.handleControlSend('Build')

      expect(submitted).toBe(false)
      expect(askState.currentPage()).toBe(1)
      expect(editorContentRef.set).toHaveBeenCalledWith('')
      expect(onControlResponse).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('uses OpenCode-native question responses', () => {
    createRoot((dispose) => {
      const onControlResponse = vi.fn().mockResolvedValue(undefined)
      const askState = createMinimalAskState()
      askState.setSelections({ 0: ['Build'] })
      askState.setCustomTexts({ 1: 'Dev' })
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
      const result = useControlResponseHandling(props, askState, () => undefined, vi.fn())

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

  it('advances OpenCode multi-question requests instead of submitting incomplete answers', () => {
    createRoot((dispose) => {
      const onControlResponse = vi.fn().mockResolvedValue(undefined)
      const askState = createMinimalAskState()
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
      const result = useControlResponseHandling(props, askState, () => editorContentRef, vi.fn())

      const submitted = result.handleControlSend('Build')

      expect(submitted).toBe(false)
      expect(askState.currentPage()).toBe(1)
      expect(editorContentRef.set).toHaveBeenCalledWith('')
      expect(onControlResponse).not.toHaveBeenCalled()
      dispose()
    })
  })
})
