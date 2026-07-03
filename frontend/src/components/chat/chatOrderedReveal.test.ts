import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createOrderedTailReveal } from './chatOrderedReveal'

describe('chatorderedreveal', () => {
  /**
   * Drive the gate from a plain reactive model: `ids` is the ordered slice and
   * `loading` is the set of rows still awaiting their own measurement. Returns
   * the held-set accessor plus mutators, all inside one reactive root.
   */
  function harness(initialIds: string[], initialLoading: string[] = []) {
    let dispose = () => {}
    let held: () => ReadonlySet<string> = () => new Set()
    const [ids, setIds] = createSignal<string[]>(initialIds)
    const [loading, setLoading] = createSignal<ReadonlySet<string>>(new Set(initialLoading))
    createRoot((d) => {
      dispose = d
      held = createOrderedTailReveal(ids, id => loading().has(id))
    })
    const setLoadingIds = (values: string[]) => setLoading(new Set(values))
    return { held: () => [...held()].sort(), setIds, setLoadingIds, dispose }
  }

  it('holds nothing when every row is measured', () => {
    const h = harness(['a', 'b', 'c'], [])
    expect(h.held()).toEqual([])
    h.dispose()
  })

  it('holds a measured later row while an earlier appended row is still loading', () => {
    // a is settled; b + c are appended and loading. b measures first -> it must NOT
    // reveal ahead of... nothing before it, so b is free; but if c measured first it
    // would be held behind b.
    const h = harness(['a', 'b', 'c'], ['b', 'c'])
    expect(h.held()).toEqual([]) // both still loading -> hidden by their own skeleton, nothing "held"

    // c (the tail) finishes measuring FIRST, while b is still loading.
    h.setLoadingIds(['b'])
    // c is ready but must wait for b: it is HELD so it can't appear before b.
    expect(h.held()).toEqual(['c'])

    // b measures -> both reveal together, in order.
    h.setLoadingIds([])
    expect(h.held()).toEqual([])
    h.dispose()
  })

  it('reveals an append burst in order as the prefix measures front-to-back', () => {
    const h = harness(['a', 'b', 'c', 'd'], ['b', 'c', 'd'])
    // d measures first, then c: both held behind the still-loading b.
    h.setLoadingIds(['b', 'c'])
    expect(h.held()).toEqual(['d'])
    h.setLoadingIds(['b'])
    expect(h.held()).toEqual(['c', 'd'])
    // b reveals -> the whole prefix is now free.
    h.setLoadingIds([])
    expect(h.held()).toEqual([])
    h.dispose()
  })

  it('never re-hides an already-revealed row when an earlier row re-enters loading', () => {
    // Everything measured and revealed.
    const h = harness(['a', 'b', 'c'], [])
    expect(h.held()).toEqual([])
    // a churns back into loading (a height-key change re-measures it). b and c were
    // already shown -- they must stay shown, not flicker back to skeletons.
    h.setLoadingIds(['a'])
    expect(h.held()).toEqual([])
    h.dispose()
  })

  it('does not hold rows below a freshly loading row entering at the top (scroll-up)', () => {
    // Steady state: three measured, all revealed.
    const h = harness(['a', 'b', 'c'], [])
    expect(h.held()).toEqual([])
    // Scroll up: a new unmeasured row w enters at the TOP. The rows below it were
    // already revealed, so they must not be held behind w.
    h.setIds(['w', 'a', 'b', 'c'])
    h.setLoadingIds(['w'])
    expect(h.held()).toEqual([])
    h.dispose()
  })

  it('forgets a released row once it scrolls out of the window', () => {
    const h = harness(['a', 'b'], [])
    expect(h.held()).toEqual([])
    // b scrolls away, then a new appended tail c arrives loading while a stale
    // "a is loading" arrives too -- c must be held behind a again (a is not
    // treated as pre-released for the new cohort because it never left, but the
    // pruning path is exercised by dropping b).
    h.setIds(['a', 'c'])
    h.setLoadingIds(['a', 'c'])
    expect(h.held()).toEqual([])
    h.setLoadingIds(['a'])
    expect(h.held()).toEqual(['c'])
    h.dispose()
  })
})
