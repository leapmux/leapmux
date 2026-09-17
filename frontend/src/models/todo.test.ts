import type { TodoItem } from '~/models/todo'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { TodoItemSchema, TodoStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { isFinishedTodoStatus, normalizeTodoStatus, protoTodoToItem, rawTodosToItems, sortTodos, todoDisplayLabel, todoProgress, todoRowKey } from '~/models/todo'

describe('TodoItem', () => {
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

    // A CANCELLED task reports the same end state the tombstone carries: the row
    // stays visible and stops being work. Cursor and OpenCode both send the word,
    // and the worker's own parser reads it onto StatusDeleted, so the row and the
    // sidebar agree. Without this it fell through to `pending` -- the state that
    // claims the least, which is the wrong claim for a task nobody will do.
    it('reads a cancelled task as deleted, in either spelling', () => {
      expect(normalizeTodoStatus('cancelled')).toBe('deleted')
      expect(normalizeTodoStatus('canceled')).toBe('deleted')
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

  // The key a UI reconciles a to-do row by. An incremental provider gives an
  // id; a snapshot-only one gives none and re-sends the whole list, so there
  // POSITION is the identity.
  describe('todoRowKey', () => {
    it('takes the provider id when there is one, whatever the position', () => {
      expect(todoRowKey('t1', 0, 'Run tests')).toBe('t1')
      expect(todoRowKey('t1', 7, 'Run tests')).toBe('t1')
    })

    it('pairs the position with the content when the provider gives no id', () => {
      expect(todoRowKey(undefined, 0, 'Run tests')).toBe('0:Run tests')
      expect(todoRowKey(undefined, 2, 'Run tests')).toBe('2:Run tests')
    })

    // The property the reconcile needs: two rows of one list never collide, so
    // no row can take another's identity and its content with it.
    it('separates two rows that differ only in position, or only in content', () => {
      expect(todoRowKey(undefined, 0, 'Same')).not.toBe(todoRowKey(undefined, 1, 'Same'))
      expect(todoRowKey(undefined, 0, 'A')).not.toBe(todoRowKey(undefined, 0, 'B'))
    })

    // An empty content is a real case -- `rawTodosToItems` coerces a missing
    // field to '' -- and it must still produce a key that separates rows.
    it('still separates rows whose content is empty', () => {
      expect(todoRowKey(undefined, 0, '')).toBe('0:')
      expect(todoRowKey(undefined, 0, '')).not.toBe(todoRowKey(undefined, 1, ''))
    })
  })

  describe('protoTodoToItem', () => {
    it('maps the proto enum to the canonical string union', () => {
      const t = create(TodoItemSchema, { id: 't1', content: 'c', status: TodoStatus.IN_PROGRESS, activeForm: 'doing c', description: 'why c' })
      expect(protoTodoToItem(t, 0)).toEqual({ id: 't1', rowKey: 't1', content: 'c', status: 'in_progress', activeForm: 'doing c', description: 'why c' })
    })

    it('coerces empty id/description to undefined', () => {
      const t = create(TodoItemSchema, { id: '', content: 'c', status: TodoStatus.COMPLETED, activeForm: '', description: '' })
      const out = protoTodoToItem(t, 3)
      expect(out.id).toBeUndefined()
      // No id, so the key is the row's position paired with its content.
      expect(out.rowKey).toBe('3:c')
      expect(out.description).toBeUndefined()
      expect(out.status).toBe('completed')
    })

    it('defaults an unspecified status to pending', () => {
      const t = create(TodoItemSchema, { content: 'c', status: TodoStatus.UNSPECIFIED, activeForm: '' })
      expect(protoTodoToItem(t, 0).status).toBe('pending')
    })
  })

  describe('rawTodosToItems', () => {
    it('skips malformed entries while retaining each valid entry position', () => {
      expect(rawTodosToItems([null, 0, [], { content: 'Keep', status: 'pending' }, {}])).toEqual([
        { rowKey: '3:Keep', content: 'Keep', status: 'pending', activeForm: '' },
        { rowKey: '4:', content: '', status: 'pending', activeForm: '' },
      ])
    })

    // Seven call sites feed this untyped provider wire arrays, so a `content` that
    // is an object or an array reaches here -- and `String()` turned one into the
    // literal "[object Object]", which the checklist and the Background-tasks rail
    // then drew verbatim. Only a shape that stringifies into a description of
    // itself becomes the empty string.
    it.each([
      ['an object', { a: 1 }],
      ['an array', ['a', 'b']],
      ['null', null],
    ])('reads %s text field as no text at all', (_name, value) => {
      const items = rawTodosToItems([{ content: value, activeForm: value }])
      expect(items).toEqual([{ rowKey: '0:', content: '', status: 'pending', activeForm: '' }])
    })

    // A PRIMITIVE keeps its text. A provider that states a numeric step means that
    // digit, and reading every non-string as absent would erase it -- including the
    // zero, which is the one a falsy test would also lose.
    it.each([
      ['a number', 42, '42'],
      ['a numeric zero', 0, '0'],
      ['a boolean', true, 'true'],
    ])('keeps %s text field as its own text', (_name, value, expected) => {
      expect(rawTodosToItems([{ content: value, activeForm: value }])).toEqual([
        { rowKey: `0:${expected}`, content: expected, status: 'pending', activeForm: expected },
      ])
    })

    it('separates two malformed rows by their position', () => {
      expect(rawTodosToItems([{ content: { a: 1 } }, { content: { b: 2 } }]).map(t => t.rowKey))
        .toEqual(['0:', '1:'])
    })

    it('returns an empty array for non-array input', () => {
      expect(rawTodosToItems(undefined)).toEqual([])
      expect(rawTodosToItems({})).toEqual([])
    })

    it('coerces fields and normalizes status', () => {
      expect(rawTodosToItems([{ content: 'a', status: 'inProgress', activeForm: 'doing a' }])).toEqual([
        { rowKey: '0:a', content: 'a', status: 'in_progress', activeForm: 'doing a' },
      ])
    })

    it('tolerates missing fields with empty-string defaults', () => {
      expect(rawTodosToItems([{}])).toEqual([{ rowKey: '0:', content: '', status: 'pending', activeForm: '' }])
    })
  })

  describe('todoProgress', () => {
    it('counts completed vs total excluding deleted', () => {
      const todos = [
        { rowKey: '0:a', content: 'a', status: 'completed' as const, activeForm: '' },
        { rowKey: 'b', content: 'b', status: 'in_progress' as const, activeForm: 'doing b' },
        { rowKey: 'c', content: 'c', status: 'pending' as const, activeForm: '' },
        { rowKey: 'd', content: 'd', status: 'deleted' as const, activeForm: '' },
      ]
      expect(todoProgress(todos)).toEqual({ done: 1, total: 3 })
    })

    it('returns zero counts for an empty list', () => {
      expect(todoProgress([])).toEqual({ done: 0, total: 0 })
    })

    it('counts all-non-deleted as done when all completed', () => {
      const todos = [
        { rowKey: '0:a', content: 'a', status: 'completed' as const, activeForm: '' },
        { rowKey: 'b', content: 'b', status: 'completed' as const, activeForm: '' },
      ]
      expect(todoProgress(todos)).toEqual({ done: 2, total: 2 })
    })
  })
})

describe('sortTodos', () => {
  const todo = (content: string, status: TodoItem['status']): TodoItem =>
    ({ rowKey: content, content, status, activeForm: '' })

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
