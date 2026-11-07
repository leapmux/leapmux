import { describe, expect, it } from 'vitest'
import { codewhaleChecklistItems, codewhaleTodoItems } from './todo'

describe('codewhaleTodoItems', () => {
  it('reads the list every to-do alias sends', () => {
    expect(codewhaleTodoItems({
      todos: [
        { content: 'A', status: 'cancelled' },
      ],
    })).toMatchObject([
      { content: 'A', status: 'deleted' },
    ])
    expect(codewhaleTodoItems({ todos: [] })).toStrictEqual([])
  })

  it('answers null for arguments that carry no list', () => {
    expect(codewhaleTodoItems({})).toBeNull()
    expect(codewhaleTodoItems({ todos: 'x' })).toBeNull()
  })
})

describe('codewhaleChecklistItems', () => {
  it('reads the runtime\'s kept checklist', () => {
    expect(codewhaleChecklistItems({
      task_updates: {
        checklist: {
          items: [
            { id: 1, content: 'A', status: 'completed' },
          ],
        },
      },
    })).toMatchObject([
      { content: 'A', status: 'completed' },
    ])
    expect(codewhaleChecklistItems({})).toBeNull()
    expect(codewhaleChecklistItems({
      task_updates: { checklist: {} },
    })).toBeNull()
  })
})
