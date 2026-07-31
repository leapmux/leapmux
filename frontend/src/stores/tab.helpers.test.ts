import type { Tab } from './tab.types'
import type { AgentGitStatus, AgentInfo, AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentGitStatusSchema, AgentInfoSchema, AgentProvider, AgentStatus, AvailableOptionGroupSchema, AvailableOptionSchema } from '~/generated/leapmux/v1/agent_pb'
import { TerminalInfoSchema, TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { clearSettingsLabelCache, getCachedSettingsGroupLabel } from '~/lib/settingsLabelCache'
import { agentTabToInfo, deriveOptionGroupTabFields, isSameRepo, isTabReadyForGitStatus, openedTerminalMetadata, protoToAgentTabFields, resolveOptimisticGitInfo, setOptionValue, tabDisplayLabel, terminalMetadata, toAgentGitTabFields, toGitTabFields } from './tab.helpers'
import { createTabMetadataStore } from './tabMetadata.store'

// `tabDisplayLabel` is the shared "what should we render in the tab strip
// AND in the workspace tree?" helper. Three call sites depend on its
// fallback order (title → FILE basename → type-default), so each branch
// gets its own test to guard against silent drift.
function file(overrides: Partial<Extract<Tab, { type: TabType.FILE }>> = {}): Tab {
  return { type: TabType.FILE, id: 'f1', workspaceId: 'ws-1', ...overrides }
}

function agent(overrides: Partial<Extract<Tab, { type: TabType.AGENT }>> = {}): Tab {
  return { type: TabType.AGENT, id: 'a1', workspaceId: 'ws-1', ...overrides }
}

function terminal(overrides: Partial<Extract<Tab, { type: TabType.TERMINAL }>> = {}): Tab {
  return { type: TabType.TERMINAL, id: 't1', workspaceId: 'ws-1', ...overrides }
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
// both AppShell branch-changed call sites -- the singleton-refresh
// decision and the metadata branch stamp;
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

  it('rejects an empty workerId instead of matching every unresolved tab', () => {
    // A freshly-created tab may not have a workerId yet, and `?? ''` would
    // then make an empty QUERY match every one of them regardless of worker —
    // the symmetric half of the empty-toplevel wildcard below. The guard used
    // to live at one call site (`stampBranchOnTabs`), so the predicate itself
    // answered `('' === '')` -> true and every other caller was on its own.
    expect(isSameRepo({ gitToplevel: '/repo' }, '', '/repo')).toBe(false)
    expect(isSameRepo({ workerId: '', gitToplevel: '/repo' }, '', '/repo')).toBe(false)
    expect(isSameRepo({ gitToplevel: '/repo' }, 'w1', '/repo')).toBe(false)
  })

  // Regression guard, and the reason an empty `repoToplevel` is rejected
  // outright: `?? ''` normalization would otherwise make the empty query a
  // WILDCARD over every tab whose git identity hasn't resolved yet. A branch
  // change on one un-stamped repo would then re-label tabs in a sibling
  // un-stamped repo on the same worker — and since the stamp now spans every
  // workspace rather than just the active one, across the whole account.
  it('never matches an empty repoToplevel, even against an unresolved tab', () => {
    expect(isSameRepo({ workerId: 'w1' }, 'w1', '')).toBe(false)
    expect(isSameRepo({ workerId: 'w1', gitToplevel: '' }, 'w1', '')).toBe(false)
    expect(isSameRepo({ workerId: 'w1' }, 'w1', '/repo')).toBe(false)
  })

  it('rejects an empty repoToplevel before the workerId comparison', () => {
    // Not reachable via a workerId mismatch — the guard has to fire on its
    // own, or a same-worker query would still leak.
    expect(isSameRepo({ workerId: 'w1', gitToplevel: '/repo' }, 'w1', '')).toBe(false)
  })

  it('returns false for null / undefined input', () => {
    expect(isSameRepo(null, 'w1', '/repo')).toBe(false)
    expect(isSameRepo(undefined, 'w1', '/repo')).toBe(false)
  })

  it('returns false when only one side is unset (no accidental empty-empty matches)', () => {
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
      workspaceId: 'ws-1',
      id: 'a1',
      workerId: 'w1',
      gitToplevel: '/repo',
    }
    expect(isSameRepo(tab, 'w1', '/repo')).toBe(true)
  })
})

// `toGitTabFields` carries the four-field git tuple
// (branch + originUrl + toplevel + isWorktree) onto every tab. The
// disposition was added when wires were already in place for the other
// three; pin its inclusion so a future "factor out branch+origin" refactor
// can't quietly drop it.
describe('toGitTabFields', () => {
  it('answers undefined when the probe returned nothing at all', () => {
    // All four at proto zero is the worker saying "I could not tell you"
    // (`gitutil.GetGitStatus` yields nil when both porcelain probes fail).
    // Emitting `isWorktree: false` there asserts a negative the worker never
    // made, and the tab loses a worktree disposition it can never re-probe.
    expect(toGitTabFields('', '', '', false)).toBeUndefined()
    expect(toGitTabFields('', '', '', true)).toBeUndefined()
  })

  // Regression: these three used to collapse to `undefined` while
  // `gitIsWorktree` did not. `tabMetadata.patch` SKIPS undefined, so a
  // collapsed field cannot CLEAR a populated one -- the write is dropped, the
  // value never changes, and the next status event repeats that forever. A repo that loses its remote kept the dead origin for the life of
  // the page, and the sidebar kept grouping the tab under it.
  it('emits an empty string, not undefined, for a field the repo has lost', () => {
    expect(toGitTabFields('main', '', '/repo', false)).toEqual({
      gitBranch: 'main',
      gitOriginUrl: '',
      gitToplevel: '/repo',
      gitIsWorktree: false,
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

  /**
   * `false` must survive as `false`. `tabMetadata.patch` SKIPS undefined
   * values, so collapsing the negative case means a tab that stops being a
   * worktree can never be told: the patch drops the field, the value never
   * changes, and every subsequent event re-sends it — a write loop that never
   * converges, with the sidebar stuck showing the worktree badge.
   */
  it('keeps isWorktree=false as a real false, so a tab can stop being a worktree', () => {
    expect(toGitTabFields('main', '', '/repo', false)!.gitIsWorktree).toBe(false)
  })

  it('converges: a true -> false transition actually lands through patch', () => {
    const metadata = createTabMetadataStore()
    metadata.patch('t1', toGitTabFields('main', '', '/repo', true)!)
    expect(metadata.get('t1')?.gitIsWorktree).toBe(true)

    const next = toGitTabFields('main', '', '/repo', false)!
    metadata.patch('t1', next)
    expect(metadata.get('t1')?.gitIsWorktree, 'the write actually landed').toBe(false)

    const settled = metadata.get('t1')!
    metadata.patch('t1', next)
    expect(metadata.get('t1'), 'and re-sending it is a no-op - the loop converges').toBe(settled)
  })
})

/**
 * The producer of an agent tab's whole git group. Its reference-reuse rule is
 * load-bearing rather than an optimization: the worker recomputes and re-ships
 * the full status on every push, so an unchanged repo arrives as an
 * equal-but-FRESH proto. `Tab` is compared with `shallowEqual` (per-key
 * `Object.is`) and consumers iterate with `<For>`, which keys rows by item
 * identity -- so writing that fresh object back re-keys the tab and tears down
 * every row rendered from it.
 */
describe('toAgentGitTabFields', () => {
  // Only the fields these cases vary; `Partial<AgentGitStatus>` would drag in
  // `$typeName`, which `create`'s init shape rejects.
  const status = (over: { ahead?: number, conflicted?: boolean } = {}) =>
    create(AgentGitStatusSchema, { branch: 'main', toplevel: '/repo', originUrl: 'o', ahead: 2, modified: true, ...over })

  it('reports nothing at all when the push carried no status', () => {
    expect(toAgentGitTabFields(undefined)).toEqual({})
  })

  it('carries the proto plus the four flat mirrors as one group', () => {
    const gs = status()
    expect(toAgentGitTabFields(gs)).toEqual({
      agentGitStatus: gs,
      gitBranch: 'main',
      gitOriginUrl: 'o',
      gitToplevel: '/repo',
      gitIsWorktree: false,
    })
  })

  // Reference reuse is NOT this function's job -- it reports the status it was
  // given, every time, and `tabMetadata.patch` drops the write when the stored
  // value is already equal (see `sameStoredValue`, and the store's own tests for
  // the dedupe). Keeping the producer unconditional is what lets every other
  // producer of an object-valued field get the same treatment for free.
  it('reports the status unconditionally, leaving the dedupe to the write point', () => {
    const gs = status()
    expect(toAgentGitTabFields(gs).agentGitStatus).toBe(gs)
  })
})

describe('protoToAgentTabFields git status', () => {
  const agent = (gs: AgentGitStatus | undefined) =>
    create(AgentInfoSchema, { id: 'a1', workingDir: '/repo', agentProvider: AgentProvider.CLAUDE_CODE, gitStatus: gs })
  const status = () => create(AgentGitStatusSchema, { branch: 'main', toplevel: '/repo' })

  it('routes the git group through the shared producer', () => {
    const gs = status()
    const fields = protoToAgentTabFields('wkr-1', agent(gs))
    expect(fields.agentGitStatus).toBe(gs)
    expect(fields.gitBranch).toBe('main')
    expect(fields.gitToplevel).toBe('/repo')
  })

  it('reports no git group at all for an agent with no status', () => {
    expect(protoToAgentTabFields('wkr-1', agent(undefined)).agentGitStatus).toBeUndefined()
  })

  it('leaves the rest of the payload alone', () => {
    const fields = protoToAgentTabFields('wkr-1', agent(status()))
    expect(fields.workerId).toBe('wkr-1')
    expect(fields.workingDir).toBe('/repo')
  })
})

/**
 * The four git fields must LAND through `metadata.patch`, and must stop
 * re-writing once they have.
 *
 * These used to drive `gitTabFieldsDiffer`, a per-producer comparator the
 * terminal path consulted before patching. That rule now lives at the single
 * write point (`sameStoredValue`), so the behaviour is asserted where it is
 * actually enforced -- a producer-side test would have kept passing while the
 * producer stopped being consulted.
 */
describe('git tab fields through patch', () => {
  const base = { gitBranch: 'main', gitOriginUrl: 'o', gitToplevel: '/r', gitIsWorktree: false }

  function stamped(fields: Partial<typeof base>) {
    const metadata = createTabMetadataStore()
    metadata.patch('t1', base)
    const before = metadata.get('t1')!
    metadata.patch('t1', { ...base, ...fields })
    return { metadata, before, after: metadata.get('t1')! }
  }

  it('does not re-write the row for an identical tuple', () => {
    const { before, after } = stamped({})
    // Same ROW OBJECT: a fresh one would re-key the tab and remount every
    // `<For>` row rendered from it.
    expect(after).toBe(before)
  })

  it('lands a branch change', () => {
    expect(stamped({ gitBranch: 'feature' }).after.gitBranch).toBe('feature')
  })

  it('lands an originUrl change', () => {
    expect(stamped({ gitOriginUrl: 'other' }).after.gitOriginUrl).toBe('other')
  })

  it('lands a toplevel change', () => {
    expect(stamped({ gitToplevel: '/other' }).after.gitToplevel).toBe('/other')
  })

  it('lands an isWorktree change (false -> true)', () => {
    // Regression guard for the isWorktree plumbing: if a worker re-probes and
    // reports the path as a linked worktree where it previously wasn't (or vice
    // versa), the tab MUST update -- the sidebar's BranchGroup.isWorktree
    // disposition is derived from it and ChangeBranchDialog reads that to seed
    // its path-info shape.
    expect(stamped({ gitIsWorktree: true }).after.gitIsWorktree).toBe(true)
  })

  it('converges: a true -> false transition lands once and then stops', () => {
    const metadata = createTabMetadataStore()
    metadata.patch('t1', toGitTabFields('main', '', '/repo', true)!)
    expect(metadata.get('t1')?.gitIsWorktree).toBe(true)

    const next = toGitTabFields('main', '', '/repo', false)!
    metadata.patch('t1', next)
    expect(metadata.get('t1')?.gitIsWorktree, 'the write actually landed').toBe(false)

    const settled = metadata.get('t1')!
    metadata.patch('t1', next)
    expect(metadata.get('t1'), 'and re-sending it changes nothing').toBe(settled)
  })
})

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

// Ported from the deleted `tab.store.test.ts`. `isTabReadyForGitStatus` never
// belonged to the store -- it is a pure predicate over a tab plus its agent
// record -- and it is still live in AppShell, gating the git-status refresh.
// It lost every one of its tests when the store was deleted.
describe('isTabReadyForGitStatus', () => {
  // Minimal agent fixture; only the three fields the helper reads matter.
  function agent(p: Partial<Pick<AgentInfo, 'status' | 'startupMessage' | 'gitStatus'>>): AgentInfo {
    return {
      status: AgentStatus.STARTING,
      startupMessage: '',
      gitStatus: undefined,
      ...p,
    } as AgentInfo
  }

  const agentTab: Tab = { type: TabType.AGENT, id: 'a1', workspaceId: 'ws-1' }

  it('treats file tabs as always ready', () => {
    const fileTab: Tab = { type: TabType.FILE, id: 'f1', workspaceId: 'ws-1' }
    expect(isTabReadyForGitStatus(fileTab, null)).toBe(true)
  })

  it('treats a non-STARTING terminal tab as ready', () => {
    const ready: Tab = { type: TabType.TERMINAL, id: 't1', workspaceId: 'ws-1', status: TerminalStatus.READY }
    const exited: Tab = { type: TabType.TERMINAL, id: 't1', workspaceId: 'ws-1', status: TerminalStatus.EXITED }
    const failed: Tab = { type: TabType.TERMINAL, id: 't1', workspaceId: 'ws-1', status: TerminalStatus.STARTUP_FAILED }
    const undef: Tab = { type: TabType.TERMINAL, id: 't1', workspaceId: 'ws-1' }
    expect(isTabReadyForGitStatus(ready, null)).toBe(true)
    expect(isTabReadyForGitStatus(exited, null)).toBe(true)
    expect(isTabReadyForGitStatus(failed, null)).toBe(true)
    expect(isTabReadyForGitStatus(undef, null)).toBe(true)
  })

  it('defers a fresh STARTING terminal tab with no startupMessage', () => {
    // OpenTerminal's response leaves the tab in STARTING with no
    // phase-0 broadcast yet — same race window as agents.
    const tab: Tab = { type: TabType.TERMINAL, id: 't1', workspaceId: 'ws-1', status: TerminalStatus.STARTING }
    expect(isTabReadyForGitStatus(tab, null)).toBe(false)
  })

  it('defers a STARTING terminal tab even with a phase-0 startupMessage', () => {
    // The "Creating worktree …" label is broadcast BEFORE executeGitMode
    // runs (terminal.go runTerminalPhase0), so a non-empty startupMessage
    // is not proof that the worktree is on disk. Defer until the tab
    // leaves STARTING entirely.
    const tab: Tab = {
      type: TabType.TERMINAL,
      id: 't1',
      workspaceId: 'ws-1',
      status: TerminalStatus.STARTING,
      startupMessage: 'Creating worktree "feature"…',
    }
    expect(isTabReadyForGitStatus(tab, null)).toBe(false)
  })

  it('treats null/undefined tab as ready', () => {
    expect(isTabReadyForGitStatus(null, null)).toBe(true)
    expect(isTabReadyForGitStatus(undefined, null)).toBe(true)
  })

  it('treats an agent tab with no matching agent as ready', () => {
    expect(isTabReadyForGitStatus(agentTab, null)).toBe(true)
    expect(isTabReadyForGitStatus(agentTab, undefined)).toBe(true)
  })

  it('defers in the initial STARTING state — no startupMessage and no gitStatus', () => {
    // The window between OpenAgent's response and any broadcast.
    expect(
      isTabReadyForGitStatus(
        agentTab,
        agent({ status: AgentStatus.STARTING, startupMessage: '', gitStatus: undefined }),
      ),
    ).toBe(false)
  })

  it('defers a STARTING agent with a phase-0 startupMessage', () => {
    // Phase 0 broadcasts "Creating worktree …" BEFORE executeGitMode
    // runs, so a non-empty startupMessage means nothing about disk state.
    expect(
      isTabReadyForGitStatus(
        agentTab,
        agent({ status: AgentStatus.STARTING, startupMessage: 'Creating worktree "feature"…' }),
      ),
    ).toBe(false)
  })

  it('defers a STARTING agent in the phase-1 window', () => {
    expect(
      isTabReadyForGitStatus(
        agentTab,
        agent({ status: AgentStatus.STARTING, startupMessage: 'Checking Git status…' }),
      ),
    ).toBe(false)
  })

  it('defers a STARTING agent in the phase-2 window even with gitStatus set', () => {
    // gitStatus arrives at the start of phase 2, before the worktree is
    // reliably observable to a separate process — see the helper docstring.
    expect(
      isTabReadyForGitStatus(
        agentTab,
        agent({
          status: AgentStatus.STARTING,
          startupMessage: 'Starting Claude Code…',
          gitStatus: { branch: 'main' } as AgentInfo['gitStatus'],
        }),
      ),
    ).toBe(false)
  })

  it('is ready in any non-STARTING state regardless of message/gitStatus', () => {
    for (const status of [AgentStatus.ACTIVE, AgentStatus.INACTIVE, AgentStatus.STARTUP_FAILED]) {
      expect(
        isTabReadyForGitStatus(
          agentTab,
          agent({ status, startupMessage: '', gitStatus: undefined }),
        ),
      ).toBe(true)
    }
  })
})

/**
 * Every producer of the git/startup fields must write REAL negatives.
 * `tabMetadata.patch` skips `undefined`, and these producers run a SECOND time
 * over a populated row -- `useTabHydrators` re-arms on DISCONNECTED, which the
 * worker-offline sweep sets -- so a collapsed negative can never be delivered.
 */
describe('terminalMetadata convergence', () => {
  it('delivers a false gitIsWorktree and empty startup fields on a re-hydration', () => {
    const metadata = createTabMetadataStore()
    metadata.patch('t1', { gitIsWorktree: true, startupMessage: 'Creating worktree...' })

    metadata.patch('t1', terminalMetadata('w1', create(TerminalInfoSchema, {
      terminalId: 't1',
      status: TerminalStatus.READY,
      // A REAL negative carries a toplevel: the dir is still a git repo, it
      // just stopped being a worktree. Without one this fixture would be the
      // failed-probe shape below, which must behave the opposite way.
      gitToplevel: '/repo',
      gitIsWorktree: false,
    })))

    expect(metadata.get('t1')?.gitIsWorktree, 'the tab stopped being a worktree').toBe(false)
    expect(metadata.get('t1')?.startupMessage, 'the phase label must not outlive the phase').toBe('')
  })

  // The other half of the same rule. `gitIsWorktree` is the one field whose
  // negative is indistinguishable from "no answer": the worker leaves all four
  // git fields at proto zero when the probe returns nothing (gitutil yields nil
  // when both porcelain probes fail, and the caller only assigns `if gs != nil`).
  // Writing a bare `false` there while branch/origin/toplevel collapse to
  // undefined and are SKIPPED splits a pair that has to travel together -- the
  // tab keeps its branch but loses its worktree disposition, so the sidebar
  // regroups it under the non-worktree branch row and ChangeBranchDialog offers
  // an in-place checkout on a worktree, with nothing left to re-probe it.
  it('preserves the worktree disposition when the git probe returned nothing', () => {
    const metadata = createTabMetadataStore()
    metadata.patch('t1', { gitBranch: 'feature', gitToplevel: '/repo', gitIsWorktree: true })

    metadata.patch('t1', terminalMetadata('w1', create(TerminalInfoSchema, {
      terminalId: 't1',
      status: TerminalStatus.READY,
      // All four git fields at proto zero -- a failed probe, not an answer.
    })))

    expect(metadata.get('t1')?.gitIsWorktree, 'a failed probe must not assert "not a worktree"').toBe(true)
    expect(metadata.get('t1')?.gitBranch, 'the branch it could not re-probe survives').toBe('feature')
    expect(metadata.get('t1')?.gitToplevel, 'and so does the toplevel it travels with').toBe('/repo')
  })
})

describe('resolveOptimisticGitInfo', () => {
  // The seed is a FALLBACK, spread AFTER the worker's own fields. Object spread
  // copies `undefined`-valued OWN keys, so a key present-but-undefined does not
  // "leave the value alone" -- it ERASES it.
  it('omits keys it has no value for, so it cannot subtract from the payload', () => {
    const active = agent({ gitOriginUrl: 'o', gitToplevel: '/r', workingDir: '/r' })
    const seed = resolveOptimisticGitInfo(active, { workingDir: '/r' })

    expect('gitBranch' in seed, 'an unresolved branch must not be spread as undefined').toBe(false)
    expect({ gitBranch: 'main', ...seed }.gitBranch, 'the authoritative value survives').toBe('main')
  })

  it('still seeds the values it does have', () => {
    const seed = resolveOptimisticGitInfo(
      agent({ gitBranch: 'feature', gitOriginUrl: 'o', gitToplevel: '/r', workingDir: '/r' }),
      { workingDir: '/r' },
    )
    expect(seed.gitBranch).toBe('feature')
    expect(seed.gitToplevel).toBe('/r')
  })
})

/**
 * The seed for a terminal this client just opened. Both open paths (the tab-bar
 * button and the two dialogs) go through here so they cannot disagree about the
 * same moment in a terminal's life — they did, and the dialog path was wrong.
 */
describe('openedTerminalMetadata', () => {
  // A READY seed is not merely optimistic, it is STICKY: applyTerminalStatusChange's
  // STARTING arm refuses to move a tab that already reads READY, so every phase
  // label is dropped AND the later READY arm no-ops too. With `hydrated` set,
  // nothing re-asks. STARTING is self-correcting on all of those paths.
  it('seeds STARTING so a later phase broadcast is not blocked', () => {
    expect(openedTerminalMetadata({ title: 'Terminal Ada', workingDir: '/w' }).terminalStatus)
      .toBe(TerminalStatus.STARTING)
  })

  it('marks the tab hydrated, because the OpenTerminal response IS the answer', () => {
    expect(openedTerminalMetadata({ title: 't', workingDir: '/w' }).hydrated).toBe(true)
  })

  // `effectiveGitDir` is `shellStartDir || workingDir`, so writing the key
  // unconditionally would change what the optimistic git seed resolves against
  // for the caller that does not track one.
  it('omits shellStartDir when the caller has none, and defaults it to workingDir when it does', () => {
    expect('shellStartDir' in openedTerminalMetadata({ title: 't', workingDir: '/w' })).toBe(false)
    expect(openedTerminalMetadata({ title: 't', workingDir: '/w', shellStartDir: '' }).shellStartDir).toBe('/w')
    expect(openedTerminalMetadata({ title: 't', workingDir: '/w', shellStartDir: '/s' }).shellStartDir).toBe('/s')
  })
})
