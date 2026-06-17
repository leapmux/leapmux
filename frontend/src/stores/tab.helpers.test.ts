import type { Tab } from './tab.types'
import type { AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentProvider, AvailableOptionGroupSchema, AvailableOptionSchema } from '~/generated/leapmux/v1/agent_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { clearSettingsLabelCache, getCachedSettingsGroupLabel } from '~/lib/settingsLabelCache'
import { agentTabToInfo, deriveOptionGroupTabFields, gitTabFieldsDiffer, isSameRepo, preserveNonEmptyGitFields, setOptionValue, spliceTabGitFields, tabDisplayLabel, toGitTabFields } from './tab.helpers'

// `tabDisplayLabel` is the shared "what should we render in the tab strip
// AND in the workspace tree?" helper. Three call sites depend on its
// fallback order (title → FILE basename → type-default), so each branch
// gets its own test to guard against silent drift.
function file(overrides: Partial<Extract<Tab, { type: TabType.FILE }>> = {}): Tab {
  return { type: TabType.FILE, id: 'f1', ...overrides }
}

function agent(overrides: Partial<Extract<Tab, { type: TabType.AGENT }>> = {}): Tab {
  return { type: TabType.AGENT, id: 'a1', ...overrides }
}

function terminal(overrides: Partial<Extract<Tab, { type: TabType.TERMINAL }>> = {}): Tab {
  return { type: TabType.TERMINAL, id: 't1', ...overrides }
}

describe('tabDisplayLabel', () => {
  it('prefers an explicit title over every fallback', () => {
    expect(tabDisplayLabel(file({ title: 'Renamed', filePath: '/repo/notes.txt' }))).toBe('Renamed')
    expect(tabDisplayLabel(agent({ title: 'My Agent' }))).toBe('My Agent')
    expect(tabDisplayLabel(terminal({ title: 'zsh' }))).toBe('zsh')
  })

  it('treats an empty-string title as no title (falls through to fallbacks)', () => {
    // Solid stores can briefly hold the empty string as a transitional
    // value; the helper must NOT show a blank label.
    expect(tabDisplayLabel(file({ title: '', filePath: '/repo/notes.txt' }))).toBe('notes.txt')
    expect(tabDisplayLabel(agent({ title: '' }))).toBe('Agent')
    expect(tabDisplayLabel(terminal({ title: '' }))).toBe('Terminal')
  })

  describe('file fallback', () => {
    it('uses basename(filePath) when no title is set', () => {
      expect(tabDisplayLabel(file({ filePath: '/repo/src/foo.ts' }))).toBe('foo.ts')
    })

    it('handles Windows-style paths', () => {
      expect(tabDisplayLabel(file({ filePath: 'C:\\users\\alice\\report.md' }))).toBe('report.md')
    })

    it('returns "File" when filePath is missing entirely', () => {
      // Pre-hydration projection — tab arrives without filePath. The
      // workspace tree must show *something*, not blank.
      expect(tabDisplayLabel(file({ filePath: undefined }))).toBe('File')
    })

    it('returns "File" when filePath is an empty string', () => {
      expect(tabDisplayLabel(file({ filePath: '' }))).toBe('File')
    })

    it('returns "File" when filePath is just a root separator (empty basename)', () => {
      // `basename('/')` returns '' (no segments after the root). The
      // helper's || 'File' fallback must catch that so we don't render
      // a blank label.
      expect(tabDisplayLabel(file({ filePath: '/' }))).toBe('File')
    })

    it('handles a bare filename with no separators', () => {
      expect(tabDisplayLabel(file({ filePath: 'standalone.md' }))).toBe('standalone.md')
    })
  })

  describe('agent / terminal fallback', () => {
    it('returns "Agent" for an unnamed agent tab', () => {
      expect(tabDisplayLabel(agent())).toBe('Agent')
    })

    it('returns "Terminal" for an unnamed terminal tab', () => {
      expect(tabDisplayLabel(terminal())).toBe('Terminal')
    })
  })
})

// `isSameRepo` is the single source of truth for matching a
// (workerId, repoToplevel) pair against a Tab-shaped value. It backs
// the AppShell branch-changed routing AND tabStore.stampBranchOnTabs;
// every behavior listed here represents a contract those callers rely on.
describe('isSameRepo', () => {
  it('matches when workerId and gitToplevel both equal', () => {
    expect(isSameRepo({ workerId: 'w1', gitToplevel: '/repo' }, 'w1', '/repo')).toBe(true)
  })

  it('rejects when workerId differs (cross-worker leakage guard)', () => {
    // A branch change on worker A must never trigger a stamp on a tab
    // hosted by worker B even if both happen to share a repo path.
    expect(isSameRepo({ workerId: 'wA', gitToplevel: '/repo' }, 'wB', '/repo')).toBe(false)
  })

  it('rejects when gitToplevel differs (cross-repo guard)', () => {
    expect(isSameRepo({ workerId: 'w1', gitToplevel: '/repo-a' }, 'w1', '/repo-b')).toBe(false)
  })

  it('treats undefined workerId as empty string', () => {
    // A freshly-created tab may not have a workerId yet. Without the
    // ?? '' normalization, `undefined === ''` would be false and an
    // "empty/empty" query would also fail — but neither case should
    // match anything meaningful.
    expect(isSameRepo({ gitToplevel: '/repo' }, '', '/repo')).toBe(true)
    expect(isSameRepo({ gitToplevel: '/repo' }, 'w1', '/repo')).toBe(false)
  })

  it('treats undefined gitToplevel as empty string', () => {
    // A tab outside any git repo has gitToplevel=undefined. It must
    // only match an explicit empty-string query, never an arbitrary
    // path.
    expect(isSameRepo({ workerId: 'w1' }, 'w1', '')).toBe(true)
    expect(isSameRepo({ workerId: 'w1' }, 'w1', '/repo')).toBe(false)
  })

  it('returns false for null / undefined input', () => {
    expect(isSameRepo(null, 'w1', '/repo')).toBe(false)
    expect(isSameRepo(undefined, 'w1', '/repo')).toBe(false)
  })

  it('returns false when only one side is unset (no accidental empty-empty matches)', () => {
    // Worth pinning: an unset tab paired with an unset query DOES match
    // by the helper's spec, but mismatched cases must not. The two-arg
    // identity rule is symmetric.
    expect(isSameRepo({ workerId: 'w1' }, '', '/repo')).toBe(false)
    expect(isSameRepo({ gitToplevel: '/repo' }, 'w1', '')).toBe(false)
  })

  it('does not perform substring matching on gitToplevel', () => {
    // Regression guard: `/repo` must not match `/repo-other` even
    // though one is a prefix of the other.
    expect(isSameRepo({ workerId: 'w1', gitToplevel: '/repo-other' }, 'w1', '/repo')).toBe(false)
    expect(isSameRepo({ workerId: 'w1', gitToplevel: '/repo' }, 'w1', '/repo-other')).toBe(false)
  })

  it('accepts a full Tab object (the common production call shape)', () => {
    const tab: Tab = {
      type: TabType.AGENT,
      id: 'a1',
      workerId: 'w1',
      gitToplevel: '/repo',
    }
    expect(isSameRepo(tab, 'w1', '/repo')).toBe(true)
  })
})

// `toGitTabFields` / `gitTabFieldsDiffer` carry the four-field git tuple
// (branch + originUrl + toplevel + isWorktree) onto every tab. The
// disposition was added when wires were already in place for the other
// three; pin its inclusion so a future "factor out branch+origin" refactor
// can't quietly drop it.
describe('toGitTabFields', () => {
  it('maps every input field, collapsing empty strings to undefined', () => {
    // Empty strings on the wire mean "not set"; the tab convention is
    // undefined so equality checks against tabs that never carried a
    // value don't churn on '' vs undefined.
    expect(toGitTabFields('', '', '', false)).toEqual({
      gitBranch: undefined,
      gitOriginUrl: undefined,
      gitToplevel: undefined,
      gitIsWorktree: undefined,
    })
  })

  it('carries every non-empty field through', () => {
    expect(toGitTabFields('main', 'https://example.com/r.git', '/repo', true)).toEqual({
      gitBranch: 'main',
      gitOriginUrl: 'https://example.com/r.git',
      gitToplevel: '/repo',
      gitIsWorktree: true,
    })
  })

  it('collapses isWorktree=false to undefined (proto default = "not a worktree")', () => {
    // A wire-default `false` is the most common case — keep it
    // undefined so the field doesn't surface as a meaningful "value
    // present" signal on tabs that haven't been resolved yet.
    expect(toGitTabFields('main', '', '/repo', false).gitIsWorktree).toBeUndefined()
  })
})

describe('gitTabFieldsDiffer', () => {
  const base = { gitBranch: 'main', gitOriginUrl: 'o', gitToplevel: '/r', gitIsWorktree: false }

  it('returns false for an identical tuple', () => {
    expect(gitTabFieldsDiffer(base, { ...base })).toBe(false)
  })

  it('detects a branch change', () => {
    expect(gitTabFieldsDiffer(base, { ...base, gitBranch: 'feature' })).toBe(true)
  })

  it('detects an originUrl change', () => {
    expect(gitTabFieldsDiffer(base, { ...base, gitOriginUrl: 'other' })).toBe(true)
  })

  it('detects a toplevel change', () => {
    expect(gitTabFieldsDiffer(base, { ...base, gitToplevel: '/other' })).toBe(true)
  })

  it('detects an isWorktree change (false → true)', () => {
    // Regression guard for the isWorktree plumbing: if a worker re-
    // probes and reports the path as a linked worktree where it
    // previously wasn't (or vice versa), the tab MUST update — the
    // sidebar's BranchGroup.isWorktree disposition is derived from
    // it and ChangeBranchDialog reads that to seed its path-info shape.
    expect(gitTabFieldsDiffer(base, { ...base, gitIsWorktree: true })).toBe(true)
  })

  it('treats undefined and false isWorktree as equal (no churn on proto-zero default)', () => {
    // Proto-zero `false` arrives from the wire as undefined in the tab
    // (toGitTabFields collapses), so the comparator must not flag a
    // false→undefined transition as a change — every refresh would
    // otherwise allocate a new tab object.
    expect(gitTabFieldsDiffer({ ...base, gitIsWorktree: undefined }, { ...base, gitIsWorktree: false })).toBe(false)
  })
})

describe('spliceTabGitFields', () => {
  const agentTab = (id: string, branch?: string): Tab =>
    ({ type: TabType.AGENT, id, gitBranch: branch }) as Tab

  it('returns the SAME array (no copy) when the matched tab git fields are unchanged', () => {
    const tabs = [agentTab('a', 'main')]
    const next = toGitTabFields('main', '', '', false)
    expect(spliceTabGitFields(tabs, t => t.id === 'a', next)).toBe(tabs)
  })

  it('returns the SAME array when no tab matches', () => {
    const tabs = [agentTab('a', 'main')]
    const next = toGitTabFields('feature', '', '', false)
    expect(spliceTabGitFields(tabs, t => t.id === 'missing', next)).toBe(tabs)
  })

  it('copy-on-writes only the matched tab when its git fields differ', () => {
    const tabs = [agentTab('a', 'main'), agentTab('b', 'main')]
    const next = toGitTabFields('feature', '', '', false)
    const out = spliceTabGitFields(tabs, t => t.id === 'b', next)
    expect(out).not.toBe(tabs) // fresh array
    expect(out[0]).toBe(tabs[0]) // untouched tab keeps its reference
    expect(out[1]).not.toBe(tabs[1]) // matched tab replaced
    expect(out[1].gitBranch).toBe('feature')
    expect(tabs[1].gitBranch).toBe('main') // original not mutated
  })
})

describe('preserveNonEmptyGitFields', () => {
  it('carries gitIsWorktree forward when fresh has no toplevel', () => {
    // A transient probe failure (or a partial proto from a different
    // code path) leaves `fresh.gitToplevel` unset; the preserve helper
    // restores BOTH `gitToplevel` and `gitIsWorktree` from the prior
    // snapshot since they're co-derived.
    const out = preserveNonEmptyGitFields<{
      gitBranch?: string
      gitToplevel?: string
      gitIsWorktree?: boolean
    }>(
      { gitBranch: 'main' },
      { gitBranch: 'main', gitOriginUrl: 'o', gitToplevel: '/r', gitIsWorktree: true },
    )
    expect(out.gitToplevel).toBe('/r')
    expect(out.gitIsWorktree).toBe(true)
  })

  it('lets fresh override gitIsWorktree when fresh has a toplevel', () => {
    // The preserve helper must NOT mask a legitimate update from a fresh
    // probe — if the worker now reports a non-worktree where it used
    // to report a worktree, the new value wins.
    const out = preserveNonEmptyGitFields(
      { gitToplevel: '/r', gitIsWorktree: false },
      { gitToplevel: '/r', gitIsWorktree: true },
    )
    expect(out.gitIsWorktree).toBe(false)
  })
})

// agentTabToInfo projects the optimistic per-tab settings onto the option-group
// catalog. The interesting case is a model switch: each model carries its own
// model-dependent groups (effort tiers + the extended-thinking label) in
// `subGroups`, and an in-flight switch must swap those in immediately rather
// than waiting for the worker's relaunch round-trip.
describe('agentTabToInfo model-dependent option groups', () => {
  function opt(id: string, name: string, subGroups: AvailableOptionGroup[] = []) {
    return create(AvailableOptionSchema, { id, name, subGroups })
  }
  function effortGroup(ids: string[], defaultValue: string) {
    return create(AvailableOptionGroupSchema, {
      id: 'effort',
      label: 'Effort',
      order: 20,
      mutable: true,
      defaultValue,
      options: ids.map(id => create(AvailableOptionSchema, { id, name: id })),
    })
  }
  function thinkingGroup(onLabel: string) {
    return create(AvailableOptionGroupSchema, {
      id: 'alwaysThinkingEnabled',
      label: 'Extended Thinking',
      order: 30,
      mutable: true,
      defaultValue: 'on',
      options: [opt('on', onLabel), opt('off', 'Off')],
    })
  }
  // Sonnet: high/max, adaptive thinking. Haiku: no effort, plain "On". Opus:
  // xhigh/ultracode, adaptive thinking.
  const sonnetSub = [effortGroup(['auto', 'high', 'max'], 'high'), thinkingGroup('Adaptive')]
  const haikuSub = [thinkingGroup('On')]
  const opusSub = [effortGroup(['auto', 'high', 'xhigh', 'ultracode', 'max'], 'high'), thinkingGroup('Adaptive')]

  function modelGroup(currentValue: string) {
    return create(AvailableOptionGroupSchema, {
      id: 'model',
      label: 'Model',
      order: 10,
      mutable: true,
      currentValue,
      defaultValue: 'sonnet',
      options: [opt('sonnet', 'Sonnet', sonnetSub), opt('haiku', 'Haiku', haikuSub), opt('opus[1m]', 'Opus', opusSub)],
    })
  }
  const permissionGroup = create(AvailableOptionGroupSchema, {
    id: 'permissionMode',
    label: 'Permission Mode',
    order: 90,
    mutable: true,
    currentValue: 'default',
    options: [opt('default', 'Default')],
  })

  // A confirmed catalog for a running agent on `model`: the top-level effort and
  // thinking groups reflect that model (matching what the worker broadcasts).
  function catalogFor(model: string): AvailableOptionGroup[] {
    const sub = model === 'haiku' ? haikuSub : model === 'opus[1m]' ? opusSub : sonnetSub
    return [modelGroup(model), ...sub, permissionGroup]
  }

  function infoGroups(overrides: Partial<Extract<Tab, { type: TabType.AGENT }>>): AvailableOptionGroup[] {
    const tab = agent({ agentProvider: 0, ...overrides }) as Tab
    return agentTabToInfo(tab)!.optionGroups
  }

  it('leaves the catalog untouched when no model switch is pending', () => {
    const base = catalogFor('sonnet')
    const groups = infoGroups({ optionValues: { model: 'sonnet' }, optionGroups: base })
    // Same array reference: identity stability keeps <For> from churning.
    expect(groups).toBe(base)
  })

  it('returns a stable array reference across reads during an in-flight model switch', () => {
    // The projection rebuilds the model-dependent groups while a switch is in flight; the cache
    // (keyed on the optionGroups + optionValues references, both stable until the worker confirms)
    // must hand back the SAME array on repeated reads so downstream <For>/memos don't churn.
    const base = catalogFor('sonnet')
    const tab = agent({ agentProvider: 0, optionValues: { model: 'opus[1m]' }, optionGroups: base }) as Tab
    const first = agentTabToInfo(tab)!.optionGroups
    const second = agentTabToInfo(tab)!.optionGroups
    expect(first).not.toBe(base) // it actually rebuilt (the switch is in flight)
    expect(second).toBe(first) // ...but the repeat read is served from the cache, not rebuilt
  })

  it('drops the effort group and relabels thinking when switching to Haiku', () => {
    const groups = infoGroups({ optionValues: { model: 'haiku' }, optionGroups: catalogFor('sonnet') })
    expect(groups.find(g => g.id === 'effort')).toBeUndefined()
    const thinking = groups.find(g => g.id === 'alwaysThinkingEnabled')
    expect(thinking?.options.find(o => o.id === 'on')?.name).toBe('On')
    // Order is preserved (model, thinking, permission).
    expect(groups.map(g => g.id)).toEqual(['model', 'alwaysThinkingEnabled', 'permissionMode'])
  })

  it('surfaces opus-only effort tiers and adaptive thinking when switching to Opus', () => {
    const groups = infoGroups({ optionValues: { model: 'opus[1m]' }, optionGroups: catalogFor('sonnet') })
    const effort = groups.find(g => g.id === 'effort')
    expect(effort?.options.map(o => o.id)).toContain('xhigh')
    expect(effort?.options.map(o => o.id)).toContain('ultracode')
    expect(groups.find(g => g.id === 'alwaysThinkingEnabled')?.options.find(o => o.id === 'on')?.name).toBe('Adaptive')
  })

  it('rebuilds effort with the new model default so a stale tier falls back', () => {
    // On Opus with xhigh selected, switching to Sonnet (no xhigh) must present
    // Sonnet's effort options with default "high"; the panel's validity guard
    // then renders "high" since the carried-over xhigh is no longer offered.
    const groups = infoGroups({ optionValues: { model: 'sonnet', effort: 'xhigh' }, optionGroups: catalogFor('opus[1m]') })
    const effort = groups.find(g => g.id === 'effort')
    expect(effort?.options.map(o => o.id)).not.toContain('xhigh')
    expect(effort?.defaultValue).toBe('high')
  })

  it('keeps existing dependent groups when the optimistic model is not a listed option', () => {
    // A hidden/unknown model id lingering in optionValues must NOT strip effort
    // and thinking to nothing -- the catalog's dependent groups survive until a
    // real push arrives.
    const groups = infoGroups({ optionValues: { model: 'ghost-model' }, optionGroups: catalogFor('sonnet') })
    expect(groups.find(g => g.id === 'effort')).toBeDefined()
    expect(groups.find(g => g.id === 'alwaysThinkingEnabled')).toBeDefined()
  })
})

describe('deriveOptionGroupTabFields', () => {
  function group(id: string, currentValue: string): AvailableOptionGroup {
    return create(AvailableOptionGroupSchema, {
      id,
      options: [create(AvailableOptionSchema, { id: currentValue, name: currentValue })],
      currentValue,
    })
  }

  it('maps every group currentValue into optionValues by id, with no axis special-cased', () => {
    const groups = [
      group('model', 'sonnet'),
      group('effort', 'high'),
      group('permissionMode', 'plan'),
      group('sandbox_policy', 'workspace-write'), // a non-well-known provider extra
    ]
    const fields = deriveOptionGroupTabFields(groups)
    // The well-known axes AND the provider extra all land in the one generic map,
    // keyed by group id -- proving the derive does no per-axis branching.
    expect(fields.optionValues).toEqual({
      model: 'sonnet',
      effort: 'high',
      permissionMode: 'plan',
      sandbox_policy: 'workspace-write',
    })
    expect(fields.optionGroups).toBe(groups)
  })

  it('omits empty current values and returns {} for an empty catalog', () => {
    expect(deriveOptionGroupTabFields([])).toEqual({})
    const fields = deriveOptionGroupTabFields([group('model', 'sonnet'), group('effort', '')])
    expect(fields.optionValues).toEqual({ model: 'sonnet' })
  })

  it('is pure: does not prime the settings-label cache', () => {
    clearSettingsLabelCache()
    const labelled = create(AvailableOptionGroupSchema, {
      id: 'model',
      label: 'Model',
      options: [create(AvailableOptionSchema, { id: 'sonnet', name: 'Sonnet' })],
      currentValue: 'sonnet',
    })
    deriveOptionGroupTabFields([labelled])
    // Priming the label cache is the caller's job (protoToAgentTabFields / the
    // statusChange handler), so the converter writes nothing -- it stays referentially
    // transparent and testable without cache cleanup.
    expect(getCachedSettingsGroupLabel(AgentProvider.CLAUDE_CODE, 'model')).toBeUndefined()
  })
})

describe('setOptionValue', () => {
  it('sets a non-empty value and preserves other axes', () => {
    expect(setOptionValue({ model: 'sonnet' }, 'effort', 'high')).toEqual({ model: 'sonnet', effort: 'high' })
  })

  it('deletes the key for an empty value rather than storing an empty-string override', () => {
    expect(setOptionValue({ model: 'sonnet', effort: 'high' }, 'effort', '')).toEqual({ model: 'sonnet' })
  })

  it('returns a fresh map (does not mutate the input) and tolerates undefined', () => {
    const input = { model: 'sonnet' }
    const out = setOptionValue(input, 'effort', 'high')
    expect(out).not.toBe(input)
    expect(input).toEqual({ model: 'sonnet' })
    expect(setOptionValue(undefined, 'model', 'sonnet')).toEqual({ model: 'sonnet' })
  })
})
