import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createPerAgentListStore, createPerAgentStore } from '~/stores/chatPerAgentStore'

describe('createPerAgentStore', () => {
  it('returns the configured empty value for an unset agent', () => {
    createRoot((dispose) => {
      const store = createPerAgentStore<string>('')
      expect(store.get('a')).toBe('')
      const list = createPerAgentStore<number[]>([])
      expect(list.get('a')).toEqual([])
      dispose()
    })
  })

  it('set then get round-trips a value, scoped per agent', () => {
    createRoot((dispose) => {
      const store = createPerAgentStore<string>('')
      store.set('a', 'hello')
      store.set('b', 'world')
      expect(store.get('a')).toBe('hello')
      expect(store.get('b')).toBe('world')
      expect(store.get('c')).toBe('')
      dispose()
    })
  })

  it('clear resets an agent to the empty value', () => {
    createRoot((dispose) => {
      const store = createPerAgentStore<string>('')
      store.set('a', 'hello')
      store.clear('a')
      expect(store.get('a')).toBe('')
      dispose()
    })
  })

  it('clears to undefined for an undefined-empty store', () => {
    createRoot((dispose) => {
      const store = createPerAgentStore<{ n: number } | undefined>(undefined)
      store.set('a', { n: 1 })
      expect(store.get('a')).toEqual({ n: 1 })
      store.clear('a')
      expect(store.get('a')).toBeUndefined()
      dispose()
    })
  })

  it('byAgent distinguishes an unset agent from one explicitly set to empty', () => {
    createRoot((dispose) => {
      const store = createPerAgentStore<number[]>([])
      // get() hides the difference behind the empty fallback; byAgent exposes it,
      // which is what todos.replace relies on to always do the first set.
      expect(store.byAgent.a).toBeUndefined()
      expect(store.get('a')).toEqual([])
      store.clear('a')
      expect(store.byAgent.a).toEqual([])
      dispose()
    })
  })

  it('remove deletes the key entirely, unlike clear (no residual empty entry)', () => {
    createRoot((dispose) => {
      const store = createPerAgentStore<number[]>([])
      store.set('a', [1, 2])
      store.set('b', [3])
      // clear leaves the key present (set to empty); remove deletes it outright,
      // so a closed agent leaves no residue and a byAgent presence check sees it gone.
      store.clear('a')
      expect(store.byAgent.a).toEqual([])
      store.remove('a')
      expect(store.byAgent.a).toBeUndefined()
      expect('a' in store.byAgent).toBe(false)
      expect(store.get('a')).toEqual([])
      // Other agents are untouched.
      expect(store.byAgent.b).toEqual([3])
      dispose()
    })
  })

  it('remove is a no-op for an agent that was never set', () => {
    createRoot((dispose) => {
      const store = createPerAgentStore<string>('')
      expect(() => store.remove('ghost')).not.toThrow()
      expect(store.byAgent.ghost).toBeUndefined()
      dispose()
    })
  })

  it('round-trips a full-object write (the shape viewport scroll uses)', () => {
    // The object-valued slice (viewportScroll) always writes a FULL object, so the
    // SolidJS leaf-merge for objects coincides with a replace -- every field is
    // overwritten. (Partial-object writes would merge, but no slice does them.)
    createRoot((dispose) => {
      const store = createPerAgentStore<{ top: number, anchor: string } | undefined>(undefined)
      store.set('a', { top: 10, anchor: 'x' })
      store.set('a', { top: 20, anchor: 'y' })
      expect(store.get('a')).toEqual({ top: 20, anchor: 'y' })
      dispose()
    })
  })

  // The reason the method exists: a UI that renders these entries as rows
  // reconciles by REFERENCE, so a replace that hands over all-new objects tears
  // down and rebuilds every row on screen -- losing the tooltip under the
  // pointer and restarting each row's animation.
  it('setReconciled keeps the identity of an entry that did not change', () => {
    createRoot((dispose) => {
      const store = createPerAgentListStore<{ id: string, n: number }>('id')
      store.set('a', [{ id: 'x', n: 1 }, { id: 'y', n: 1 }])
      const [x, y] = store.get('a')

      store.setReconciled('a', [{ id: 'x', n: 1 }, { id: 'y', n: 2 }])

      expect(store.get('a')[0]).toBe(x)
      expect(store.get('a')[1]).toBe(y)
      expect(store.get('a')[1]!.n).toBe(2)
      dispose()
    })
  })

  it('setReconciled adds, drops and reorders entries by key', () => {
    createRoot((dispose) => {
      const store = createPerAgentListStore<{ id: string, n: number }>('id')
      store.set('a', [{ id: 'x', n: 1 }, { id: 'y', n: 2 }])

      store.setReconciled('a', [{ id: 'y', n: 2 }, { id: 'z', n: 3 }])

      expect(store.get('a').map(e => e.id)).toEqual(['y', 'z'])
      expect(store.get('a').map(e => e.n)).toEqual([2, 3])
      dispose()
    })
  })

  // `reconcile` writes its diff THROUGH the store proxy into the object
  // underneath, and the empty value is one object shared by every unset agent.
  // Reconciling into it would give every other agent this agent's rows.
  it('setReconciled leaves the shared empty value alone', () => {
    createRoot((dispose) => {
      const store = createPerAgentListStore<{ id: string }>('id')
      const empty = store.get('untouched')

      store.setReconciled('a', [{ id: 'x' }])
      expect(store.get('a').map(e => e.id)).toEqual(['x'])
      expect(empty).toEqual([])
      expect(store.get('b')).toEqual([])

      // The same hazard through the other door: a CLEARED agent holds the
      // shared empty as its value, not undefined.
      store.clear('a')
      store.setReconciled('a', [{ id: 'z' }])
      expect(store.get('a').map(e => e.id)).toEqual(['z'])
      expect(empty).toEqual([])
      dispose()
    })
  })

  // The key is declared on the STORE, so it cannot differ between two writes to
  // one dataset, and a misspelling is a compile error rather than a silent fall
  // back to a positional merge -- which is worse than a rebuild, because a row
  // then keeps its DOM identity while its content shifts under the pointer.
  it('setReconciled reconciles every write under the one key the store declares', () => {
    createRoot((dispose) => {
      const store = createPerAgentListStore<{ id: string, n: number }>('id')
      store.set('a', [{ id: 'x', n: 1 }, { id: 'y', n: 2 }])
      const [x] = store.get('a')

      // Reordered AND changed: a positional merge would rewrite entry 0 in
      // place and the identity below would follow the wrong row.
      store.setReconciled('a', [{ id: 'y', n: 9 }, { id: 'x', n: 1 }])

      expect(store.get('a').map(e => e.id)).toEqual(['y', 'x'])
      expect(store.get('a')[1]).toBe(x)
      expect(store.get('a')[0]!.n).toBe(9)
      dispose()
    })
  })

  it('set replaces (does not index-merge) a shorter array, and clear empties it', () => {
    // The pendingOutbound/todos slices depend on this: setting a shorter array
    // must drop the trailing entries, not keep them via SolidJS index-merge.
    createRoot((dispose) => {
      const store = createPerAgentStore<number[]>([])
      store.set('a', [1, 2, 3])
      store.set('a', [9])
      expect(store.get('a')).toEqual([9])
      store.clear('a')
      expect(store.get('a')).toEqual([])
      dispose()
    })
  })
})
