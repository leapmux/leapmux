import type { ControlResponseHandlingProps } from './controlResponseHandling'
import type { AskQuestionState } from './controls/types'
import { createSignal } from 'solid-js'
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
  const [, setHasContent] = createSignal(false)
  const resetEditorHeight = vi.fn()
  const result = useControlResponseHandling(
    props,
    createMinimalAskState(),
    () => undefined,
    setHasContent,
    resetEditorHeight,
  )
  return { result, onSendMessage, resetEditorHeight }
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
