import type { TodoItem } from '~/stores/chatTodos'
import { render } from '@solidjs/testing-library'
import { beforeAll, describe, expect, it } from 'vitest'
import { TodoListMessage } from '~/components/chat/todoListMessage'
import * as todoStyles from '~/components/todo/TodoList.css'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { clippedText } from '~/styles/shared.css'
import { classSelector } from '~/test-support/composedClass'

// jsdom does not provide ResizeObserver, which ToolUseLayout observes with.
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

const LONG = 'Investigate why the worker channel drops after the hub restarts and add a reconnect test'

function renderMessage(todos: TodoItem[]) {
  return render(() => (
    <PreferencesProvider>
      <TodoListMessage source={{ toolName: 'TodoWrite', title: '1 task', todos }} />
    </PreferencesProvider>
  ))
}

describe('todoListMessage', () => {
  /**
   * The transcript's tool card stretches to the tile, so it has the room to
   * show a whole to-do. The sidebar section (~208px) does not, which is why the
   * list clips there and must NOT clip here.
   */
  it('renders the to-do list in its wrapping variant', () => {
    const { container } = renderMessage([
      { id: '1', content: LONG, status: 'pending', activeForm: '' },
    ])

    const list = container.querySelector(classSelector(todoStyles.todoList))!
    expect(list.className).toMatch(/todoListWrapping/)
  })

  // The label still routes through ClippedText, so the wrapping variant turns
  // the clip off through CSS rather than by rendering a different element.
  it('keeps the label on the shared clipping element', () => {
    const { container } = renderMessage([
      { id: '1', content: LONG, status: 'pending', activeForm: '' },
    ])

    const label = container.querySelector(classSelector(todoStyles.todoText))!
    expect(label.textContent).toBe(LONG)
    expect(label.className.trim().split(/\s+/)).toContain(clippedText)
  })

  it('falls back to the cleared placeholder for an empty list', () => {
    const { container } = renderMessage([])

    expect(container.querySelector(classSelector(todoStyles.todoList))).toBeNull()
  })
})
