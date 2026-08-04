import type { Accessor, JSX } from 'solid-js'
import { createMemo, For, Show } from 'solid-js'
import { shallowEqualArrays } from './shallowEqual'

/**
 * The two halves a `<For>` over JOIN RESULTS needs: a stable list of keys to
 * iterate, and a lookup the row resolves its item through.
 *
 * `<For>` keys its rows by item REFERENCE identity. A `Tab` (see tabView) and a
 * `RenderedTab` are join results, rebuilt whenever any field they derive from
 * changes -- a title rename, a git badge, an agent status flip, an MRU stamp, an
 * OSC title a shell emits per prompt. Iterating those objects directly makes
 * every such change dispose the row and mount a fresh one, which throws away
 * whatever the DOM was holding: a text selection mid-gesture, a focused rename
 * `<input>`, a drag in flight, an xterm instance and its pooled WebGL context.
 *
 * Keys are strings, so the `shallowEqualArrays` guard means the `<For>`
 * reconciles only when a row is actually added, removed, or reordered. Every
 * other field is then read reactively INSIDE the row, where Solid updates props
 * in place instead of replacing the subtree.
 *
 * `byKey` is deliberately NOT guarded: it is the row's reactive source, and
 * rebuilding it is how a changed item reaches the row that renders it. It is one
 * pass per change rather than a `find` per row.
 *
 * `keyOf` rather than a fixed `.id`, because the two identities in play differ:
 * panes key on a bare tab id, while lists that mix tab kinds key on `tabKey`
 * (`type:id`), which is what makes an AGENT and a FILE tab sharing an id
 * distinct rows.
 */
export function createKeyedRows<T>(
  items: Accessor<readonly T[]>,
  keyOf: (item: T) => string,
): { keys: Accessor<string[]>, byKey: Accessor<Map<string, T>> } {
  return { keys: createStableKeys(items, keyOf), byKey: createKeyLookup(items, keyOf) }
}

/**
 * The key half of {@link createKeyedRows}, on its own.
 *
 * For rows that resolve their item through a lookup they already have -- the
 * tile panes go through `tabView`'s narrowed `getAgentTab`/`getFileTab`, which
 * is both narrower and shared -- a second local Map would be built and
 * maintained per tick for nothing.
 */
export function createStableKeys<T>(
  items: Accessor<readonly T[]>,
  keyOf: (item: T) => string,
): Accessor<string[]> {
  // Captured rather than returned inline so `eslint-plugin-solid`'s reactivity
  // rule can see the memo it is analysing.
  const keys = createMemo(() => items().map(keyOf), [], { equals: shallowEqualArrays })
  return keys
}

/**
 * The lookup half of {@link createKeyedRows}, on its own.
 *
 * The mirror of {@link createStableKeys}: for a list whose key ORDER comes from
 * somewhere else -- a cached grouping, a sorted structure computed upstream --
 * while the item each key resolves to must stay LIVE. `WorkspaceTabTree` is
 * exactly that shape: its tree memo is gated on a fingerprint of the fields
 * that decide grouping and order, so pairing its cached buckets with a lookup
 * built from those same cached arrays would freeze every field the fingerprint
 * omits (see that file's `tabBuildKey`). Taking the keys from the structure and
 * the items from here keeps the caching where it belongs and the rendering
 * live.
 *
 * Deliberately NOT guarded by an `equals`, for the same reason `createKeyedRows`
 * leaves its `byKey` unguarded: this map IS the row's reactive source, and
 * rebuilding it is how a changed item reaches the row that renders it.
 */
export function createKeyLookup<T>(
  items: Accessor<readonly T[]>,
  keyOf: (item: T) => string,
): Accessor<Map<string, T>> {
  const byKey = createMemo(() => new Map(items().map(item => [keyOf(item), item])))
  return byKey
}

/**
 * The `<For>`/`<Show>` scaffold that every keyed list over join results writes,
 * as one component.
 *
 * Iterate stable string keys, resolve each row's item through a lookup, and drop
 * the row when the lookup comes back empty. That last part is why `<Show>` and
 * not a `!` assertion: a parent can rebuild its map WITHOUT a key before this
 * `<For>` has reconciled the key list, and a row that reads through the
 * resulting `undefined` crashes on the next render rather than simply
 * disappearing. Five hand-written copies of this shape each had to remember
 * that; now none of them do.
 *
 * `rowSetup` runs in the FOR-ROW owner, outside the `<Show>`, and that placement
 * is the whole reason it exists as a knob rather than something the caller does
 * inline. `TabBar` creates its `createSortable(key)` handle there: a handle
 * created inside the `<Show>` would be disposed and re-created every time the
 * row's item momentarily failed to resolve, dropping a drag mid-gesture. Its
 * result is handed to `children` so the row can use it.
 */
export function KeyedFor<T, R = undefined>(props: {
  /** Stable keys -- pass `createStableKeys`/`createKeyedRows`' `keys()`. */
  each: readonly string[]
  /** Resolves a key to its item. Tracked, so the row updates in place. */
  lookup: (key: string) => T | undefined | null
  /** Optional per-row setup, evaluated in the for-row owner. See above. */
  rowSetup?: (key: string) => R
  children: (item: Accessor<NonNullable<T>>, key: string, row: R) => JSX.Element
}): JSX.Element {
  return (
    <For each={props.each}>
      {(key) => {
        const row = props.rowSetup?.(key) as R
        return (
          <Show when={props.lookup(key)}>
            {item => props.children(item, key, row)}
          </Show>
        )
      }}
    </For>
  )
}
