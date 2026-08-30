/// <reference types="vitest/globals" />
import { create } from '@bufbuild/protobuf'
import { createEffect, createRoot, createSignal } from 'solid-js'
import { unwrap } from 'solid-js/store'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { AgentStatus, AvailableOptionGroupSchema } from '~/generated/proto/leapmux/v1/agent_pb'
import { TerminalStatus } from '~/generated/proto/leapmux/v1/terminal_pb'
import { KEY_TAB_MRU, sessionStorageGet, sessionStorageSet } from '~/lib/browserStorage'
import { createTabMetadataStore, liveTabIds, useMetadataSweep } from './tabMetadata.store'

/**
 * Everything about a tab the CRDT does not carry.
 *
 * Ported from the deleted `tab.store.test.ts`, whose `updateMatchingTabs` and
 * setter-no-op-guard cases describe behaviour that moved here wholesale: the
 * fan-out write and the "don't churn subscribers on an identical write" rule.
 *
 * One flat map keyed by tab id, spanning every workspace — tab ids are globally
 * unique across the account, so there is no workspace dimension to key by. That
 * is what lets a branch rename or a worker-offline sweep reach every workspace
 * in ONE call, where the old split needed the active store plus a hand-rolled
 * fan-out across each registry snapshot.
 */
const flush = () => new Promise<void>(queueMicrotask)

// The store reads `KEY_TAB_MRU` eagerly on construction now; isolate every test
// so a prior test's stamps cannot leak into the next store's seed.
beforeEach(() => sessionStorage.clear())
afterEach(() => sessionStorage.clear())

describe('tabMetadata', () => {
  describe('patch', () => {
    it('creates the row when absent and merges into it when present', () => {
      const m = createTabMetadataStore()
      m.patch('a1', { title: 'First' })
      expect(m.get('a1')).toEqual({ title: 'First' })

      m.patch('a1', { workingDir: '/repo' })
      expect(m.get('a1')).toEqual({ title: 'First', workingDir: '/repo' })
    })

    // A partial update from one source (a git-status event, say) must not blank
    // fields another source owns — every worker event patches a different
    // subset of the same row.
    it('skips undefined values rather than writing them', () => {
      const m = createTabMetadataStore()
      m.patch('a1', { title: 'Keep', workingDir: '/repo' })
      m.patch('a1', { title: undefined, gitToplevel: '/repo' })
      expect(m.get('a1')?.title).toBe('Keep')
      expect(m.get('a1')?.gitToplevel).toBe('/repo')
    })

    // Empty string and 0 are real values, not "absent" — a shell that reports
    // zero columns or an agent renamed to '' must land.
    it('writes falsy values that are not undefined', () => {
      const m = createTabMetadataStore()
      m.patch('t1', { title: 'old', cols: 80 })
      m.patch('t1', { title: '', cols: 0 })
      expect(m.get('t1')?.title).toBe('')
      expect(m.get('t1')?.cols).toBe(0)
    })

    /**
     * The worker re-decodes and re-ships whole payloads on every push, so an
     * unchanged repo or catalog arrives as an equal-but-FRESH object. A `Tab` is
     * a join result compared with `shallowEqual` and `<For>` keys its rows by
     * that identity, so writing one back re-keys the tab and tears down every
     * row rendered from it -- including the chat pane, mid-selection.
     */
    it('reuses an option-group array whose groups are all the same objects', () => {
      const m = createTabMetadataStore()
      const groups = [create(AvailableOptionGroupSchema, { id: 'model', currentValue: 'opus' })]
      m.patch('a1', { optionGroups: groups })
      const stored = unwrap(m.get('a1')!).optionGroups

      // What `mergeStableOptionGroupRefs` produces: a new array, same elements.
      m.patch('a1', { optionGroups: [...groups] })

      expect(unwrap(m.get('a1')!).optionGroups, 'element identity is what consumers key on').toBe(stored)
    })

    it('writes an option-group array whose groups changed', () => {
      const m = createTabMetadataStore()
      m.patch('a1', { optionGroups: [create(AvailableOptionGroupSchema, { id: 'model', currentValue: 'opus' })] })
      const stored = unwrap(m.get('a1')!).optionGroups

      m.patch('a1', { optionGroups: [create(AvailableOptionGroupSchema, { id: 'model', currentValue: 'sonnet' })] })

      expect(unwrap(m.get('a1')!).optionGroups).not.toBe(stored)
    })

    it('reuses an equal option-values record', () => {
      const m = createTabMetadataStore()
      m.patch('a1', { optionValues: { model: 'opus', effort: 'high' } })
      const stored = unwrap(m.get('a1')!).optionValues

      m.patch('a1', { optionValues: { model: 'opus', effort: 'high' } })
      expect(unwrap(m.get('a1')!).optionValues).toBe(stored)

      m.patch('a1', { optionValues: { model: 'sonnet', effort: 'high' } })
      expect(unwrap(m.get('a1')!).optionValues, 'a changed value must land').not.toBe(stored)
    })

    // A record that LOST a key is a real change even though every surviving key
    // still matches -- `shallowEqual` compares key counts before values.
    it('writes an option-values record that dropped a key', () => {
      const m = createTabMetadataStore()
      m.patch('a1', { optionValues: { model: 'opus', effort: 'high' } })
      const stored = unwrap(m.get('a1')!).optionValues

      m.patch('a1', { optionValues: { model: 'opus' } })
      expect(unwrap(m.get('a1')!).optionValues).not.toBe(stored)
      expect(m.get('a1')?.optionValues).toEqual({ model: 'opus' })
    })

    /**
     * `screen` is a `Uint8Array`, and a per-key compare on one would allocate an
     * index string PER BYTE of a serialized scrollback. The writer REPLACES the
     * buffer rather than mutating it, so reference identity is both the correct
     * test and the only affordable one.
     */
    it('compares a screen buffer by reference, never by content', () => {
      const m = createTabMetadataStore()
      const first = new Uint8Array([1, 2, 3])
      m.patch('t1', { screen: first })

      const equalButFresh = new Uint8Array([1, 2, 3])
      m.patch('t1', { screen: equalButFresh })

      expect(unwrap(m.get('t1')!).screen, 'a fresh buffer is a real update').toBe(equalButFresh)
    })

    it('returns undefined for a tab it has never seen', () => {
      expect(createTabMetadataStore().get('ghost')).toBeUndefined()
    })

    it('keeps rows for different tabs independent', () => {
      const m = createTabMetadataStore()
      m.patch('a1', { title: 'One' })
      m.patch('a2', { title: 'Two' })
      expect(m.get('a1')?.title).toBe('One')
      expect(m.get('a2')?.title).toBe('Two')
    })

    // The no-op guard the old store's setters carried. Solid's fine-grained
    // store only notifies subscribers of fields that actually change, so
    // re-writing an identical value must not wake the sidebar, the tab strip
    // and every tooltip on each poll.
    it('does not notify subscribers when the value is unchanged', async () => {
      await createRoot(async (dispose) => {
        const m = createTabMetadataStore()
        m.patch('a1', { title: 'Same' })

        let runs = 0
        createEffect(() => {
          void m.state.byTabId.a1?.title
          runs++
        })
        await flush()
        const baseline = runs

        m.patch('a1', { title: 'Same' })
        await flush()
        expect(runs).toBe(baseline)

        m.patch('a1', { title: 'Different' })
        await flush()
        expect(runs).toBeGreaterThan(baseline)
        dispose()
      })
    })

    it('does not notify a field subscriber when a SIBLING field changes', async () => {
      await createRoot(async (dispose) => {
        const m = createTabMetadataStore()
        m.patch('a1', { title: 'Same', cols: 80 })

        let runs = 0
        createEffect(() => {
          void m.state.byTabId.a1?.title
          runs++
        })
        await flush()
        const baseline = runs

        m.patch('a1', { cols: 120 })
        await flush()

        expect(runs).toBe(baseline)
        dispose()
      })
    })
  })

  /**
   * The fan-out write, ported from `updateMatchingTabs`.
   *
   * A branch rename or a worker-offline sweep is one call that reaches every
   * matching tab in every workspace. The predicate sees the metadata row and
   * the tab id, so callers that need placement resolve it through the view
   * first — this store deliberately knows nothing about tiles.
   */
  describe('patchMatching', () => {
    it('writes the fields onto every matching row and leaves the rest alone', () => {
      const m = createTabMetadataStore()
      m.patch('a1', { gitToplevel: '/repo', title: 'old' })
      m.patch('t1', { gitToplevel: '/repo', title: 'old' })
      m.patch('a2', { gitToplevel: '/other', title: 'old' })

      m.patchMatching(meta => meta.gitToplevel === '/repo', { title: 'new' })

      expect(m.get('a1')?.title).toBe('new')
      expect(m.get('t1')?.title).toBe('new')
      expect(m.get('a2')?.title, 'a different repo is untouched').toBe('old')
    })

    it('passes the tab id to the predicate', () => {
      const m = createTabMetadataStore()
      m.patch('keep', { title: 'a' })
      m.patch('drop', { title: 'b' })

      m.patchMatching((_meta, tabId) => tabId === 'keep', { hasNotification: true })

      expect(m.get('keep')?.hasNotification).toBe(true)
      expect(m.get('drop')?.hasNotification).toBeUndefined()
    })

    it('skips undefined values, like patch', () => {
      const m = createTabMetadataStore()
      m.patch('a1', { title: 'Keep', gitToplevel: '/old' })
      m.patchMatching(() => true, { title: undefined, gitToplevel: '/new' })
      expect(m.get('a1')?.title).toBe('Keep')
      expect(m.get('a1')?.gitToplevel).toBe('/new')
    })

    it('is a no-op when nothing matches', () => {
      const m = createTabMetadataStore()
      m.patch('a1', { gitToplevel: '/repo' })
      m.patchMatching(() => false, { gitToplevel: '/new' })
      expect(m.get('a1')?.gitToplevel).toBe('/repo')
    })

    // The counterpart to the old `equalsFields` short-circuit: rows that
    // already carry the target value must not have their subscribers re-fired.
    it('does not notify rows whose value already matches', async () => {
      await createRoot(async (dispose) => {
        const m = createTabMetadataStore()
        m.patch('stale', { gitToplevel: '/repo', title: 'old' })
        m.patch('fresh', { gitToplevel: '/repo', title: 'new' })

        let freshRuns = 0
        createEffect(() => {
          void m.state.byTabId.fresh?.title
          freshRuns++
        })
        await flush()
        const baseline = freshRuns

        m.patchMatching(meta => meta.gitToplevel === '/repo', { title: 'new' })
        await flush()

        expect(m.get('stale')?.title, 'the stale row is written').toBe('new')
        expect(freshRuns, 'the already-correct row is not re-fired').toBe(baseline)
        dispose()
      })
    })
  })

  describe('remove', () => {
    it('drops the row', () => {
      const m = createTabMetadataStore()
      m.patch('a1', { title: 'One' })
      m.remove('a1')
      expect(m.get('a1')).toBeUndefined()
    })

    it('is a no-op for an unknown id', () => {
      const m = createTabMetadataStore()
      m.patch('a1', { title: 'One' })
      m.remove('ghost')
      expect(m.get('a1')?.title).toBe('One')
    })
  })

  /**
   * The projection is the authority on what still exists, so rows for
   * tombstoned tabs are swept against its tab set rather than removed by each
   * close path.
   */
  describe('dropTabs', () => {
    it('drops exactly the rows named, and nothing else', () => {
      const m = createTabMetadataStore()
      m.patch('alive', { title: 'A' })
      m.patch('dead', { title: 'B' })

      m.dropTabs(new Set(['dead']))

      expect(m.get('alive')?.title).toBe('A')
      expect(m.get('dead')).toBeUndefined()
    })

    it('is a no-op for an empty set', () => {
      const m = createTabMetadataStore()
      m.patch('a1', { title: 'A' })
      m.dropTabs(new Set())
      expect(m.get('a1')?.title).toBe('A')
    })

    it('ignores an id it holds no row for', () => {
      const m = createTabMetadataStore()
      m.patch('a1', { title: 'A' })
      m.dropTabs(new Set(['ghost', 'a1']))
      expect(m.get('a1')).toBeUndefined()
    })
  })

  /**
   * Which tabs the CRDT still has.
   *
   * The distinction this encodes cost a real bug: keyed on the projection's
   * `ownedTabs` instead, closing a tile silently deleted the surviving tab's
   * title and terminal scrollback, because the tab left the projection for the
   * one tick its tile chain was mid-migration.
   */
  describe('liveTabIds', () => {
    it('counts every tab the CRDT has a record for', () => {
      expect(liveTabIds({ tabs: { a1: {}, t1: {} } })).toEqual(new Set(['a1', 't1']))
    })

    it('is empty for a state with no tabs', () => {
      expect(liveTabIds({ tabs: {} })).toEqual(new Set())
    })

    // The whole point: existence does not depend on the tile resolving. A tab
    // pointed at a tile that has not materialized yet is still the user's tab.
    it('counts a tab whose tile is unresolvable', () => {
      const state = { tabs: { stranded: { tileId: { value: 'not-a-real-tile' } } } }
      expect(liveTabIds(state as never).has('stranded')).toBe(true)
    })

    /**
     * A tombstone does NOT delete the map key -- `applyTombstoneTab` replaces
     * the record with a tombstoned one. Counting raw keys therefore named every
     * tab the account had ever opened, which made the sweep a no-op and let
     * closed terminals' scrollback accumulate for the life of the page.
     */
    it('excludes a tombstoned tab even though its key is still in the map', () => {
      const state = {
        tabs: {
          alive: { tombstoneAt: { physical: 0n, logical: 0n, clientId: '' } },
          closed: { tombstoneAt: { physical: 7n, logical: 0n, clientId: 'a' } },
        },
      }
      expect(liveTabIds(state as never)).toEqual(new Set(['alive']))
    })
  })

  /**
   * The sweep as it actually runs: a memo over the CRDT state feeding an effect.
   *
   * The rule the effect adds to `liveTabIds` is the load-bearing part. A row is
   * retired when its tab WAS live and stopped being live, never merely because
   * it is not live now -- every open path writes a tab's metadata BEFORE
   * emitting the op that creates it, so a CRDT tick landing in that window used
   * to delete the row of a tab that was about to exist.
   */
  describe('useMetadataSweep', () => {
    const tombstone = { physical: 7n, logical: 0n, clientId: 'a' }
    interface SweepState { tabs: Record<string, { tombstoneAt?: unknown }> }

    it('spares a row written before its tab reaches the CRDT, and retires it once closed', async () => {
      await createRoot(async (dispose) => {
        const m = createTabMetadataStore()
        const [state, setState] = createSignal<SweepState>({ tabs: {} })
        useMetadataSweep(() => state() as never, m)
        await flush()

        // The open path's order: the row first, then the op that creates the tab.
        m.patch('f1', { title: 'file.txt', fileViewMode: 'unified-diff' })

        // An unrelated batch lands in that window. This is what used to delete
        // the row: `f1` was not live, so it was retired -- and the file then
        // opened with no view mode and no diff-mode toolbar.
        setState({ tabs: { other: {} } })
        await flush()
        expect(m.get('f1')?.fileViewMode, 'the row survives the window').toBe('unified-diff')

        // The tab's own op lands.
        setState({ tabs: { other: {}, f1: {} } })
        await flush()
        expect(m.get('f1')?.fileViewMode).toBe('unified-diff')

        // Closing it tombstones the record, which IS what retires the row.
        setState({ tabs: { other: {}, f1: { tombstoneAt: tombstone } } })
        await flush()
        expect(m.get('f1'), 'a tombstone reclaims the row').toBeUndefined()
        expect(m.get('other'), 'and only that row').toBeUndefined()

        dispose()
      })
    })

    /**
     * The OTHER way a tab leaves. `consumeEntityRemoved` DELETES the record
     * rather than tombstoning it, for a workspace that moved out of this
     * subscriber's allowed set -- so a sweep that only looked for tombstones
     * would leak every one of those rows, terminal scrollback included.
     */
    it('retires a row whose record the CRDT deleted outright', async () => {
      await createRoot(async (dispose) => {
        const m = createTabMetadataStore()
        const [state, setState] = createSignal<SweepState>({ tabs: { t1: {} } })
        useMetadataSweep(() => state() as never, m)
        m.patch('t1', { title: 'shell', screen: new Uint8Array(1024) })
        await flush()
        expect(m.get('t1')?.title).toBe('shell')

        setState({ tabs: {} })
        await flush()
        expect(m.get('t1')).toBeUndefined()

        dispose()
      })
    })

    // A tab that leaves and comes back (`emitReviveTab`) must be sweepable
    // again, which it is only if the revive puts it back among the seen ids.
    it('retires a revived tab a second time', async () => {
      await createRoot(async (dispose) => {
        const m = createTabMetadataStore()
        const [state, setState] = createSignal<SweepState>({ tabs: { a1: {} } })
        useMetadataSweep(() => state() as never, m)
        await flush()

        setState({ tabs: { a1: { tombstoneAt: tombstone } } })
        await flush()

        // Revived: the record is untombstoned again, and the tab is re-titled.
        setState({ tabs: { a1: {} } })
        await flush()
        m.patch('a1', { title: 'Back' })
        expect(m.get('a1')?.title).toBe('Back')

        setState({ tabs: { a1: { tombstoneAt: tombstone } } })
        await flush()
        expect(m.get('a1')).toBeUndefined()

        dispose()
      })
    })

    /**
     * The sweep retires an id ONCE and forgets it, so a write that lands after
     * that point would strand a row nothing can ever reclaim. The terminal
     * screen sink is that writer: `disposeTerminalInstance` defers to a
     * microtask, so for a terminal closed on ANOTHER device it runs after the
     * tombstone already swept the row.
     *
     * `patchExisting` is what closes it -- the sink creates no row for a tab
     * that is gone. `patch` must keep creating them, because every open path
     * writes a tab's metadata BEFORE the op that creates it.
     */
    it('does not resurrect a swept row when a late writer patches it', async () => {
      await createRoot(async (dispose) => {
        const m = createTabMetadataStore()
        const [state, setState] = createSignal<SweepState>({ tabs: { t1: {} } })
        useMetadataSweep(() => state() as never, m)
        m.patch('t1', { title: 'shell', screen: new Uint8Array(1024) })
        await flush()

        // Closed on another device: the tombstone arrives and the row goes.
        setState({ tabs: { t1: { tombstoneAt: tombstone } } })
        await flush()
        expect(m.get('t1')).toBeUndefined()

        // The unmount's deferred capture lands AFTER that.
        m.patchExisting('t1', { screen: new Uint8Array(4096) })
        expect(m.get('t1'), 'a late capture does not re-create the row').toBeUndefined()

        // And no later sweep can reach it, which is why the WRITE had to be the
        // thing that declined: the id left the live set for good, so a `patch`
        // here strands its row for the life of the page. That contrast is the
        // whole reason the sink uses `patchExisting`.
        m.patch('t1', { screen: new Uint8Array(4096) })
        expect(m.get('t1'), 'patch still creates -- open paths depend on it').toBeDefined()
        setState({ tabs: {} })
        await flush()
        expect(m.get('t1'), 'and no sweep can ever reclaim what patch created').toBeDefined()

        dispose()
      })
    })

    /**
     * The rows the store restores from the persisted MRU map belong to tabs that
     * were live in an EARLIER session. Retiring only ids seen live in THIS one
     * left them permanently: a tab closed on another device while this page was
     * away is never reported live, so it could never be retired either, and
     * `mruSnapshot` re-persisted its stamp on every write.
     */
    it('retires a row restored from the persisted MRU for a tab that is gone', async () => {
      sessionStorageSet(KEY_TAB_MRU, { gone: 4, kept: 5 })
      await createRoot(async (dispose) => {
        const m = createTabMetadataStore()
        expect(m.get('gone'), 'seeded from the persisted MRU').toBeDefined()

        const [state, setState] = createSignal<SweepState>({ tabs: {} })
        useMetadataSweep(() => state() as never, m)
        await flush()

        // A cold start publishes an EMPTY tab set before the server fills it.
        // Retiring against that would drop the stamp of a tab about to arrive.
        expect(m.get('kept'), 'an empty first state retires nothing').toBeDefined()

        setState({ tabs: { kept: {} } })
        await flush()
        expect(m.get('gone'), 'a seeded row with no live tab is reclaimed').toBeUndefined()
        expect(m.get('kept'), 'and one whose tab did arrive is kept').toBeDefined()

        dispose()
      })
    })

    // The memo compares the live tab-id SET, so the ~60 batches/s a tile
    // drag emits -- none of which create or retire a tab -- must not re-run it.
    it('does not re-run while the live set is unchanged', async () => {
      await createRoot(async (dispose) => {
        const m = createTabMetadataStore()
        const [state, setState] = createSignal<SweepState>({ tabs: {} }, { equals: false })
        let sweeps = 0
        const counting = {
          ...m,
          dropTabs(retired: Set<string>) {
            sweeps++
            m.dropTabs(retired)
          },
        }
        useMetadataSweep(() => state() as never, counting as never)
        setState({ tabs: { a: {}, b: {} } })
        await flush()
        const baseline = sweeps

        for (let i = 0; i < 5; i++) {
          setState({ tabs: { a: {}, b: {} } })
          await flush()
        }
        expect(sweeps, 'an unchanged live set re-runs nothing').toBe(baseline)

        setState({ tabs: { a: {}, b: { tombstoneAt: tombstone } } })
        await flush()
        expect(sweeps).toBe(baseline + 1)

        dispose()
      })
    })
  })

  /**
   * A local-only monotonic counter: higher means more recently activated. It
   * orders the MRU views within one client session and never reaches the CRDT —
   * two devices should not fight over which tab was touched last.
   */
  describe('touchMru', () => {
    it('hands out strictly increasing values', () => {
      const m = createTabMetadataStore()
      const first = m.touchMru('a1')
      const second = m.touchMru('a2')
      const third = m.touchMru('a1')
      expect(second).toBeGreaterThan(first)
      expect(third).toBeGreaterThan(second)
    })

    it('writes the value onto the tab row', () => {
      const m = createTabMetadataStore()
      const n = m.touchMru('a1')
      expect(m.get('a1')?.mru).toBe(n)
    })

    it('leaves the row other fields intact', () => {
      const m = createTabMetadataStore()
      m.patch('a1', { title: 'Keep', agentStatus: AgentStatus.ACTIVE })
      m.touchMru('a1')
      expect(m.get('a1')?.title).toBe('Keep')
      expect(m.get('a1')?.agentStatus).toBe(AgentStatus.ACTIVE)
    })

    it('counts per store, not globally', () => {
      const a = createTabMetadataStore()
      const b = createTabMetadataStore()
      a.touchMru('x')
      a.touchMru('x')
      expect(b.touchMru('y')).toBe(1)
    })

    // Re-stamping the tab that ALREADY holds the newest value changes no ordering
    // anywhere -- it would only re-key the joined `Tab`, and `<For>` keys its rows
    // by that object's identity, so the write tears down and rebuilds the tab
    // strip, the sidebar tree, and the tile's pane for nothing. Every click inside
    // a tile re-activates that tile's already-active tab, so this is the hot path.
    it('is a no-op when the tab already holds the newest stamp', () => {
      const m = createTabMetadataStore()
      const first = m.touchMru('a1')
      expect(m.touchMru('a1'), 'the value it already had').toBe(first)
      expect(m.get('a1')?.mru).toBe(first)
    })

    it('still advances a tab that is not the newest', () => {
      const m = createTabMetadataStore()
      m.touchMru('a1')
      const other = m.touchMru('a2')
      const back = m.touchMru('a1')
      expect(back).toBeGreaterThan(other)
      expect(m.get('a1')!.mru!).toBeGreaterThan(m.get('a2')!.mru!)
    })
  })

  /**
   * MRU is persisted to sessionStorage (`KEY_TAB_MRU`) so a reload restores the
   * prior ordering rather than leaving every tab at zero (which made `mruHead`
   * silently fall back to `tabs[0]`). The store seeds eagerly on construction
   * and writes back (deduped) from `touchMru`, `remove`, and `dropTabs`.
   */
  describe('mru persistence', () => {
    it('writes the stamp map to sessionStorage on touchMru', () => {
      const m = createTabMetadataStore()
      m.touchMru('a')
      m.touchMru('b')
      expect(sessionStorageGet<Record<string, number>>(KEY_TAB_MRU)).toEqual({ a: 1, b: 2 })
    })

    it('seeds byTabId and mruCounter from a stored stamp map', () => {
      sessionStorageSet(KEY_TAB_MRU, { a: 5, b: 8 })
      const m = createTabMetadataStore()
      // Both stamps restored onto the rows...
      expect(m.get('a')?.mru).toBe(5)
      expect(m.get('b')?.mru).toBe(8)
      // ...and the counter resumes above the high-water mark, so the next touch
      // does not collide with a restored stamp.
      expect(m.touchMru('c')).toBe(9)
    })

    it('drops a closed tab\'s stamp on remove', () => {
      const m = createTabMetadataStore()
      m.touchMru('a')
      m.touchMru('b')
      m.remove('a')
      expect(sessionStorageGet<Record<string, number>>(KEY_TAB_MRU)).toEqual({ b: 2 })
    })

    it('drops swept tabs\' stamps on dropTabs', () => {
      const m = createTabMetadataStore()
      m.touchMru('a')
      m.touchMru('b')
      m.touchMru('c')
      m.dropTabs(new Set(['a', 'c']))
      expect(sessionStorageGet<Record<string, number>>(KEY_TAB_MRU)).toEqual({ b: 2 })
    })

    it('ignores invalid stamps (non-number, non-positive, NaN) in the seed', () => {
      sessionStorageSet(KEY_TAB_MRU, { good: 3, bad: 'x' as unknown as number, zero: 0, neg: -1, nan: Number.NaN })
      const m = createTabMetadataStore()
      expect(m.get('good')?.mru).toBe(3)
      expect(m.get('bad')).toBeUndefined()
      expect(m.get('zero')).toBeUndefined()
      expect(m.get('neg')).toBeUndefined()
      expect(m.get('nan')).toBeUndefined()
      // Counter is the max of valid stamps only.
      expect(m.touchMru('next')).toBe(4)
    })

    it('does not rewrite storage when the stamp map is unchanged', () => {
      sessionStorageSet(KEY_TAB_MRU, { a: 1 })
      // Spy on sessionStorageSet so the assertion fires even when the value is
      // byte-identical — a missing dedupe would still call the wrapper, and a
      // string-only comparison of the stored payload could not tell.
      const spy = vi.spyOn(sessionStorage, 'setItem')
      const callsBefore = spy.mock.calls.length
      const m = createTabMetadataStore()
      // A metadata-only patch changes no `mru`, so the persister must dedupe.
      m.patch('a', { title: 'renamed' })
      expect(spy.mock.calls.length, 'no write for a metadata-only patch').toBe(callsBefore)
      spy.mockRestore()
    })

    /**
     * The end-to-end behaviour issue #345 names: a reload wipes memory, the new
     * store reconstructs its MRU ordering from what the previous session wrote.
     * Each half is covered above; this pins that they compose — the second store
     * sees the first's stamps, seeds `mruCounter` above them, and a fresh touch
     * wins over every restored one.
     */
    it('survives a simulated reload: store B seeds from store A\'s writes', () => {
      const a = createTabMetadataStore()
      a.touchMru('first')
      a.touchMru('second') // 'second' is the MRU head at handoff
      a.patch('second', { title: 'has metadata too' })

      // New store — what a page reload constructs.
      const b = createTabMetadataStore()
      expect(b.get('first')?.mru).toBe(1)
      expect(b.get('second')?.mru).toBe(2)
      // Metadata on other fields is NOT carried by KEY_TAB_MRU (it holds stamps
      // only); a separate hydrator repopulates the rest. This pins that contract
      // so a future change that accidentally broadens the blob is caught.
      expect(b.get('second')?.title).toBeUndefined()

      // A fresh touch on 'first' must outrank the restored head.
      expect(b.touchMru('first')).toBeGreaterThan(b.get('second')!.mru!)
    })
  })

  // AGENT and TERMINAL tabs share one flat row keyed by tab id, so the
  // terminal's status has its own field name. A write to a plain `status`
  // would land nowhere the join reads.
  it('keeps agent and terminal status on distinct fields', () => {
    const m = createTabMetadataStore()
    m.patch('a1', { agentStatus: AgentStatus.ACTIVE })
    m.patch('t1', { terminalStatus: TerminalStatus.READY })
    expect(m.get('a1')?.agentStatus).toBe(AgentStatus.ACTIVE)
    expect(m.get('a1')?.terminalStatus).toBeUndefined()
    expect(m.get('t1')?.terminalStatus).toBe(TerminalStatus.READY)
  })
})
