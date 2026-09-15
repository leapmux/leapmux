import type { TodoItem } from '~/stores/chatTodos'
import { describe, expect, it } from 'vitest'
import { todoToolBody } from './toolPresentation'

function todo(content: string, status: TodoItem['status']): TodoItem {
  return { rowKey: `${content}`, content, status, activeForm: content }
}

describe('todotoolbody', () => {
  it('states the list size in the header', () => {
    const items = [todo('Read the file', 'completed'), todo('Write the test', 'pending')]
    expect(todoToolBody(items)).toEqual({
      kind: 'todo',
      title: '2 tasks',
      body: { type: 'todo', items },
    })
  })

  it('says a cleared list is cleared rather than showing a count of zero', () => {
    // Goose, Reasonix and Copilot each spelled this branch separately, and one of them
    // drew the raw tool name for an empty list until it gained the branch.
    expect(todoToolBody([]).title).toBe('To-do list cleared')
  })

  it('states the singular for one task', () => {
    expect(todoToolBody([todo('Ship it', 'in_progress')]).title).toBe('1 task')
  })
})
