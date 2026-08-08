/// <reference types="vitest/globals" />
import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import type { TodoItem } from '~/stores/chatTodos'
import { render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { motion } from '~/styles/tokens'
import { ThinkingIndicator } from './ThinkingIndicator'

// The token-count <Show> gates on BOTH `visible` and a positive estimate, so the
// count's presence is asserted against the visibility it is meant to track.
//
// Rendering visible=true normally drives the expand-tick rAF loop, which the
// synchronous test rAF stub would recurse into forever; `renderVisible` stubs
// rAF to a no-op for the render and passes paused=true so the compass sim and
// verb-rotation interval stay idle. Hidden cases render visible=false directly.
function renderVisible(thinkingTokens?: number) {
  const realRaf = globalThis.requestAnimationFrame
  globalThis.requestAnimationFrame = (() => 0) as typeof globalThis.requestAnimationFrame
  try {
    return render(() => (
      <ThinkingIndicator visible={true} paused={true} thinkingTokens={thinkingTokens} />
    ))
  }
  finally {
    globalThis.requestAnimationFrame = realRaf
  }
}

describe('thinking indicator token count', () => {
  it('renders the running thinking-token count when visible and positive', () => {
    const { getByText } = renderVisible(1234)
    expect(getByText('1.23k tokens')).toBeInTheDocument()
  })

  it('renders a sub-1k estimate verbatim, without a k suffix', () => {
    // 230 is the literal value from the original thinking_tokens payload.
    const { getByText } = renderVisible(230)
    expect(getByText('230 tokens')).toBeInTheDocument()
  })

  it('does not render the count while hidden, even with a positive estimate', () => {
    // The estimate's clear is event-driven, so a stale value can briefly outlive
    // the indicator; gating the count on `visible` keeps it from rendering (and
    // running its roll effects) inside a collapsed, invisible row.
    const { getByTestId, queryByText } = render(() => (
      <ThinkingIndicator visible={false} thinkingTokens={1234} />
    ))
    expect((getByTestId('thinking-indicator') as HTMLElement).style.display).toBe('none')
    expect(queryByText(/tokens/)).toBeNull()
  })

  it('renders nothing when the estimate is absent', () => {
    const { queryByText } = renderVisible(undefined)
    expect(queryByText(/tokens/)).toBeNull()
  })

  it('renders nothing when the estimate is zero', () => {
    const { queryByText } = renderVisible(0)
    expect(queryByText(/tokens/)).toBeNull()
  })

  it('keeps the count mounted through the row fade after hiding, then unmounts it', () => {
    vi.useFakeTimers()
    const realRaf = globalThis.requestAnimationFrame
    globalThis.requestAnimationFrame = (() => 0) as typeof globalThis.requestAnimationFrame
    try {
      const [visible, setVisible] = createSignal(true)
      const { queryByText } = render(() => (
        <ThinkingIndicator visible={visible()} paused={true} thinkingTokens={500} />
      ))
      expect(queryByText('500 tokens')).toBeInTheDocument()

      // The indicator hides (turn end). The count must NOT pop — it stays
      // mounted (frozen on its last value) to fade out with the collapsing row.
      setVisible(false)
      expect(queryByText('500 tokens')).toBeInTheDocument()

      // Once the wrapper's opacity fade (ROW_FADE_MS) elapses, it unmounts.
      vi.advanceTimersByTime(motion.medium)
      expect(queryByText('500 tokens')).toBeNull()
    }
    finally {
      globalThis.requestAnimationFrame = realRaf
      vi.useRealTimers()
    }
  })

  it('removes the collapsed wrapper from flex layout after the hide transition', () => {
    vi.useFakeTimers()
    const realRaf = globalThis.requestAnimationFrame
    globalThis.requestAnimationFrame = (() => 0) as typeof globalThis.requestAnimationFrame
    try {
      const [visible, setVisible] = createSignal(true)
      const { getByTestId } = render(() => (
        <ThinkingIndicator visible={visible()} paused={true} />
      ))
      const indicator = getByTestId('thinking-indicator') as HTMLElement
      expect(indicator.style.display).toBe('grid')

      setVisible(false)
      expect(indicator.style.display).toBe('grid')

      vi.advanceTimersByTime(motion.medium * 2)
      expect(indicator.style.display).toBe('none')
    }
    finally {
      globalThis.requestAnimationFrame = realRaf
      vi.useRealTimers()
    }
  })

  it('keeps the wrapper in layout when shown again before hide cleanup fires', () => {
    vi.useFakeTimers()
    const realRaf = globalThis.requestAnimationFrame
    globalThis.requestAnimationFrame = (() => 0) as typeof globalThis.requestAnimationFrame
    try {
      const [visible, setVisible] = createSignal(true)
      const { getByTestId } = render(() => (
        <ThinkingIndicator visible={visible()} paused={true} />
      ))
      const indicator = getByTestId('thinking-indicator') as HTMLElement

      setVisible(false)
      vi.advanceTimersByTime(motion.medium)
      setVisible(true)
      vi.advanceTimersByTime(motion.medium * 2)

      expect(indicator.style.display).toBe('grid')
    }
    finally {
      globalThis.requestAnimationFrame = realRaf
      vi.useRealTimers()
    }
  })
})

describe('thinking indicator chips', () => {
  const realRaf = globalThis.requestAnimationFrame

  function renderChips(props: {
    backgroundTaskCount?: number
    backgroundTasks?: BackgroundTaskItem[]
    todos?: TodoItem[]
  }) {
    globalThis.requestAnimationFrame = (() => 0) as typeof globalThis.requestAnimationFrame
    try {
      return render(() => (
        <ThinkingIndicator visible={true} paused={true} {...props} />
      ))
    }
    finally {
      globalThis.requestAnimationFrame = realRaf
    }
  }

  it('renders the bg-tasks chip when backgroundTaskCount > 0', () => {
    const { getByTestId, queryByTestId } = renderChips({ backgroundTaskCount: 2 })
    expect(getByTestId('thinking-bg-tasks-chip')).toBeInTheDocument()
    expect(getByTestId('thinking-bg-tasks-chip')).toHaveTextContent('2')
    expect(queryByTestId('bg-tasks-popover')).toBeInTheDocument()
  })

  it('hides the bg-tasks chip when count is zero', () => {
    const { queryByTestId } = renderChips({ backgroundTaskCount: 0 })
    expect(queryByTestId('thinking-bg-tasks-chip')).toBeNull()
  })

  it('renders the todos chip with done/total when todos are present', () => {
    const todos: TodoItem[] = [
      { content: 'a', status: 'completed', activeForm: '' },
      { content: 'b', status: 'in_progress', activeForm: 'doing b' },
      { content: 'c', status: 'pending', activeForm: '' },
    ]
    const { getByTestId } = renderChips({ todos })
    expect(getByTestId('thinking-todos-chip')).toHaveTextContent('1/3')
    expect(getByTestId('todo-list-popover')).toBeInTheDocument()
  })

  it('hides the todos chip when all todos are deleted', () => {
    const todos: TodoItem[] = [
      { content: 'a', status: 'deleted', activeForm: '' },
    ]
    const { queryByTestId } = renderChips({ todos })
    expect(queryByTestId('thinking-todos-chip')).toBeNull()
  })

  it('hides the todos chip when the list is empty', () => {
    const { queryByTestId } = renderChips({ todos: [] })
    expect(queryByTestId('thinking-todos-chip')).toBeNull()
  })
})
