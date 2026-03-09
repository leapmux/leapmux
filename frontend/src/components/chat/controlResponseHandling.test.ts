import type { ControlResponseHandlingProps } from './controlResponseHandling'
import type { AskQuestionState } from './controls/types'
import type { ControlRequest } from '~/stores/control.store'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
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

function makeControlRequest(requestId: string, agentId: string): ControlRequest {
  return { requestId, agentId, payload: { tool_name: 'Bash', tool_input: {} } }
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
    expect(onSendMessage).toHaveBeenCalledWith(content)
    expect(resetEditorHeight).toHaveBeenCalled()
  })
})
