import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { TodoItemSchema, TodoStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { createTodoStore } from '~/stores/chatTodoStore'

function protoTodo(id: string, content: string, status = TodoStatus.PENDING) {
  return create(TodoItemSchema, { id, content, status, activeForm: '', description: '' })
}

describe('chatTodoStore', () => {
  it('returns the shared empty list for an agent with no todos', () =>
    createRoot((dispose) => {
      const store = createTodoStore()
      expect(store.get('a1')).toEqual([])
      expect(store.getById('a1', 'whatever')).toBeUndefined()
      dispose()
    }))

  it('replace converts proto items to the store shape, per agent and independent', () =>
    createRoot((dispose) => {
      const store = createTodoStore()
      store.replace('a1', [protoTodo('t1', 'Run tests', TodoStatus.IN_PROGRESS)])
      store.replace('a2', [protoTodo('t2', 'Write docs')])
      expect(store.get('a1')).toEqual([
        { id: 't1', rowKey: 't1', content: 'Run tests', status: 'in_progress', activeForm: '', description: undefined },
      ])
      expect(store.getById('a1', 't1')?.status).toBe('in_progress')
      expect(store.get('a2').map(t => t.id)).toEqual(['t2'])
      expect(store.getById('a1', 't2')).toBeUndefined() // not cross-agent
      dispose()
    }))

  it('replace skips a structurally-identical re-broadcast (same reference, no reactive churn)', () =>
    createRoot((dispose) => {
      const store = createTodoStore()
      store.replace('a1', [protoTodo('t1', 'Run tests')])
      const first = store.get('a1')
      // A no-op patch / KindDetail re-broadcast with identical content: skipped, so the
      // stored array reference is preserved (reactive consumers don't re-run).
      store.replace('a1', [protoTodo('t1', 'Run tests')])
      expect(store.get('a1')).toBe(first)
      // A genuine change RECONCILES: the row keeps its identity, and only the
      // field that changed changes. A whole-array replace handed `<For>` a new
      // object for every row, so one to-do finishing rebuilt the whole list --
      // closing the tooltip under the pointer and restarting the in-progress
      // row's animation.
      const firstRow = first[0]
      store.replace('a1', [protoTodo('t1', 'Run tests', TodoStatus.COMPLETED)])
      expect(store.get('a1')[0]).toBe(firstRow)
      expect(store.getById('a1', 't1')?.status).toBe('completed')
      dispose()
    }))

  it('replace([]) after a populated list goes through (clears it)', () =>
    createRoot((dispose) => {
      const store = createTodoStore()
      store.replace('a1', [protoTodo('t1', 'Run tests')])
      store.replace('a1', [])
      expect(store.get('a1')).toEqual([])
      dispose()
    }))

  it('clear and remove drop the agent list', () =>
    createRoot((dispose) => {
      const store = createTodoStore()
      store.replace('a1', [protoTodo('t1', 'Run tests')])
      store.clear('a1')
      expect(store.get('a1')).toEqual([])
      store.replace('a1', [protoTodo('t2', 'Again')])
      store.remove('a1')
      expect(store.get('a1')).toEqual([])
      dispose()
    }))
})
