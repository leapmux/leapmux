import type { OpenSubagentTabDeps } from './openSubagentTab'
/// <reference types="vitest/globals" />
import type { AgentTab } from '~/stores/tab.types'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { tabKey } from '~/stores/tab.helpers'
// `vi.mock` below is hoisted above this import by vitest, so the imported
// values are the mocked ones. The emit payloads are read back out of these.
import { emitAddTab, emitReviveTab, positionAfterKey } from '~/stores/tabOps'
import { openSubagentTab } from './openSubagentTab'

/**
 * `openSubagentTab` has four return paths and a strict metadata-before-emit
 * ordering rule. The deps struct is narrow enough that the whole shape can be
 * faked with plain objects + `vi.fn()` -- there is no CRDT bridge to stand up
 * because `emitAddTab` / `emitReviveTab` are the only writers and they go
 * through a module mock below.
 *
 * `vi.mock('~/stores/tabOps')` records every emit call into `calls` so a test
 * can assert WHICH op fired AND that `metadata.patch` ran first (the order
 * rule openTabInFocusedTile documents the same way). `positionAfterKey` is
 * imported by the source from the same module, so the mock returns a sentinel
 * string the assertions can read back out of the recorded emit payload.
 */
const calls: Array<{ kind: 'patch' | 'add' | 'revive', id: string }> = []
const positions: string[] = []

vi.mock('~/stores/tabOps', () => ({
  emitAddTab: vi.fn((tab: { id: string, position: string }) => {
    calls.push({ kind: 'add', id: tab.id })
    positions.push(tab.position)
    return null
  }),
  emitReviveTab: vi.fn((tab: { id: string, position: string }) => {
    calls.push({ kind: 'revive', id: tab.id })
    positions.push(tab.position)
    return null
  }),
  // Sentinel position so the emit payload is assertable without pulling in the
  // real LexoRank helper (the source only passes the result through).
  positionAfterKey: vi.fn(() => 'POS_AFTER_PARENT'),
}))

afterEach(() => {
  calls.length = 0
  positions.length = 0
  vi.clearAllMocks()
})

/** The mocked emit-add payload from the most recent openSubagentTab call. */
function lastAddPayload(): { id: string, tileId: string, position: string, workerId?: string } {
  return vi.mocked(emitAddTab).mock.calls.at(-1)![0] as { id: string, tileId: string, position: string, workerId?: string }
}

/** The mocked emit-revive payload from the most recent openSubagentTab call. */
function lastRevivePayload(): { id: string, tileId: string, position: string, workerId: string } {
  return vi.mocked(emitReviveTab).mock.calls.at(-1)![0] as { id: string, tileId: string, position: string, workerId: string }
}

/** A placed, worker-hosted parent agent tab. */
function parentAgentTab(overrides: Partial<AgentTab> = {}): AgentTab {
  return {
    type: TabType.AGENT,
    id: 'parent-1',
    workspaceId: 'ws-1',
    tileId: 'tile-A',
    workerId: 'worker-1',
    workingDir: '/repo',
    agentProvider: AgentProvider.CLAUDE_CODE,
    ...overrides,
  }
}

/**
 * Minimal deps double. Only the methods `openSubagentTab` actually reads are
 * faked; everything else stays `vi.fn()` so an unexpected call is visible.
 */
function makeDeps(opts: {
  getAgentTab?: (id: string) => AgentTab | undefined
  hasProjectedTile?: (tileId: string) => boolean
  focusedTileId?: () => string
  activeKeyForTile?: (tileId: string) => string | null
  speculativeTabs?: () => Record<string, { tombstoneAt?: unknown } | undefined>
}): { deps: OpenSubagentTabDeps, seq: string[], agentTabs: Map<string, AgentTab> } {
  // The single ordered trace both writers append to, so a test can assert that
  // `patch` precedes `add`/`revive` by reading positions out of `seq`.
  const seq: string[] = []
  const agentTabs = new Map<string, AgentTab>()
  // Re-wrap so a test that mutates `agentTabs` after construction still flows
  // through the live map (the default closes over `agentTabs` at call time, not
  // build time), while an explicit opts.getAgentTab override is consulted last.
  const getAgentTab = opts.getAgentTab
    ? (id: string) => agentTabs.get(id) ?? opts.getAgentTab!(id)
    : (id: string) => agentTabs.get(id)

  const deps: OpenSubagentTabDeps = {
    view: {
      getAgentTab,
      forTile: vi.fn(() => []),
    } as unknown as OpenSubagentTabDeps['view'],
    layoutStore: {
      focusedTileId: vi.fn(opts.focusedTileId ?? (() => 'tile-focused')),
      hasProjectedTile: vi.fn(opts.hasProjectedTile ?? (() => true)),
    } as unknown as OpenSubagentTabDeps['layoutStore'],
    selection: {
      setActiveById: vi.fn(() => {
        seq.push('select')
        calls.push({ kind: 'patch', id: '__select__' }) // not used; kept for symmetry
      }),
      activeKeyForTile: vi.fn(opts.activeKeyForTile ?? (() => null)),
    } as unknown as OpenSubagentTabDeps['selection'],
    metadata: {
      patch: vi.fn((id: string) => {
        seq.push(`patch:${id}`)
      }),
    } as unknown as OpenSubagentTabDeps['metadata'],
    speculativeTabs: vi.fn(opts.speculativeTabs ?? (() => ({}))),
    focusTileId: vi.fn((tileId: string) => {
      seq.push(`focus:${tileId}`)
    }),
  }
  return { deps, seq, agentTabs }
}

describe('openSubagentTab', () => {
  describe('1. dedup-activate (tab already open)', () => {
    it('returns "activated", selects the child, and focuses its tile', () => {
      const { deps, agentTabs } = makeDeps({})
      // The child tab is already projected on tile-A.
      const existing: AgentTab = {
        type: TabType.AGENT,
        id: 'child-1',
        workspaceId: 'ws-1',
        tileId: 'tile-A',
      }
      agentTabs.set('child-1', existing)

      const result = openSubagentTab(deps, { childAgentId: 'child-1', parentAgentId: 'parent-1' })

      expect(result).toBe('activated')
      // Active selection moved to the child id.
      expect(deps.selection.setActiveById).toHaveBeenCalledWith(TabType.AGENT, 'child-1')
      // The existing tab's tile gets focused.
      expect(deps.focusTileId).toHaveBeenCalledWith('tile-A')
      // No emit fired: the dedup path neither places nor revives.
      expect(calls.filter(c => c.kind === 'add' || c.kind === 'revive')).toEqual([])
    })

    it('returns "no-tile" when no childAgentId is supplied', () => {
      const { deps } = makeDeps({})
      expect(openSubagentTab(deps, {})).toBe('no-tile')
      expect(deps.selection.setActiveById).not.toHaveBeenCalled()
    })
  })

  describe('2. opened (placement after a placed parent)', () => {
    it('returns "opened", patches metadata FIRST, then emits the placement', () => {
      const { deps, agentTabs, seq } = makeDeps({})
      agentTabs.set('parent-1', parentAgentTab())

      const result = openSubagentTab(deps, {
        childAgentId: 'child-1',
        parentAgentId: 'parent-1',
        title: 'Subagent',
      })

      expect(result).toBe('opened')

      // The child lands on the parent's tile (the parent is placed + has a worker).
      const addCalls = calls.filter(c => c.kind === 'add')
      expect(addCalls).toHaveLength(1)

      // ORDER RULE: metadata.patch runs before emitAddTab. `seq` records the
      // patch id then the selection/focus; the emit is recorded in the module
      // mock's `calls` between them. Asserting the patch id is first in `seq`
      // and the emit is the only add in `calls` is enough: the source calls
      // `metadata.patch(...)` immediately before `emitAddTab(...)`.
      expect(seq[0]).toBe('patch:child-1')

      // The child is selected.
      expect(deps.selection.setActiveById).toHaveBeenCalledWith(TabType.AGENT, 'child-1')
    })

    it('emits against the parent\'s tile with a position computed after the parent key', () => {
      const { deps, agentTabs } = makeDeps({})
      agentTabs.set('parent-1', parentAgentTab())

      openSubagentTab(deps, { childAgentId: 'child-1', parentAgentId: 'parent-1' })

      expect(emitAddTab).toHaveBeenCalledTimes(1)
      const payload = lastAddPayload()
      expect(payload.id).toBe('child-1')
      expect(payload.tileId).toBe('tile-A')
      expect(payload.workerId).toBe('worker-1')
      // positionAfterKey was invoked (the source computes position from the
      // parent's key) and the result was threaded into the emit.
      expect(payload.position).toBe('POS_AFTER_PARENT')
      // And positionAfterKey received the parent's tab key as the anchor.
      expect(positionAfterKey).toHaveBeenCalled()
    })
  })

  describe('3. fallback to the focused tile (no placed parent)', () => {
    it('places on layoutStore.focusedTileId() and resolves workerId via the active tile tab', () => {
      // Parent absent -> parentTileId is undefined -> fallback to focused tile.
      // No parent workerId -> activeWorkerIdForTile resolves via the tile's
      // active AGENT tab. Seed that active tab so the worker resolves.
      const activeRoot: AgentTab = {
        type: TabType.AGENT,
        id: 'root-owner',
        workspaceId: 'ws-1',
        workerId: 'worker-root',
      }
      const { deps, agentTabs } = makeDeps({
        focusedTileId: () => 'tile-focused',
        // The focused tile's active selection IS the root owner that spawned
        // the child, so its workerId is the fallback.
        activeKeyForTile: (tileId: string) =>
          tileId === 'tile-focused' ? tabKey({ type: TabType.AGENT, id: 'root-owner' }) : null,
      })
      agentTabs.set('root-owner', activeRoot)

      const result = openSubagentTab(deps, { childAgentId: 'child-orphan' })

      expect(result).toBe('opened')
      const payload = lastAddPayload()
      expect(payload.tileId).toBe('tile-focused')
      expect(payload.workerId).toBe('worker-root')
    })

    it('returns "opened" even when parentAgentId names an unplaced parent', () => {
      // Parent exists but has no tileId -> parentTileId() is undefined -> fallback.
      const { deps, agentTabs } = makeDeps({
        focusedTileId: () => 'tile-focused',
        activeKeyForTile: (tileId: string) =>
          tileId === 'tile-focused' ? tabKey({ type: TabType.AGENT, id: 'root-owner' }) : null,
      })
      agentTabs.set('parent-1', parentAgentTab({ tileId: undefined, workerId: 'worker-1' }))
      agentTabs.set('root-owner', { type: TabType.AGENT, id: 'root-owner', workspaceId: 'ws-1', workerId: 'w-root' })

      const result = openSubagentTab(deps, { childAgentId: 'child-1', parentAgentId: 'parent-1' })

      expect(result).toBe('opened')
    })
  })

  describe('4. refusal (no projected tile / no workerId)', () => {
    it('returns "no-tile" when the resolved tile is not projected', () => {
      const { deps } = makeDeps({
        focusedTileId: () => 'tile-ghost',
        hasProjectedTile: () => false,
      })
      // A placed parent still wins for tile resolution, but hasProjectedTile is
      // globally false here so neither path can place.
      const result = openSubagentTab(deps, { childAgentId: 'child-1', parentAgentId: 'parent-1' })
      expect(result).toBe('no-tile')
      expect(calls.filter(c => c.kind === 'add')).toEqual([])
    })

    it('returns "no-tile" when no workerId is resolvable (parent absent, no active agent on the tile)', () => {
      const { deps } = makeDeps({
        focusedTileId: () => 'tile-focused',
        hasProjectedTile: () => true,
        // No active selection on the tile -> activeWorkerIdForTile returns undefined.
        activeKeyForTile: () => null,
      })
      // No parent, no active agent tab -> workerId is unresolvable.
      const result = openSubagentTab(deps, { childAgentId: 'child-1' })
      expect(result).toBe('no-tile')
      expect(calls.filter(c => c.kind === 'add')).toEqual([])
    })

    it('returns "no-tile" when the parent has no workerId and the tile has no active agent', () => {
      const { deps, agentTabs } = makeDeps({
        focusedTileId: () => 'tile-focused',
        hasProjectedTile: () => true,
        activeKeyForTile: () => null,
      })
      agentTabs.set('parent-1', parentAgentTab({ workerId: undefined }))

      const result = openSubagentTab(deps, { childAgentId: 'child-1', parentAgentId: 'parent-1' })
      expect(result).toBe('no-tile')
    })
  })

  describe('5. revived (tombstoned speculative tab)', () => {
    it('returns "revived" and emits emitReviveTab on a placed tile with a parent workerId', () => {
      const { deps, agentTabs } = makeDeps({
        speculativeTabs: () => ({ 'child-1': { tombstoneAt: { nano: 1n } } }),
      })
      agentTabs.set('parent-1', parentAgentTab())

      const result = openSubagentTab(deps, { childAgentId: 'child-1', parentAgentId: 'parent-1' })

      expect(result).toBe('revived')
      // revive, NOT add.
      expect(calls.filter(c => c.kind === 'revive')).toHaveLength(1)
      expect(calls.filter(c => c.kind === 'add')).toEqual([])

      expect(emitReviveTab).toHaveBeenCalledTimes(1)
      const payload = lastRevivePayload()
      expect(payload.id).toBe('child-1')
      expect(payload.tileId).toBe('tile-A')
      expect(payload.workerId).toBe('worker-1')
      // The child is selected after revive.
      expect(deps.selection.setActiveById).toHaveBeenCalledWith(TabType.AGENT, 'child-1')
    })

    it('refuses revive when the parent has no projected tile and the focused tile is also unprojected', () => {
      const { deps } = makeDeps({
        hasProjectedTile: () => false,
        speculativeTabs: () => ({ 'child-1': { tombstoneAt: { nano: 1n } } }),
      })
      const result = openSubagentTab(deps, { childAgentId: 'child-1', parentAgentId: 'parent-1' })
      expect(result).toBe('no-tile')
      expect(calls.filter(c => c.kind === 'revive')).toEqual([])
    })

    it('revives via the active-tile worker fallback when the row carries no parentAgentId', () => {
      // Claude/ACP never set parentAgentId on a registry row (the owner IS the
      // parent). The revive branch must share the open branch's worker
      // resolution: fall back to the active AGENT tab in the target tile.
      // Without the fallback, a closed-then-reopened child is unopenable even
      // though the same row opens fine the first time.
      const activeRoot: AgentTab = {
        type: TabType.AGENT,
        id: 'root-owner',
        workspaceId: 'ws-1',
        workerId: 'worker-root',
      }
      const { deps, agentTabs } = makeDeps({
        focusedTileId: () => 'tile-focused',
        activeKeyForTile: (tileId: string) =>
          tileId === 'tile-focused' ? tabKey({ type: TabType.AGENT, id: 'root-owner' }) : null,
        speculativeTabs: () => ({ 'child-1': { tombstoneAt: { nano: 1n } } }),
      })
      agentTabs.set('root-owner', activeRoot)

      // No parentAgentId on the item -- the open branch's fallback path must
      // also apply to the revive branch.
      const result = openSubagentTab(deps, { childAgentId: 'child-1' })

      expect(result).toBe('revived')
      const payload = lastRevivePayload()
      expect(payload.id).toBe('child-1')
      expect(payload.workerId).toBe('worker-root')
    })
  })
})
