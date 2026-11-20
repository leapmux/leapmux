import { describe, expect, it } from 'vitest'
import { OrderedChatPage } from './chatOrderedPage'

describe('OrderedChatPage', () => {
  it('publishes stable prefixes and keeps the index in sequence order', () => {
    const first = { id: 'first', seq: 1n }
    const second = { id: 'second', seq: 2n }
    const third = { id: 'third', seq: 3n }
    const initial = [first]
    const page = new OrderedChatPage(initial)

    expect(page.snapshot()).toBe(initial)
    expect(page.append(second)).toBe(true)
    const observed = page.snapshot()
    expect(page.snapshot()).toBe(observed)
    expect(page.append(third)).toBe(true)
    expect(page.indexOf('third')).toBe(2)
    expect(observed).toEqual([first, second])
    expect(page.finish()).toEqual([first, second, third])
    expect(page.append({ id: 'late', seq: 4n })).toBe(false)
  })

  it('reserves the append path for fresh rows beyond the loaded tail', () => {
    const page = new OrderedChatPage([
      { id: 'resident', seq: 10n },
    ])

    expect(page.append({ id: 'resident', seq: 11n })).toBe(false)
    expect(page.append({ id: 'colliding', seq: 10n })).toBe(false)
    expect(page.append({ id: 'older', seq: 9n })).toBe(false)
    expect(page.indexOf('colliding')).toBe(-1)
    expect(page.finish().map(row => row.id)).toEqual(['resident'])
  })
})
