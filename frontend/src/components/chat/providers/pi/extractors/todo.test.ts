import { describe, expect, it } from 'vitest'
import { input } from '../../testUtils'
import { piTodoSource } from './todo'

const tasks = [
  { id: 1, subject: 'Inspect sample', status: 'in_progress', activeForm: 'Inspecting sample', description: 'Read **sample.ts**.', owner: 'worker', metadata: { count: 0 } },
  { id: 2, subject: 'Report findings', status: 'pending', blockedBy: [1] },
  { id: 3, subject: 'Discarded task', status: 'deleted' },
]

function result(params: Record<string, unknown>, snapshot: unknown = tasks, extra: Record<string, unknown> = {}) {
  return { type: 'tool_execution_end', toolCallId: 'todo-call', toolName: 'todo', result: { content: [{ type: 'text', text: 'Native response' }], details: { params, action: params.action, tasks: snapshot, nextId: 4, ...extra } } }
}

describe('pi rpiv-todo sources', () => {
  it('uses the exact result snapshot to resolve a status-only request', () => {
    const request = { type: 'tool_execution_start', toolCallId: 'todo-call', toolName: 'todo', args: { action: 'update', id: 1, status: 'in_progress' } }
    const source = piTodoSource(request, undefined, input(result(request.args)))
    expect(source?.list.title).toBe('Update task: Inspect sample')
    expect(source?.list.todos).toHaveLength(1)
    expect(source?.list.todos[0]).toMatchObject({ id: '1', content: 'Inspect sample', activeForm: 'Inspecting sample', status: 'in_progress' })
    expect(source?.description).toBe('Read **sample.ts**.')
    expect(source?.metadata).toContainEqual({ label: 'Owner', value: 'worker' })
    expect(piTodoSource({ ...request, toolCallId: 'foreign' }, undefined, input(result(request.args)))?.list.title).toBe('Update task: Task #1')
  })

  it('uses the provider-assigned ID for creation', () => {
    const created = { id: 4, subject: 'New task', status: 'pending' }
    const source = piTodoSource(result({ action: 'create', subject: 'New task' }, [...tasks, created], { nextId: 5 }))
    expect(source?.list.todos.map(task => task.id)).toEqual(['4'])
  })

  it('resolves a standalone get and its dependencies without a request', () => {
    const source = piTodoSource(result({ action: 'get', id: 2 }))
    expect(source?.list.todos.map(task => task.id)).toEqual(['2'])
    expect(source?.metadata).toContainEqual({ label: 'Blocked by', value: '#1' })
  })

  it('keeps deletion visible as a tombstone', () => {
    expect(piTodoSource(result({ action: 'delete', id: 3 }))?.list.todos[0]).toMatchObject({ id: '3', status: 'deleted' })
  })

  it('applies list filters without changing the saved snapshot', () => {
    expect(piTodoSource(result({ action: 'list' }))?.list.todos.map(task => task.id)).toEqual(['1', '2'])
    expect(piTodoSource(result({ action: 'list', includeDeleted: true }))?.list.todos.map(task => task.id)).toEqual(['1', '2', '3'])
    expect(piTodoSource(result({ action: 'list', status: 'pending' }))?.list.todos.map(task => task.id)).toEqual(['2'])
    const filtered = piTodoSource(result({ action: 'list', status: 'completed' }))
    expect(filtered?.list.todos).toEqual([])
    expect(filtered?.list.emptyText).toBe('No matching tasks')
    expect(tasks).toHaveLength(3)
  })

  it('distinguishes an explicit clear from missing data', () => {
    expect(piTodoSource(result({ action: 'clear' }, []))?.list).toMatchObject({ todos: [], emptyText: 'To-do list cleared' })
    expect(piTodoSource(result({ action: 'clear' }, null))).toBeNull()
  })

  it('preserves a native failure and the unchanged snapshot', () => {
    const source = piTodoSource(result({ action: 'update', id: 99 }, tasks, { error: '#99 not found' }))
    expect(source?.error).toBe('#99 not found')
    expect(source?.list.todos).toHaveLength(3)
  })

  it.each([null, {}, [null], [{ ...tasks[0], id: 0 }], [{ ...tasks[0], id: -1 }], [{ ...tasks[0], id: 1.5 }], [{ ...tasks[0], id: Number.MAX_SAFE_INTEGER + 1 }], [{ ...tasks[0], subject: ' ' }], [{ ...tasks[0], status: 'unknown' }], [{ ...tasks[0], description: {} }], [tasks[0], tasks[0]]])('rejects an invalid snapshot: %j', (snapshot) => {
    expect(piTodoSource(result({ action: 'list' }, snapshot))).toBeNull()
  })

  it('does not treat another extension or unknown operation as rpiv-todo', () => {
    expect(piTodoSource({ ...result({ action: 'list' }), toolName: 'other' })).toBeNull()
    expect(piTodoSource(result({ action: 'unknown' }))).toBeNull()
  })
})
