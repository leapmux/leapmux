import type { CodeTokens } from './codeTokenCache'
import { describe, expect, it } from 'vitest'
import { codeTokenKey, createCodeTokenCache } from './codeTokenCache'

/** Tokens that identify the text they belong to. */
function tokensOf(text: string): CodeTokens {
  return [
    [
      { content: text, className: 'sk-a' },
    ],
  ]
}

/** A key of exactly `length` characters. */
function keyOfLength(prefix: string, length: number): string {
  return prefix.padEnd(length, '.')
}

describe('codeTokenKey', () => {
  it('separates the theme, the language and the text', () => {
    expect(codeTokenKey('a,b', 'ts', 'x')).toBe('a,b\u0000ts\u0000x')
    // A text that starts like another language does not collide with it.
    expect(codeTokenKey('t', 'ts', 'x')).not.toBe(codeTokenKey('t', 't', 'sx'))
  })

  it('accepts an empty text', () => {
    expect(codeTokenKey('t', 'ts', '')).toBe('t\u0000ts\u0000')
  })
})

describe('createCodeTokenCache', () => {
  it('returns the tokens that it stored, and nothing for an unknown key', () => {
    const cache = createCodeTokenCache()
    cache.set('a', tokensOf('a'))

    expect(cache.get('a')).toEqual(tokensOf('a'))
    expect(cache.has('a')).toBe(true)
    expect(cache.get('b')).toBeUndefined()
    expect(cache.has('b')).toBe(false)
  })

  it('keeps empty tokens as an entry', () => {
    const cache = createCodeTokenCache()
    cache.set('plain', [])

    expect(cache.has('plain')).toBe(true)
    expect(cache.get('plain')).toEqual([])
  })

  it('drops the oldest entries when the keys grow past the limit', () => {
    const cache = createCodeTokenCache(30)
    const first = keyOfLength('first', 10)
    const second = keyOfLength('second', 10)
    const third = keyOfLength('third', 10)
    const fourth = keyOfLength('fourth', 10)
    cache.set(first, tokensOf('1'))
    cache.set(second, tokensOf('2'))
    cache.set(third, tokensOf('3'))
    // Exactly at the limit, so nothing goes.
    expect([first, second, third].map(cache.has)).toEqual([true, true, true])

    cache.set(fourth, tokensOf('4'))

    expect([first, second, third, fourth].map(cache.has)).toEqual([false, true, true, true])
  })

  it('limits the total length of the keys, not their count', () => {
    const cache = createCodeTokenCache(100)
    const short = Array.from({ length: 20 }, (_, i) => `k${i}`)
    for (const key of short)
      cache.set(key, tokensOf(key))
    // Twenty short keys fit in 100 characters.
    expect(short.every(cache.has)).toBe(true)

    const long = keyOfLength('long', 90)
    cache.set(long, tokensOf('long'))

    expect(cache.has(long)).toBe(true)
    const left = short.filter(cache.has)
    // Only the newest short keys stay beside the long one.
    expect(left).toEqual(short.slice(short.length - left.length))
    expect(left.join('').length + long.length).toBeLessThanOrEqual(100)
  })

  it('keeps the newest entry even when its key alone exceeds the limit', () => {
    const cache = createCodeTokenCache(10)
    cache.set('old', tokensOf('old'))
    const huge = keyOfLength('huge', 1_000)

    cache.set(huge, tokensOf('huge'))

    expect(cache.get(huge)).toEqual(tokensOf('huge'))
    expect(cache.has('old')).toBe(false)

    // The next entry replaces it.
    cache.set('next', tokensOf('next'))
    expect(cache.has(huge)).toBe(false)
    expect(cache.has('next')).toBe(true)
  })

  it('does not count a key twice when it is stored again', () => {
    const cache = createCodeTokenCache(20)
    const a = keyOfLength('a', 10)
    const b = keyOfLength('b', 10)
    cache.set(a, tokensOf('a1'))
    cache.set(b, tokensOf('b'))
    cache.set(a, tokensOf('a2'))

    // Still 20 characters: both stay, and `a` holds its new tokens.
    expect(cache.has(b)).toBe(true)
    expect(cache.get(a)).toEqual(tokensOf('a2'))
  })

  it('moves a key that is stored again to the newest end', () => {
    const cache = createCodeTokenCache(20)
    const a = keyOfLength('a', 10)
    const b = keyOfLength('b', 10)
    const c = keyOfLength('c', 10)
    cache.set(a, tokensOf('a1'))
    cache.set(b, tokensOf('b'))
    cache.set(a, tokensOf('a2'))

    cache.set(c, tokensOf('c'))

    // `b` was the oldest after `a` was stored again, so `b` goes.
    expect([a, b, c].map(cache.has)).toEqual([true, false, true])
    expect(cache.get(a)).toEqual(tokensOf('a2'))
  })

  it('keeps only the newest entry under a limit of zero', () => {
    const cache = createCodeTokenCache(0)
    cache.set('first', tokensOf('first'))
    expect(cache.get('first')).toEqual(tokensOf('first'))

    cache.set('second', tokensOf('second'))

    expect(cache.has('first')).toBe(false)
    expect(cache.get('second')).toEqual(tokensOf('second'))
  })

  it('moves an entry that `get` reads to the newest end, and leaves the order alone on `has`', () => {
    const cache = createCodeTokenCache(20)
    const a = keyOfLength('a', 10)
    const b = keyOfLength('b', 10)
    const c = keyOfLength('c', 10)
    cache.set(a, tokensOf('a'))
    cache.set(b, tokensOf('b'))
    cache.get(a)
    cache.has(b)

    cache.set(c, tokensOf('c'))

    // `b` was the oldest: `get` moved `a` after it, and `has` did not move `b`.
    expect([a, b, c].map(cache.has)).toEqual([true, false, true])
  })
})
