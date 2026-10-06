import type { AvailableOptionGroup } from '~/generated/proto/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it } from 'vitest'
import { AgentProvider, AvailableOptionGroupSchema, AvailableOptionSchema } from '~/generated/proto/leapmux/v1/agent_pb'
import { clearSettingsLabelCache, collectSettingsLabelDependencies, getCachedSettingsGroupLabel, getCachedSettingsLabel, settingsLabelCacheRevision, settingsLabelDependencyRevision, updateSettingsLabelCache } from './settingsLabelCache'

const CLAUDE = AgentProvider.CLAUDE_CODE
const CURSOR = AgentProvider.CURSOR
const GOOSE = AgentProvider.GOOSE

function group(id: string, label: string, options: Array<[string, string]>): AvailableOptionGroup {
  return create(AvailableOptionGroupSchema, {
    id,
    label,
    options: options.map(([oid, name]) => create(AvailableOptionSchema, { id: oid, name })),
  })
}

describe('settingsLabelCache', () => {
  it('bumps its revision only when display metadata changes', () => {
    clearSettingsLabelCache()
    const before = settingsLabelCacheRevision()
    const groups = [group('model', 'Model', [['sonnet', 'Sonnet']])]

    updateSettingsLabelCache(CLAUDE, groups)
    const changed = settingsLabelCacheRevision()
    expect(changed).toBeGreaterThan(before)

    updateSettingsLabelCache(CLAUDE, groups)
    expect(settingsLabelCacheRevision()).toBe(changed)
  })

  it('changes a dependency revision only for the label group that moved', () => {
    updateSettingsLabelCache(CLAUDE, [group('model', 'Model', [['sonnet', 'Sonnet']])])
    const collected = collectSettingsLabelDependencies(() => getCachedSettingsLabel(CLAUDE, 'model', 'sonnet'))
    const before = settingsLabelDependencyRevision(collected.dependencies)

    updateSettingsLabelCache(CLAUDE, [group('permissionMode', 'Mode', [['plan', 'Plan']])])
    expect(settingsLabelDependencyRevision(collected.dependencies)).toBe(before)

    updateSettingsLabelCache(CLAUDE, [group('model', 'Model', [['sonnet', 'Sonnet 4']])])
    expect(settingsLabelDependencyRevision(collected.dependencies)).not.toBe(before)
  })

  beforeEach(() => clearSettingsLabelCache())

  it('caches the group label and each option label by id', () => {
    updateSettingsLabelCache(CLAUDE, [group('model', 'Model', [['sonnet', 'Sonnet'], ['opus', 'Opus']])])
    expect(getCachedSettingsGroupLabel(CLAUDE, 'model')).toBe('Model')
    expect(getCachedSettingsLabel(CLAUDE, 'model', 'sonnet')).toBe('Sonnet')
    expect(getCachedSettingsLabel(CLAUDE, 'model', 'opus')).toBe('Opus')
  })

  it('retains a label for an option no longer in the group (historical notifications still reference it)', () => {
    updateSettingsLabelCache(CLAUDE, [group('effort', 'Effort', [['xhigh', 'XHigh'], ['ultracode', 'Ultra Code']])])
    // Switching off the model that offered "ultracode" narrows the effort catalog, but a
    // past settings_changed notification that set effort to "ultracode" must still resolve
    // its label on scroll-back -- append-merge keeps it rather than evicting.
    updateSettingsLabelCache(CLAUDE, [group('effort', 'Effort', [['xhigh', 'XHigh']])])
    expect(getCachedSettingsLabel(CLAUDE, 'effort', 'xhigh')).toBe('XHigh')
    expect(getCachedSettingsLabel(CLAUDE, 'effort', 'ultracode')).toBe('Ultra Code')
  })

  it('picks up a renamed option (id reused for a new display name)', () => {
    updateSettingsLabelCache(CLAUDE, [group('effort', 'Effort', [['high', 'High']])])
    updateSettingsLabelCache(CLAUDE, [group('effort', 'Effort', [['high', 'High (xhigh clamp)']])])
    expect(getCachedSettingsLabel(CLAUDE, 'effort', 'high')).toBe('High (xhigh clamp)')
  })

  it('takes the first of two same-id groups in one push (no silent merge)', () => {
    // A well-formed catalog carries one group per id; a malformed one with two same-id groups
    // must not merge their option sets (or last-writer-wins their labels) under a single id.
    updateSettingsLabelCache(CLAUDE, [
      group('effort', 'Effort', [['high', 'High']]),
      group('effort', 'Effort (dupe)', [['low', 'Low']]),
    ])
    expect(getCachedSettingsLabel(CLAUDE, 'effort', 'high')).toBe('High')
    expect(getCachedSettingsLabel(CLAUDE, 'effort', 'low')).toBeUndefined()
    expect(getCachedSettingsGroupLabel(CLAUDE, 'effort')).toBe('Effort')
  })

  it('preserves prior labels on a transient empty-options report', () => {
    updateSettingsLabelCache(CLAUDE, [group('model', 'Model', [['sonnet', 'Sonnet']])])
    // An empty-options push must not wipe the group's labels.
    updateSettingsLabelCache(CLAUDE, [group('model', 'Model', [])])
    expect(getCachedSettingsLabel(CLAUDE, 'model', 'sonnet')).toBe('Sonnet')
    expect(getCachedSettingsGroupLabel(CLAUDE, 'model')).toBe('Model')
  })

  it('scopes labels by provider so a same-id group does not collide across providers', () => {
    // Cursor and Goose both expose a `permissionMode` group, but with different option
    // sets. A global (provider-blind) cache would let one overwrite the other; per-provider
    // keying keeps each agent's notification resolving against its own provider's labels.
    updateSettingsLabelCache(CURSOR, [group('permissionMode', 'Mode', [['agent', 'Agent'], ['plan', 'Plan']])])
    updateSettingsLabelCache(GOOSE, [group('permissionMode', 'Mode', [['auto', 'Auto'], ['approve', 'Approve']])])

    expect(getCachedSettingsLabel(CURSOR, 'permissionMode', 'agent')).toBe('Agent')
    expect(getCachedSettingsLabel(GOOSE, 'permissionMode', 'approve')).toBe('Approve')
    // Cross-provider lookups miss rather than returning the other provider's label.
    expect(getCachedSettingsLabel(CURSOR, 'permissionMode', 'approve')).toBeUndefined()
    expect(getCachedSettingsLabel(GOOSE, 'permissionMode', 'agent')).toBeUndefined()
  })

  it('returns undefined for an unknown provider (notification without provider context)', () => {
    updateSettingsLabelCache(CLAUDE, [group('model', 'Model', [['sonnet', 'Sonnet']])])
    expect(getCachedSettingsLabel(undefined, 'model', 'sonnet')).toBeUndefined()
    expect(getCachedSettingsGroupLabel(undefined, 'model')).toBeUndefined()
  })

  it('no-ops on undefined groups and clears on demand', () => {
    updateSettingsLabelCache(CLAUDE, undefined)
    expect(getCachedSettingsGroupLabel(CLAUDE, 'model')).toBeUndefined()
    updateSettingsLabelCache(CLAUDE, [group('model', 'Model', [['sonnet', 'Sonnet']])])
    clearSettingsLabelCache()
    expect(getCachedSettingsLabel(CLAUDE, 'model', 'sonnet')).toBeUndefined()
    expect(getCachedSettingsGroupLabel(CLAUDE, 'model')).toBeUndefined()
  })

  it('bounds a group\'s label map with LRU eviction, keeping recently-seen ids', () => {
    // Churn far past the per-group cap (256), seeing each id once.
    for (let i = 0; i < 600; i++)
      updateSettingsLabelCache(CURSOR, [group('model', 'Model', [[`m${i}`, `Model ${i}`]])])
    // The oldest ids are evicted (fall back to raw value); the most-recent are retained.
    expect(getCachedSettingsLabel(CURSOR, 'model', 'm0')).toBeUndefined()
    expect(getCachedSettingsLabel(CURSOR, 'model', 'm599')).toBe('Model 599')
    expect(getCachedSettingsLabel(CURSOR, 'model', 'm400')).toBe('Model 400')

    // Re-seeing an old id refreshes it to most-recent, so a further (sub-cap) round of
    // churn evicts older ids before it -- m344 (now the oldest) goes, m400 survives.
    updateSettingsLabelCache(CURSOR, [group('model', 'Model', [['m400', 'Model 400']])])
    for (let i = 600; i < 700; i++)
      updateSettingsLabelCache(CURSOR, [group('model', 'Model', [[`m${i}`, `Model ${i}`]])])
    expect(getCachedSettingsLabel(CURSOR, 'model', 'm344')).toBeUndefined()
    expect(getCachedSettingsLabel(CURSOR, 'model', 'm400')).toBe('Model 400')
  })

  it('bounds the GROUP-id dimension with LRU eviction (non-conforming server cycling group ids)', () => {
    // A non-conforming server cycling distinct GROUP ids must not grow the outer maps without
    // bound -- the group-id dimension is LRU-capped (64) just like the per-group option-id one.
    for (let i = 0; i < 200; i++)
      updateSettingsLabelCache(CURSOR, [group(`g${i}`, `Group ${i}`, [[`opt${i}`, `Option ${i}`]])])
    // The oldest groups are evicted from BOTH outer maps (label and option sub-map); the most
    // recent are retained.
    expect(getCachedSettingsGroupLabel(CURSOR, 'g0')).toBeUndefined()
    expect(getCachedSettingsLabel(CURSOR, 'g0', 'opt0')).toBeUndefined()
    expect(getCachedSettingsGroupLabel(CURSOR, 'g199')).toBe('Group 199')
    expect(getCachedSettingsLabel(CURSOR, 'g199', 'opt199')).toBe('Option 199')
  })

  it('keeps the group-label and option-label maps in lockstep across label-only / option-only pushes', () => {
    // [V17] A group reported with a label but no options, then with options but no label, must
    // refresh its LRU position in BOTH outer maps -- otherwise it half-evicts (its group label
    // ages out of one map while its option labels stay in the other) under churn from other
    // groups, leaving one of its two lookups to fall back to the raw id even though it is current.
    updateSettingsLabelCache(GOOSE, [group('pinned', 'Pinned', [['a', 'A']])])

    // Churn 70 OTHER groups (> the 64 group cap) while re-touching 'pinned' each round with an
    // option-only push (EMPTY label) -- the exact divergence trigger.
    for (let i = 0; i < 70; i++) {
      updateSettingsLabelCache(GOOSE, [group(`other${i}`, `Other ${i}`, [[`o${i}`, `O ${i}`]])])
      updateSettingsLabelCache(GOOSE, [{ id: 'pinned', label: '', options: [{ id: 'a', name: 'A' }] } as unknown as AvailableOptionGroup])
    }

    // 'pinned' was refreshed every round, so it survives in BOTH maps despite > cap churn: its
    // group label (kept by the label-or-prior refresh) AND its option label stay resolvable.
    expect(getCachedSettingsGroupLabel(GOOSE, 'pinned')).toBe('Pinned')
    expect(getCachedSettingsLabel(GOOSE, 'pinned', 'a')).toBe('A')
  })

  it('evicts a group\'s label and option sub-map together once later pushes pass the cap (no half-eviction)', () => {
    // A LABELED group, then 64 other groups in a later push: under the old two-map design, whose
    // label and option maps had different eviction populations, the old group's option sub-map
    // could evict while its label survived (a half-eviction). With one record per group, a group's
    // label and options evict -- or survive -- together.
    updateSettingsLabelCache(GOOSE, [group('old', 'Old', [['a', 'A']])])
    const groups: AvailableOptionGroup[] = []
    for (let i = 0; i < 63; i++)
      groups.push(group(`fill${i}`, `Fill ${i}`, [[`f${i}`, `F ${i}`]]))
    groups.push(group('new', 'New', [['z', 'Z']]))
    updateSettingsLabelCache(GOOSE, groups)
    updateSettingsLabelCache(GOOSE, [group('newest', 'Newest', [['n', 'N']])])

    // 'old' is the least recent of 66 groups, so its WHOLE entry evicts -- both lookups miss.
    expect(getCachedSettingsGroupLabel(GOOSE, 'old')).toBeUndefined()
    expect(getCachedSettingsLabel(GOOSE, 'old', 'a')).toBeUndefined()
    // 'new' is among the 64 most recent groups, so its WHOLE entry survives -- both lookups resolve.
    expect(getCachedSettingsGroupLabel(GOOSE, 'new')).toBe('New')
    expect(getCachedSettingsLabel(GOOSE, 'new', 'z')).toBe('Z')
  })

  // A provider can offer more options than the cap: Kilo's catalog holds about 400 models. The cap
  // bounds labels that stopped appearing, so it must never evict an option of the current push.
  describe('a push larger than the cap', () => {
    function models(count: number): AvailableOptionGroup {
      return group('model', 'Model', Array.from({ length: count }, (_, i): [string, string] => [`m${i}`, `Model ${i}`]))
    }

    it('keeps the label of every option that the push offers', () => {
      clearSettingsLabelCache()
      updateSettingsLabelCache(CURSOR, [models(400)])
      expect(getCachedSettingsLabel(CURSOR, 'model', 'm0')).toBe('Model 0')
      expect(getCachedSettingsLabel(CURSOR, 'model', 'm399')).toBe('Model 399')
    })

    // A revision bump makes every reader of the labels compute again, so an identical push must not
    // bump it. Each agent status update pushes the whole catalog again.
    it('keeps its revision for an identical push', () => {
      clearSettingsLabelCache()
      updateSettingsLabelCache(CURSOR, [models(400)])
      const revision = settingsLabelCacheRevision()
      updateSettingsLabelCache(CURSOR, [models(400)])
      expect(settingsLabelCacheRevision()).toBe(revision)
    })

    it('keeps every group that the push offers, also past the group cap', () => {
      clearSettingsLabelCache()
      const groups = Array.from({ length: 70 }, (_, i) => group(`g${i}`, `Group ${i}`, [[`o${i}`, `Option ${i}`]]))
      updateSettingsLabelCache(GOOSE, groups)
      expect(getCachedSettingsGroupLabel(GOOSE, 'g0')).toBe('Group 0')
      const revision = settingsLabelCacheRevision()
      updateSettingsLabelCache(GOOSE, groups)
      expect(settingsLabelCacheRevision()).toBe(revision)
    })
  })
})
