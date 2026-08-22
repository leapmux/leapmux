import type { AgentTab, Tab } from './tab.types'
import type { AgentInfo, AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import type { GitRepoStatus } from '~/generated/leapmux/v1/common_pb'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { registerProvider } from '~/components/chat/providers/registry'
import { AgentInfoSchema, AgentProvider, AgentStatus, AvailableOptionGroupSchema, AvailableOptionSchema } from '~/generated/leapmux/v1/agent_pb'
import { GitRepoStatusSchema } from '~/generated/leapmux/v1/common_pb'
import { TerminalInfoSchema, TerminalProgress_State, TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { clearSettingsLabelCache, getCachedSettingsGroupLabel } from '~/lib/settingsLabelCache'
import { agentTabToInfo, deriveOptionGroupTabFields, descendantAgentTabs, isSameRepo, isSteerableAgentTab, isTabReadyForGitStatus, mruSteerableAgentTab, openedTerminalMetadata, protoToAgentTabFields, resolveOptimisticGitInfo, rootAgentIdFor, setOptionValue, tabDisplayLabel, tabTooltipShowWhen, tabTooltipText, terminalMetadata, terminalProgressBarProps } from './tab.helpers'
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

  it('falls back to ptyTitle when the terminal has no title', () => {
    expect(tabDisplayLabel(terminal({ title: '', ptyTitle: 'shell' }))).toBe('shell')
  })

  it('prefers an explicit title over ptyTitle (user rename sticks across OSC)', () => {
    expect(tabDisplayLabel(terminal({ title: 'My Shell', ptyTitle: 'live' }))).toBe('My Shell')
  })

  it('tabTooltipText prefers ptyTitle for terminals', () => {
    expect(tabTooltipText(terminal({ title: 'My Shell', ptyTitle: 'live' }))).toBe('live')
    expect(tabTooltipText(terminal({ title: 'My Shell', ptyTitle: '' }))).toBe('My Shell')
    expect(tabTooltipText(agent({ title: 'Agent A' }))).toBe('Agent A')
  })

  /**
   * `clipped` is for a tooltip that REPEATS its label; it also withholds the
   * text from a screen reader. A terminal's live OSC title is text the label
   * never shows, so gating it on the label happening to overflow hid it
   * outright.
   */
  it('tabTooltipShowWhen is always only when the tooltip differs from the label', () => {
    // A live PTY title behind a user rename: the tooltip carries text the
    // label does not, so clip detection must not gate it.
    expect(tabTooltipShowWhen(terminal({ title: 'My Shell', ptyTitle: 'vim src/app.ts' }))).toBe('always')
    // No PTY title: the tooltip repeats the label, so `clipped` is correct.
    expect(tabTooltipShowWhen(terminal({ title: 'My Shell', ptyTitle: '' }))).toBe('clipped')
    // A PTY title that happens to equal the label repeats it too.
    expect(tabTooltipShowWhen(terminal({ title: 'live', ptyTitle: 'live' }))).toBe('clipped')
    // Non-terminal tabs never carry a second string.
    expect(tabTooltipShowWhen(agent({ title: 'Agent A' }))).toBe('clipped')
    expect(tabTooltipShowWhen(file({ title: 'Renamed', filePath: '/repo/notes.txt' }))).toBe('clipped')
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
    expect(isSameRepo({ workerId: 'w1', gitToplevel: '/repo' }, { workerId: 'w1', gitToplevel: '/repo' })).toBe(true)
  })

  it('rejects when workerId differs (cross-worker leakage guard)', () => {
    // A branch change on worker A must never trigger a stamp on a tab
    // hosted by worker B even if both happen to share a repo path.
    expect(isSameRepo({ workerId: 'wA', gitToplevel: '/repo' }, { workerId: 'wB', gitToplevel: '/repo' })).toBe(false)
  })

  it('rejects when gitToplevel differs (cross-repo guard)', () => {
    expect(isSameRepo({ workerId: 'w1', gitToplevel: '/repo-a' }, { workerId: 'w1', gitToplevel: '/repo-b' })).toBe(false)
  })

  it('rejects an empty workerId instead of matching every unresolved tab', () => {
    // A freshly-created tab may not have a workerId yet, and `?? ''` would
    // then make an empty QUERY match every one of them regardless of worker —
    // the symmetric half of the empty-toplevel wildcard below. The guard used
    // to live at one call site (`stampBranchOnTabs`), so the predicate itself
    // answered `('' === '')` -> true and every other caller was on its own.
    expect(isSameRepo({ gitToplevel: '/repo' }, { workerId: '', gitToplevel: '/repo' })).toBe(false)
    expect(isSameRepo({ workerId: '', gitToplevel: '/repo' }, { workerId: '', gitToplevel: '/repo' })).toBe(false)
    expect(isSameRepo({ gitToplevel: '/repo' }, { workerId: 'w1', gitToplevel: '/repo' })).toBe(false)
  })

  // Regression guard, and the reason an empty `repoToplevel` is rejected
  // outright: `?? ''` normalization would otherwise make the empty query a
  // WILDCARD over every tab whose git identity hasn't resolved yet. A branch
  // change on one un-stamped repo would then re-label tabs in a sibling
  // un-stamped repo on the same worker — and since the stamp now spans every
  // workspace rather than just the active one, across the whole account.
  it('never matches an empty repoToplevel, even against an unresolved tab', () => {
    expect(isSameRepo({ workerId: 'w1' }, { workerId: 'w1', gitToplevel: '' })).toBe(false)
    expect(isSameRepo({ workerId: 'w1', gitToplevel: '' }, { workerId: 'w1', gitToplevel: '' })).toBe(false)
    expect(isSameRepo({ workerId: 'w1' }, { workerId: 'w1', gitToplevel: '/repo' })).toBe(false)
  })

  it('rejects an empty repoToplevel before the workerId comparison', () => {
    // Not reachable via a workerId mismatch — the guard has to fire on its
    // own, or a same-worker query would still leak.
    expect(isSameRepo({ workerId: 'w1', gitToplevel: '/repo' }, { workerId: 'w1', gitToplevel: '' })).toBe(false)
  })

  it('returns false for null / undefined input', () => {
    expect(isSameRepo(null, { workerId: 'w1', gitToplevel: '/repo' })).toBe(false)
    expect(isSameRepo(undefined, { workerId: 'w1', gitToplevel: '/repo' })).toBe(false)
  })

  it('returns false when only one side is unset (no accidental empty-empty matches)', () => {
    expect(isSameRepo({ workerId: 'w1' }, { workerId: '', gitToplevel: '/repo' })).toBe(false)
    expect(isSameRepo({ gitToplevel: '/repo' }, { workerId: 'w1', gitToplevel: '' })).toBe(false)
  })

  it('does not perform substring matching on gitToplevel', () => {
    // Regression guard: `/repo` must not match `/repo-other` even
    // though one is a prefix of the other.
    expect(isSameRepo({ workerId: 'w1', gitToplevel: '/repo-other' }, { workerId: 'w1', gitToplevel: '/repo' })).toBe(false)
    expect(isSameRepo({ workerId: 'w1', gitToplevel: '/repo' }, { workerId: 'w1', gitToplevel: '/repo-other' })).toBe(false)
  })

  it('accepts a full Tab object (the common production call shape)', () => {
    const tab: Tab = {
      type: TabType.AGENT,
      workspaceId: 'ws-1',
      id: 'a1',
      workerId: 'w1',
      gitToplevel: '/repo',
    }
    expect(isSameRepo(tab, { workerId: 'w1', gitToplevel: '/repo' })).toBe(true)
  })
})

describe('protoToAgentTabFields git status', () => {
  const agent = (gs: GitRepoStatus | undefined) =>
    create(AgentInfoSchema, { id: 'a1', workingDir: '/repo', agentProvider: AgentProvider.CLAUDE_CODE, gitStatus: gs })
  const status = () => create(GitRepoStatusSchema, { branch: 'main', toplevel: '/repo' })

  it('writes repo identity onto the tab when git status carries a toplevel', () => {
    const fields = protoToAgentTabFields('wkr-1', agent(status()))
    expect(fields.gitToplevel).toBe('/repo')
  })

  it('reports no git identity for an agent with no status', () => {
    expect(protoToAgentTabFields('wkr-1', agent(undefined)).gitToplevel).toBeUndefined()
  })

  it('leaves the rest of the payload alone', () => {
    const fields = protoToAgentTabFields('wkr-1', agent(status()))
    expect(fields.workerId).toBe('wkr-1')
    expect(fields.workingDir).toBe('/repo')
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

describe('agentTabToInfo subagent linkage', () => {
  it('passes parentAgentId and acceptsMessages through to AgentInfo', () => {
    const tab = agent({ parentAgentId: 'root-1', acceptsMessages: true }) as Tab
    const info = agentTabToInfo(tab)
    expect(info).toBeDefined()
    expect(info!.parentAgentId).toBe('root-1')
    expect(info!.acceptsMessages).toBe(true)
  })

  it('defaults parentAgentId to empty and acceptsMessages to false for a root agent', () => {
    const tab = agent() as Tab
    const info = agentTabToInfo(tab)
    expect(info).toBeDefined()
    expect(info!.parentAgentId).toBe('')
    expect(info!.acceptsMessages).toBe(false)
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
  it('clears startup fields on a re-hydration', () => {
    const metadata = createTabMetadataStore()
    metadata.patch('t1', { startupMessage: 'Creating worktree...' })

    metadata.patch('t1', terminalMetadata('w1', create(TerminalInfoSchema, {
      terminalId: 't1',
      status: TerminalStatus.READY,
      gitStatus: { toplevel: '/repo' },
    })))

    expect(metadata.get('t1')?.gitToplevel).toBe('/repo')
    expect(metadata.get('t1')?.startupMessage, 'the phase label must not outlive the phase').toBe('')
  })

  it('does not clear a known toplevel when the git probe returned nothing', () => {
    const metadata = createTabMetadataStore()
    metadata.patch('t1', { gitToplevel: '/repo' })

    metadata.patch('t1', terminalMetadata('w1', create(TerminalInfoSchema, {
      terminalId: 't1',
      status: TerminalStatus.READY,
    })))

    expect(metadata.get('t1')?.gitToplevel, 'a failed probe must not erase repo identity').toBe('/repo')
  })
})

describe('resolveOptimisticGitInfo', () => {
  it('omits keys it has no value for, so it cannot subtract from the payload', () => {
    const active = agent({ gitToplevel: '/r', workingDir: '/r' })
    const seed = resolveOptimisticGitInfo(active, { workingDir: '/r' })

    expect('gitToplevel' in seed).toBe(true)
    expect({ gitToplevel: '/other', ...seed }.gitToplevel, 'the authoritative value survives').toBe('/r')
  })

  it('still seeds the toplevel when the dirs match', () => {
    const seed = resolveOptimisticGitInfo(
      agent({ gitToplevel: '/r', workingDir: '/r' }),
      { workingDir: '/r' },
    )
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

describe('terminalProgressBarProps', () => {
  it('clamps percent into 0..100 and formats the CSS var', () => {
    expect(terminalProgressBarProps(terminal({ progressPercent: 150, progressState: TerminalProgress_State.NORMAL }))).toEqual({
      style: { '--progress-percent': '100%' },
      title: '100%',
    })
    expect(terminalProgressBarProps(terminal({ progressPercent: -5, progressState: TerminalProgress_State.NORMAL }))).toEqual({
      style: { '--progress-percent': '0%' },
      title: '0%',
    })
  })

  it('uses an indeterminate title when progress is indeterminate', () => {
    expect(terminalProgressBarProps(terminal({
      progressPercent: 40,
      progressState: TerminalProgress_State.INDETERMINATE,
    })).title).toBe('In progress')
  })
})

/**
 * `rootAgentIdFor` walks an agent's parentAgentId chain up to the root, with a
 * visited-set cycle guard. It keys the per-root background-task registry, and a
 * cycle (or an unbounded walk) would hang the registry lookup at startup.
 */
describe('rootAgentIdFor', () => {
  it('walks a parentAgentId chain up to the root id', () => {
    // grand <- parent <- child; each lookup returns the next tab up.
    const tabs = new Map<string, AgentTab>([
      ['child', { type: TabType.AGENT, id: 'child', workspaceId: 'ws-1', parentAgentId: 'parent' }],
      ['parent', { type: TabType.AGENT, id: 'parent', workspaceId: 'ws-1', parentAgentId: 'grand' }],
      ['grand', { type: TabType.AGENT, id: 'grand', workspaceId: 'ws-1' }], // root
    ])
    const get = (id: string) => tabs.get(id)

    expect(rootAgentIdFor(get, 'child')).toBe('grand')
    // Mid-chain start still reaches the same root.
    expect(rootAgentIdFor(get, 'parent')).toBe('grand')
  })

  it('returns the dangling parent id when the chain ends at an unknown parent', () => {
    // child points at a parent the hydration has not delivered yet. The impl
    // returns the LAST id it walked (the unresolvable parent), NOT the input
    // id -- the docstring's "falls back to agentId" wording describes the cycle
    // arm, not this one. The divergence is observationally harmless: the result
    // keys the background-task registry, and neither the dangling parent id nor
    // the input child id is a real root, so both miss the registry the same way.
    const tabs = new Map<string, AgentTab>([
      ['child', { type: TabType.AGENT, id: 'child', workspaceId: 'ws-1', parentAgentId: 'missing' }],
    ])
    expect(rootAgentIdFor((id: string) => tabs.get(id), 'child')).toBe('missing')
  })

  it('returns the input id when the tab has no parentAgentId (itself a root)', () => {
    const tabs = new Map<string, AgentTab>([
      ['root', { type: TabType.AGENT, id: 'root', workspaceId: 'ws-1' }],
    ])
    expect(rootAgentIdFor((id: string) => tabs.get(id), 'root')).toBe('root')
  })

  it('returns the input id when getAgentTab returns undefined for the starting id', () => {
    // A tab id the projection has no record of at all -- the cycle guard's
    // other early-out (no tab -> no parent -> return current).
    expect(rootAgentIdFor(() => undefined, 'ghost')).toBe('ghost')
  })

  it('terminates on a cycle and returns the input id (does not infinite-loop)', () => {
    // A -> B -> A: a corrupted/circular chain. The visited-set guard must
    // break the loop and fall back to the input rather than spin forever.
    const tabs = new Map<string, AgentTab>([
      ['A', { type: TabType.AGENT, id: 'A', workspaceId: 'ws-1', parentAgentId: 'B' }],
      ['B', { type: TabType.AGENT, id: 'B', workspaceId: 'ws-1', parentAgentId: 'A' }],
    ])
    expect(rootAgentIdFor((id: string) => tabs.get(id), 'A')).toBe('A')
  })

  it('breaks a longer cycle that returns to the start after several hops', () => {
    // A -> B -> C -> A: the guard catches the revisit on the fourth lookup.
    const tabs = new Map<string, AgentTab>([
      ['A', { type: TabType.AGENT, id: 'A', workspaceId: 'ws-1', parentAgentId: 'B' }],
      ['B', { type: TabType.AGENT, id: 'B', workspaceId: 'ws-1', parentAgentId: 'C' }],
      ['C', { type: TabType.AGENT, id: 'C', workspaceId: 'ws-1', parentAgentId: 'A' }],
    ])
    expect(rootAgentIdFor((id: string) => tabs.get(id), 'A')).toBe('A')
  })

  it('prefers the wire-provided rootAgentId over walking the chain', () => {
    // The backend resolves the root server-side and sends it on AgentInfo; the
    // frontend trusts it rather than re-deriving from a partially-hydrated chain.
    // A grandchild whose intermediate parent is unhydrated still resolves to the
    // real root via its own rootAgentId.
    const tabs = new Map<string, AgentTab>([
      ['gc', { type: TabType.AGENT, id: 'gc', workspaceId: 'ws-1', parentAgentId: 'c', rootAgentId: 'root-1' }],
    ])
    expect(rootAgentIdFor((id: string) => tabs.get(id), 'gc')).toBe('root-1')
  })
})

/**
 * `descendantAgentTabs` is what makes a subagent tab close with the tab that
 * spawned it. A child owns no process, so a subtree left behind is a set of
 * transcripts nothing can add to, hanging off a parent row that is gone.
 */
describe('descendantAgentTabs', () => {
  const agent = (id: string, parentAgentId?: string): Tab =>
    ({ type: TabType.AGENT, id, workspaceId: 'ws-1', parentAgentId } as Tab)
  const ids = (tabs: readonly Tab[], id: string) => descendantAgentTabs(tabs, id).map(t => t.id)

  it('returns nothing for an agent with no children', () => {
    expect(ids([agent('root'), agent('other')], 'root')).toEqual([])
  })

  it('returns nothing for an empty tab list', () => {
    expect(descendantAgentTabs([], 'root')).toEqual([])
  })

  it('returns every child of the agent', () => {
    const tabs = [agent('root'), agent('c1', 'root'), agent('c2', 'root')]
    expect(new Set(ids(tabs, 'root'))).toEqual(new Set(['c1', 'c2']))
  })

  // Deepest first, so each tab closes before the one that placed it.
  it('returns a grandchild before its own parent', () => {
    const tabs = [agent('root'), agent('c1', 'root'), agent('gc', 'c1')]
    expect(ids(tabs, 'root')).toEqual(['gc', 'c1'])
  })

  it('never includes the agent itself', () => {
    const tabs = [agent('root'), agent('c1', 'root')]
    expect(ids(tabs, 'root')).not.toContain('root')
  })

  it('returns only what is below the given agent, not a sibling branch', () => {
    const tabs = [agent('root'), agent('a'), agent('c1', 'root'), agent('c2', 'a')]
    expect(ids(tabs, 'root')).toEqual(['c1'])
  })

  // A child whose parent is not in the list is not this agent's descendant. It
  // stays open, matching what the sidebar draws for it -- a top-level row.
  it('ignores a child whose parent is absent from the list', () => {
    expect(ids([agent('root'), agent('orphan', 'gone')], 'root')).toEqual([])
  })

  // A TERMINAL and an AGENT tab can share an id, and only an AGENT is ever a
  // parent, so a non-agent tab must never be swept in as one.
  it('never returns a non-agent tab', () => {
    const tabs = [
      agent('root'),
      agent('c1', 'root'),
      { type: TabType.TERMINAL, id: 'c1', workspaceId: 'ws-1' } as Tab,
    ]
    const result = descendantAgentTabs(tabs, 'root')
    expect(result).toHaveLength(1)
    expect(result[0].type).toBe(TabType.AGENT)
  })

  // The worker cannot produce a cycle (parent_agent_id is a DAG rooted at a main
  // agent), but a walk that recursed forever would hang the close, so the guard
  // has to hold regardless.
  it('terminates on a cycle rather than recursing forever', () => {
    // root -> a -> b, and b claims a as its child again. The revisit of `a` is
    // what the visited set has to catch; without it the walk never returns.
    const tabs = [agent('root'), agent('a', 'root'), agent('b', 'a')]
    const cyclic = [...tabs, { type: TabType.AGENT, id: 'a', workspaceId: 'ws-1', parentAgentId: 'b' } as Tab]
    expect(ids(cyclic, 'root')).toEqual(['b', 'a'])
  })

  it('terminates when a tab names itself as its own parent', () => {
    // Asked about `self`, the walk finds `self` among its own children. The
    // visited set is seeded with the starting id, so it stops there.
    expect(ids([agent('self', 'self')], 'self')).toEqual([])
  })

  it('returns nothing for an id no tab carries', () => {
    expect(ids([agent('root'), agent('c1', 'root')], 'ghost')).toEqual([])
  })
})

/**
 * `isSteerableAgentTab` gates whether an agent tab's composer is enabled. Roots
 * always steer; children steer only when their provider can drive a subagent
 * conversation. Used to exclude non-steerable children from MRU-agent
 * resolution (mentions/quotes never target a read-only transcript).
 */
describe('isSteerableAgentTab', () => {
  it('returns false for a non-AGENT tab', () => {
    // The type guard short-circuits before any other field is consulted.
    expect(isSteerableAgentTab({ type: TabType.FILE, agentProvider: AgentProvider.CODEX })).toBe(false)
    expect(isSteerableAgentTab({ type: TabType.TERMINAL })).toBe(false)
  })

  it('returns true for a root agent tab (no parentAgentId)', () => {
    expect(isSteerableAgentTab({ type: TabType.AGENT })).toBe(true)
    expect(isSteerableAgentTab({ type: TabType.AGENT, acceptsMessages: false })).toBe(true)
  })

  it('returns true for a child with acceptsMessages === true (backend-authoritative)', () => {
    expect(
      isSteerableAgentTab({ type: TabType.AGENT, parentAgentId: 'root', acceptsMessages: true }),
    ).toBe(true)
  })

  it('returns false for a child with acceptsMessages === false (read-only transcript)', () => {
    expect(
      isSteerableAgentTab({ type: TabType.AGENT, parentAgentId: 'root', acceptsMessages: false }),
    ).toBe(false)
  })

  it('falls back to the provider for an unhydrated child: a provider with supportsSubagentSend is optimistically steerable', () => {
    // acceptsMessages is unset (optimistic state before hydration). The fallback
    // routes through the provider plugin's supportsSubagentSend so the single
    // source of truth is the plugin (Codex sets it true), not a hardcoded name.
    registerProvider(AgentProvider.CODEX, { classify: () => ({} as never), supportsSubagentSend: true })
    expect(
      isSteerableAgentTab({ type: TabType.AGENT, parentAgentId: 'root', agentProvider: AgentProvider.CODEX }),
    ).toBe(true)
  })

  it('returns false for an unhydrated child of a provider without supportsSubagentSend', () => {
    // Claude/ACP children do not steer; before hydration they are treated as
    // read-only rather than optimistically enabling a composer that may not work.
    registerProvider(AgentProvider.CLAUDE_CODE, { classify: () => ({} as never) })
    expect(
      isSteerableAgentTab({ type: TabType.AGENT, parentAgentId: 'root', agentProvider: AgentProvider.CLAUDE_CODE }),
    ).toBe(false)
    expect(
      isSteerableAgentTab({ type: TabType.AGENT, parentAgentId: 'root' }),
    ).toBe(false)
  })

  it('acceptsMessages wins over the provider fallback when both are present', () => {
    // A CODEX child that the backend says is NOT steerable must read false --
    // the authoritative flag overrides the optimistic provider default.
    expect(
      isSteerableAgentTab({ type: TabType.AGENT, parentAgentId: 'root', acceptsMessages: false, agentProvider: AgentProvider.CODEX }),
    ).toBe(false)
  })
})

describe('mruSteerableAgentTab', () => {
  it('returns the first steerable agent tab in MRU order', () => {
    const tabs: Tab[] = [
      file(),
      // A non-steerable child appears first in MRU but must be skipped.
      agent({ id: 'child', parentAgentId: 'root', acceptsMessages: false }),
      agent({ id: 'root', acceptsMessages: true }),
    ]
    expect(mruSteerableAgentTab(tabs)?.id).toBe('root')
  })

  it('returns undefined when no steerable agent tab is present', () => {
    const tabs: Tab[] = [
      file(),
      terminal(),
      agent({ id: 'child', parentAgentId: 'root', acceptsMessages: false }),
    ]
    expect(mruSteerableAgentTab(tabs)).toBeUndefined()
  })

  it('skips non-AGENT tabs even when they appear first', () => {
    const tabs: Tab[] = [
      file(),
      terminal(),
      agent({ id: 'root' }),
    ]
    expect(mruSteerableAgentTab(tabs)?.id).toBe('root')
  })
})
