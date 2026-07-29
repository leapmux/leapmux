/// <reference types="vitest/globals" />
import { createEffect, createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { createTabMetadataStore, liveTabIds } from './tabMetadata.store'

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
      m.patch('a1', { title: undefined, gitBranch: 'main' })
      expect(m.get('a1')?.title).toBe('Keep')
      expect(m.get('a1')?.gitBranch).toBe('main')
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
      m.patch('a1', { gitToplevel: '/repo', gitBranch: 'old' })
      m.patch('t1', { gitToplevel: '/repo', gitBranch: 'old' })
      m.patch('a2', { gitToplevel: '/other', gitBranch: 'old' })

      m.patchMatching(meta => meta.gitToplevel === '/repo', { gitBranch: 'new' })

      expect(m.get('a1')?.gitBranch).toBe('new')
      expect(m.get('t1')?.gitBranch).toBe('new')
      expect(m.get('a2')?.gitBranch, 'a different repo is untouched').toBe('old')
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
      m.patch('a1', { title: 'Keep', gitBranch: 'old' })
      m.patchMatching(() => true, { title: undefined, gitBranch: 'new' })
      expect(m.get('a1')?.title).toBe('Keep')
      expect(m.get('a1')?.gitBranch).toBe('new')
    })

    it('is a no-op when nothing matches', () => {
      const m = createTabMetadataStore()
      m.patch('a1', { gitBranch: 'old' })
      m.patchMatching(() => false, { gitBranch: 'new' })
      expect(m.get('a1')?.gitBranch).toBe('old')
    })

    // The counterpart to the old `equalsFields` short-circuit: rows that
    // already carry the target value must not have their subscribers re-fired.
    it('does not notify rows whose value already matches', async () => {
      await createRoot(async (dispose) => {
        const m = createTabMetadataStore()
        m.patch('stale', { gitToplevel: '/repo', gitBranch: 'old' })
        m.patch('fresh', { gitToplevel: '/repo', gitBranch: 'new' })

        let freshRuns = 0
        createEffect(() => {
          void m.state.byTabId.fresh?.gitBranch
          freshRuns++
        })
        await flush()
        const baseline = freshRuns

        m.patchMatching(meta => meta.gitToplevel === '/repo', { gitBranch: 'new' })
        await flush()

        expect(m.get('stale')?.gitBranch, 'the stale row is written').toBe('new')
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
  describe('retainOnly', () => {
    it('drops rows for tabs not in the live set', () => {
      const m = createTabMetadataStore()
      m.patch('alive', { title: 'A' })
      m.patch('dead', { title: 'B' })

      m.retainOnly(new Set(['alive']))

      expect(m.get('alive')?.title).toBe('A')
      expect(m.get('dead')).toBeUndefined()
    })

    it('clears everything for an empty live set', () => {
      const m = createTabMetadataStore()
      m.patch('a1', { title: 'A' })
      m.retainOnly(new Set())
      expect(m.get('a1')).toBeUndefined()
    })

    it('keeps every row when the live set covers them all', () => {
      const m = createTabMetadataStore()
      m.patch('a1', { title: 'A' })
      m.patch('a2', { title: 'B' })
      m.retainOnly(new Set(['a1', 'a2', 'not-yet-seen']))
      expect(m.get('a1')?.title).toBe('A')
      expect(m.get('a2')?.title).toBe('B')
    })
  })

  /**
   * Which tabs a sweep may retire.
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

    it('survives a sweep round-trip: unresolvable tabs keep their metadata', () => {
      const m = createTabMetadataStore()
      m.patch('stranded', { title: 'Still mine', screen: new Uint8Array([1, 2, 3]) })
      m.patch('gone', { title: 'Closed elsewhere' })

      // `gone` left the CRDT entirely; `stranded` is merely unplaced.
      m.retainOnly(liveTabIds({ tabs: { stranded: {} } }))

      expect(m.get('stranded')?.title).toBe('Still mine')
      expect(m.get('stranded')?.screen).toEqual(new Uint8Array([1, 2, 3]))
      expect(m.get('gone')).toBeUndefined()
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

    it('reclaims a closed terminal scrollback on sweep', () => {
      const m = createTabMetadataStore()
      m.patch('t1', { title: 'shell', screen: new Uint8Array(1024) })

      m.retainOnly(liveTabIds({
        tabs: { t1: { tombstoneAt: { physical: 9n, logical: 0n, clientId: 'a' } } },
      } as never))

      expect(m.get('t1')).toBeUndefined()
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
