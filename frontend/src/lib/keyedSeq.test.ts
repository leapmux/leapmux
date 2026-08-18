import { describe, expect, it } from 'vitest'
import { createKeyedSeq } from './keyedSeq'

describe('createKeyedSeq', () => {
  it('counts up per key, and each key counts alone', () => {
    const seq = createKeyedSeq()
    expect(seq.next('a')).toBe(1)
    expect(seq.next('a')).toBe(2)
    expect(seq.next('b')).toBe(1)
    expect(seq.next('a')).toBe(3)
  })

  it('reports only the last sequence a key handed out as the newest', () => {
    const seq = createKeyedSeq()
    const first = seq.next('a')
    expect(seq.isNewest('a', first)).toBe(true)
    const second = seq.next('a')
    expect(seq.isNewest('a', first)).toBe(false)
    expect(seq.isNewest('a', second)).toBe(true)
  })

  it('does not let one key supersede another', () => {
    const seq = createKeyedSeq()
    const a = seq.next('a')
    seq.next('b')
    expect(seq.isNewest('a', a)).toBe(true)
  })

  // A caller that snapshots the whole map and compares a key it never
  // wrote reads 0 for that key. Without the `?? 0` on the stored side, the
  // comparison is `undefined === 0` and every untouched key looks stale --
  // which would drop the list reply for every setting the user never
  // changed.
  it('reads an untouched key as sequence 0', () => {
    const seq = createKeyedSeq()
    expect(seq.isNewest('never-written', 0)).toBe(true)
    expect(seq.isNewest('never-written', 1)).toBe(false)
  })

  // SettingRow owns one row and needs one counter, so it passes no key.
  it('serves an unkeyed caller with a single counter', () => {
    const seq = createKeyedSeq()
    const first = seq.next()
    expect(first).toBe(1)
    expect(seq.isNewest(undefined, first)).toBe(true)
    seq.next()
    expect(seq.isNewest(undefined, first)).toBe(false)
  })

  it('keeps the unkeyed counter apart from a named one', () => {
    const seq = createKeyedSeq()
    const unkeyed = seq.next()
    seq.next('theme')
    expect(seq.isNewest(undefined, unkeyed)).toBe(true)
  })

  // The unkeyed caller resolves to `''` inside the helper, so a caller
  // that passes `''` explicitly SHARES that counter -- the two are one
  // subject, not two. No production caller passes `''` today; this pins
  // the collision, so a caller that starts to derive a key from a string
  // that can be empty reads it here instead of finding it in the field.
  it('folds the empty-string key into the unkeyed counter', () => {
    const seq = createKeyedSeq()
    const unkeyed = seq.next()
    expect(seq.isNewest('', unkeyed)).toBe(true)

    const empty = seq.next('')
    expect(empty).toBe(2)
    expect(seq.isNewest(undefined, empty)).toBe(true)
    // The unkeyed caller's own sequence is superseded by a write it never
    // made.
    expect(seq.isNewest(undefined, unkeyed)).toBe(false)
    expect(seq.snapshot().get('')).toBe(2)
  })

  it('snapshots the sequences taken so far, and the copy does not move', () => {
    const seq = createKeyedSeq()
    seq.next('a')
    seq.next('a')
    const taken = seq.snapshot()
    expect(taken.get('a')).toBe(2)
    expect(taken.get('b')).toBeUndefined()

    seq.next('a')
    expect(taken.get('a')).toBe(2)
    expect(seq.isNewest('a', taken.get('a') ?? 0)).toBe(false)
  })
})
