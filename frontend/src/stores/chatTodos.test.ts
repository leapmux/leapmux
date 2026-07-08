import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { TodoItemSchema, TodoStatus } from '~/generated/leapmux/v1/agent_pb'
import { isTerminalTodoStatus, normalizeTodoStatus, protoTodoToStore, rawTodosToItems, todoDisplayLabel } from '~/stores/chatTodos'

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

  describe('isTerminalTodoStatus', () => {
    it('is true only for completed and deleted', () => {
      expect(isTerminalTodoStatus('completed')).toBe(true)
      expect(isTerminalTodoStatus('deleted')).toBe(true)
      expect(isTerminalTodoStatus('pending')).toBe(false)
      expect(isTerminalTodoStatus('in_progress')).toBe(false)
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
})
