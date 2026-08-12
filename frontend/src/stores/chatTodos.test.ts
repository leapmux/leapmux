import type { TodoItem } from '~/stores/chatTodos'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { TodoItemSchema, TodoStatus } from '~/generated/leapmux/v1/agent_pb'
import { isFinishedTodoStatus, normalizeTodoStatus, protoTodoToStore, rawTodosToItems, sortTodos, todoDisplayLabel, todoProgress } from '~/stores/chatTodos'

describe('chatTodos', () => {
  describe('normalizeTodoStatus', () => {
    it('accepts the snake_case wire form (claude/acp)', () => {
      expect(normalizeTodoStatus('in_progress')).toBe('in_progress')
    })

    it('accepts the camelCase form (codex)', () => {
      expect(normalizeTodoStatus('inProgress')).toBe('in_progress')
    })

    it('passes completed and deleted through', () => {
      expect(normalizeTodoStatus('completed')).toBe('completed')
      expect(normalizeTodoStatus('deleted')).toBe('deleted')
    })

    it('falls back to pending for unknown / non-string input', () => {
      expect(normalizeTodoStatus('bogus')).toBe('pending')
      expect(normalizeTodoStatus(undefined)).toBe('pending')
      expect(normalizeTodoStatus(42)).toBe('pending')
    })
  })

  describe('isFinishedTodoStatus', () => {
    it('is true only for completed and deleted', () => {
      expect(isFinishedTodoStatus('completed')).toBe(true)
      expect(isFinishedTodoStatus('deleted')).toBe(true)
      expect(isFinishedTodoStatus('pending')).toBe(false)
      expect(isFinishedTodoStatus('in_progress')).toBe(false)
    })
  })

  describe('todoDisplayLabel', () => {
    it('shows activeForm while in_progress when set', () => {
      expect(todoDisplayLabel({ status: 'in_progress', content: 'Run tests', activeForm: 'Running tests' })).toBe('Running tests')
    })

    it('falls back to content while in_progress without an activeForm', () => {
      expect(todoDisplayLabel({ status: 'in_progress', content: 'Run tests', activeForm: '' })).toBe('Run tests')
    })

    it('shows content for non-in_progress statuses even when activeForm is set', () => {
      expect(todoDisplayLabel({ status: 'completed', content: 'Run tests', activeForm: 'Running tests' })).toBe('Run tests')
    })
  })

  describe('protoTodoToStore', () => {
    it('maps the proto enum to the canonical string union', () => {
      const t = create(TodoItemSchema, { id: 't1', content: 'c', status: TodoStatus.IN_PROGRESS, activeForm: 'doing c', description: 'why c' })
      expect(protoTodoToStore(t)).toEqual({ id: 't1', content: 'c', status: 'in_progress', activeForm: 'doing c', description: 'why c' })
    })

    it('coerces empty id/description to undefined', () => {
      const t = create(TodoItemSchema, { id: '', content: 'c', status: TodoStatus.COMPLETED, activeForm: '', description: '' })
      const out = protoTodoToStore(t)
      expect(out.id).toBeUndefined()
      expect(out.description).toBeUndefined()
      expect(out.status).toBe('completed')
    })

    it('defaults an unspecified status to pending', () => {
      const t = create(TodoItemSchema, { content: 'c', status: TodoStatus.UNSPECIFIED, activeForm: '' })
      expect(protoTodoToStore(t).status).toBe('pending')
    })
  })

  describe('rawTodosToItems', () => {
    it('returns an empty array for non-array input', () => {
      expect(rawTodosToItems(undefined)).toEqual([])
      expect(rawTodosToItems({})).toEqual([])
    })

    it('coerces fields and normalizes status', () => {
      expect(rawTodosToItems([{ content: 'a', status: 'inProgress', activeForm: 'doing a' }])).toEqual([
        { content: 'a', status: 'in_progress', activeForm: 'doing a' },
      ])
    })

    it('tolerates missing fields with empty-string defaults', () => {
      expect(rawTodosToItems([{}])).toEqual([{ content: '', status: 'pending', activeForm: '' }])
    })
  })

  describe('todoProgress', () => {
    it('counts completed vs total excluding deleted', () => {
      const todos = [
        { content: 'a', status: 'completed' as const, activeForm: '' },
        { content: 'b', status: 'in_progress' as const, activeForm: 'doing b' },
        { content: 'c', status: 'pending' as const, activeForm: '' },
        { content: 'd', status: 'deleted' as const, activeForm: '' },
      ]
      expect(todoProgress(todos)).toEqual({ done: 1, total: 3 })
    })

    it('returns zero counts for an empty list', () => {
      expect(todoProgress([])).toEqual({ done: 0, total: 0 })
    })

    it('counts all-non-deleted as done when all completed', () => {
      const todos = [
        { content: 'a', status: 'completed' as const, activeForm: '' },
        { content: 'b', status: 'completed' as const, activeForm: '' },
      ]
      expect(todoProgress(todos)).toEqual({ done: 2, total: 2 })
    })
  })
})

describe('sortTodos', () => {
  const todo = (content: string, status: TodoItem['status']): TodoItem =>
    ({ content, status, activeForm: '' })

  it('puts in-progress first, then what is left, then what is done', () => {
    const sorted = sortTodos([
      todo('c', 'completed'),
      todo('p', 'pending'),
      todo('w', 'in_progress'),
    ])
    expect(sorted.map(t => t.content)).toEqual(['w', 'p', 'c'])
  })

  // A dropped item sorts below one that was actually finished.
  it('sinks deleted items to the bottom', () => {
    const sorted = sortTodos([
      todo('d', 'deleted'),
      todo('c', 'completed'),
      todo('w', 'in_progress'),
    ])
    expect(sorted.map(t => t.content)).toEqual(['w', 'c', 'd'])
  })

  // The store holds todos in the agent's seq order, so a stable sort means the
  // oldest of a group stays on top.
  it('keeps the oldest first within a group', () => {
    const sorted = sortTodos([
      todo('p1', 'pending'),
      todo('p2', 'pending'),
      todo('w1', 'in_progress'),
      todo('p3', 'pending'),
      todo('w2', 'in_progress'),
    ])
    expect(sorted.map(t => t.content)).toEqual(['w1', 'w2', 'p1', 'p2', 'p3'])
  })

  it('returns a new array and leaves the input alone', () => {
    const input = [todo('c', 'completed'), todo('w', 'in_progress')]
    const sorted = sortTodos(input)
    expect(sorted).not.toBe(input)
    expect(input.map(t => t.content)).toEqual(['c', 'w'])
  })

  it('handles an empty list', () => {
    expect(sortTodos([])).toEqual([])
  })
})
