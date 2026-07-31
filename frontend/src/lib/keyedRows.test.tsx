import { render, screen } from '@solidjs/testing-library'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createKeyedRows, KeyedFor } from './keyedRows'

interface Row { id: string, title: string }

describe('createKeyedRows', () => {
  it('keeps the key list identical when only a field changed', () => {
    createRoot((dispose) => {
      const [items, setItems] = createSignal<Row[]>([{ id: 'a', title: 'A' }, { id: 'b', title: 'B' }])
      const { keys } = createKeyedRows(items, r => r.id)

      const first = keys()
      // A join result is REBUILT on any field change, so the whole array arrives
      // with fresh object identities. That is exactly the churn the guard exists
      // to absorb: same keys, so no `<For>` reconciliation.
      setItems([{ id: 'a', title: 'renamed' }, { id: 'b', title: 'B' }])
      expect(keys(), 'a field change must not re-key the rows').toBe(first)
      dispose()
    })
  })

  it('emits a new key list when a row is added, removed, or reordered', () => {
    createRoot((dispose) => {
      const [items, setItems] = createSignal<Row[]>([{ id: 'a', title: 'A' }, { id: 'b', title: 'B' }])
      const { keys } = createKeyedRows(items, r => r.id)

      const first = keys()
      setItems([{ id: 'b', title: 'B' }, { id: 'a', title: 'A' }])
      expect(keys(), 'a reorder is a real change').not.toBe(first)
      expect(keys()).toEqual(['b', 'a'])

      const reordered = keys()
      setItems([{ id: 'b', title: 'B' }])
      expect(keys()).not.toBe(reordered)
      expect(keys()).toEqual(['b'])
      dispose()
    })
  })

  it('resolves the CURRENT item for a key after a field change', () => {
    createRoot((dispose) => {
      const [items, setItems] = createSignal<Row[]>([{ id: 'a', title: 'A' }])
      const { byKey } = createKeyedRows(items, r => r.id)

      expect(byKey().get('a')?.title).toBe('A')
      // The lookup is deliberately unguarded: it is how a changed item reaches
      // the row that renders it, so it must NOT hold the previous object.
      setItems([{ id: 'a', title: 'renamed' }])
      expect(byKey().get('a')?.title).toBe('renamed')
      dispose()
    })
  })

  it('keys through the supplied extractor, so two kinds sharing an id stay distinct', () => {
    createRoot((dispose) => {
      const [items] = createSignal([
        { id: 'x', title: 'agent' },
        { id: 'x', title: 'file' },
      ])
      const { keys, byKey } = createKeyedRows(items, r => `${r.title}:${r.id}`)

      expect(keys()).toEqual(['agent:x', 'file:x'])
      expect(byKey().size, 'a bare id would have collapsed these to one row').toBe(2)
      dispose()
    })
  })
})

describe('keyedFor', () => {
  it('drops a row whose lookup comes back empty, without crashing it', () => {
    const [present, setPresent] = createSignal(true)
    render(() => (
      <KeyedFor
        each={['a', 'b']}
        lookup={key => (key === 'b' && !present() ? undefined : { label: key.toUpperCase() })}
      >
        {item => <span data-testid="row">{item().label}</span>}
      </KeyedFor>
    ))
    expect(screen.getAllByTestId('row').map(e => e.textContent)).toEqual(['A', 'B'])

    // The parent rebuilt its map without `b` before the key list reconciled.
    // A `!` assertion here would read through undefined and throw.
    setPresent(false)
    expect(screen.getAllByTestId('row').map(e => e.textContent)).toEqual(['A'])
  })

  /**
   * `rowSetup` runs in the FOR-ROW owner, so its result outlives a tick where
   * the item fails to resolve. TabBar's drag handle depends on exactly this: a
   * `createSortable` created inside the `<Show>` would be disposed and rebuilt
   * on that tick, dropping a drag mid-gesture.
   */
  it('keeps the rowSetup result across a tick the lookup missed', () => {
    const [present, setPresent] = createSignal(true)
    const setups: string[] = []
    render(() => (
      <KeyedFor
        each={['a']}
        lookup={() => (present() ? { label: 'A' } : undefined)}
        rowSetup={(key) => {
          setups.push(key)
          return { handle: key }
        }}
      >
        {(item, _key, row) => <span data-testid="row">{`${item().label}:${row.handle}`}</span>}
      </KeyedFor>
    ))
    expect(screen.getByTestId('row').textContent).toBe('A:a')
    expect(setups).toEqual(['a'])

    setPresent(false)
    expect(screen.queryByTestId('row')).toBeNull()
    setPresent(true)

    expect(screen.getByTestId('row').textContent).toBe('A:a')
    expect(setups, 'the row was never re-set-up').toEqual(['a'])
  })
})
