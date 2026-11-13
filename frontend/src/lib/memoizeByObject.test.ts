import { describe, expect, it, vi } from 'vitest'
import { memoizeByObject } from './memoizeByObject'

describe('memoizeByObject', () => {
  it('computes once for each key object', () => {
    const compute = vi.fn((key: { n: number }) => key.n * 2)
    const memo = memoizeByObject(compute)
    const key = { n: 21 }

    expect(memo(key)).toBe(42)
    expect(memo(key)).toBe(42)

    expect(compute).toHaveBeenCalledOnce()
  })

  it('computes again for an equal but distinct key object', () => {
    const compute = vi.fn((key: { n: number }) => ({ doubled: key.n * 2 }))
    const memo = memoizeByObject(compute)
    const first = memo({ n: 1 })
    const second = memo({ n: 1 })

    expect(second).toEqual(first)
    expect(second).not.toBe(first)
    expect(compute).toHaveBeenCalledTimes(2)
  })

  it.each([
    ['null', null],
    ['undefined', undefined],
    ['zero', 0],
    ['an empty string', ''],
  ] as const)('caches an answer of %s', (_label, answer) => {
    const compute = vi.fn((_key: object) => answer)
    const memo = memoizeByObject(compute)
    const key = {}

    expect(memo(key)).toBe(answer)
    expect(memo(key)).toBe(answer)

    expect(compute).toHaveBeenCalledOnce()
  })

  it('keeps the answers of separate wrappers apart', () => {
    const key = { n: 3 }
    const doubled = memoizeByObject((k: { n: number }) => k.n * 2)
    const tripled = memoizeByObject((k: { n: number }) => k.n * 3)

    expect(doubled(key)).toBe(6)
    expect(tripled(key)).toBe(9)
  })

  it('caches nothing when the computation throws', () => {
    let fails = true
    const compute = vi.fn((_key: object) => {
      if (fails)
        throw new Error('the computation failed')
      return 'answer'
    })
    const memo = memoizeByObject(compute)
    const key = {}

    expect(() => memo(key)).toThrow('the computation failed')
    fails = false
    expect(memo(key)).toBe('answer')
    expect(compute).toHaveBeenCalledTimes(2)
  })
})
