import { describe, expect, it } from 'vitest'
import { kimiTodoItems } from './todo'

describe('kimiTodoItems', () => {
  it('reads each item by its title and status', () => {
    expect(kimiTodoItems([
      { title: 'Read', status: 'done' },
      { title: 'Write', status: 'in_progress' },
      { title: 'Test', status: 'pending' },
      { title: 'Other', status: 'completed' },
      'not an item',
    ])).toStrictEqual([
      { rowKey: '0:Read', content: 'Read', status: 'completed', activeForm: '' },
      { rowKey: '1:Write', content: 'Write', status: 'in_progress', activeForm: '' },
      { rowKey: '2:Test', content: 'Test', status: 'pending', activeForm: '' },
      { rowKey: '3:Other', content: 'Other', status: 'completed', activeForm: '' },
    ])
  })

  // Position is the identity, so an item that states nothing still keeps its place.
  it('reads an item that states no title and no status as a pending item with no words', () => {
    expect(kimiTodoItems([{}, { title: 'Next', status: 'unknown-word' }])).toStrictEqual([
      { rowKey: '0:', content: '', status: 'pending', activeForm: '' },
      { rowKey: '1:Next', content: 'Next', status: 'pending', activeForm: '' },
    ])
  })

  it('reads no items from anything but a list', () => {
    expect(kimiTodoItems(undefined)).toStrictEqual([])
    expect(kimiTodoItems({ title: 'x' })).toStrictEqual([])
    expect(kimiTodoItems([])).toStrictEqual([])
  })
})
