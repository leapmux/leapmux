import { describe, expect, it } from 'vitest'
import { CODEWHALE_TOOL } from '~/generated/contracts/codewhale-protocol'
import { codewhaleChecklistItems, codewhalePlanNote, codewhaleTodoItems } from './todo'

describe('codewhaleTodoItems', () => {
  it('reads the list every to-do alias sends', () => {
    expect(codewhaleTodoItems(CODEWHALE_TOOL.ChecklistWrite, { todos: [{ content: 'A', status: 'cancelled' }] })).toMatchObject([{ content: 'A', status: 'deleted' }])
    expect(codewhaleTodoItems(CODEWHALE_TOOL.TodoWrite, { todos: [] })).toStrictEqual([])
  })

  it('reads a plan\'s steps, and only for the plan tool', () => {
    expect(codewhaleTodoItems(CODEWHALE_TOOL.UpdatePlan, { plan: [{ step: 'Look', status: 'in_progress' }, 'x'] })).toMatchObject([{ content: 'Look', status: 'in_progress' }])
    expect(codewhaleTodoItems(CODEWHALE_TOOL.TodoWrite, { plan: [{ step: 'Look' }] })).toBeNull()
  })

  it('answers null for arguments that carry no list', () => {
    expect(codewhaleTodoItems(CODEWHALE_TOOL.TodoWrite, {})).toBeNull()
    expect(codewhaleTodoItems(CODEWHALE_TOOL.TodoWrite, { todos: 'x' })).toBeNull()
  })
})

describe('codewhalePlanNote', () => {
  it('reads the explanation of a plan alone', () => {
    expect(codewhalePlanNote(CODEWHALE_TOOL.UpdatePlan, { explanation: ' Why ' })).toBe('Why')
    expect(codewhalePlanNote(CODEWHALE_TOOL.TodoWrite, { explanation: 'Why' })).toBe('')
  })
})

describe('codewhaleChecklistItems', () => {
  it('reads the runtime\'s kept checklist', () => {
    expect(codewhaleChecklistItems({ task_updates: { checklist: { items: [{ id: 1, content: 'A', status: 'completed' }] } } })).toMatchObject([{ content: 'A', status: 'completed' }])
    expect(codewhaleChecklistItems({})).toBeNull()
    expect(codewhaleChecklistItems({ task_updates: { checklist: {} } })).toBeNull()
  })
})
