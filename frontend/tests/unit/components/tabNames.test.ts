import type { Tab } from '~/stores/tab.store'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { pickTabName, TAB_NAMES } from '~/components/shell/tabNames'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'

function tab(overrides: Partial<Tab> & { type: TabType, id: string }): Tab {
  return { ...overrides }
}

describe('tab_names constant', () => {
  it('has exactly 256 entries', () => {
    expect(TAB_NAMES).toHaveLength(256)
  })

  it('contains no duplicates', () => {
    expect(new Set(TAB_NAMES).size).toBe(TAB_NAMES.length)
  })

  it('only contains capitalized ASCII tokens', () => {
    const re = /^[A-Z][A-Za-z]+$/
    for (const name of TAB_NAMES) {
      expect(name, name).toMatch(re)
    }
  })
})

describe('pickTabName', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('returns a name from TAB_NAMES when the tab list is empty', () => {
    const name = pickTabName([])
    expect(TAB_NAMES).toContain(name)
  })

  it('does not return a name already used by an Agent tab of the same type', () => {
    const used = TAB_NAMES[0]
    // Force Math.random to point at the used name first; pickTabName should still skip it.
    vi.spyOn(Math, 'random').mockReturnValue(0)
    const name = pickTabName([
      tab({ type: TabType.AGENT, id: 'a1', title: `Agent ${used}` }),
    ])
    expect(name).not.toBe(used)
  })

  it('does not return a name already used by a Terminal tab when picking for an Agent', () => {
    // Cross-type dedupe: a Terminal Olivia must block Agent Olivia.
    const used = TAB_NAMES[0]
    vi.spyOn(Math, 'random').mockReturnValue(0)
    const name = pickTabName([
      tab({ type: TabType.TERMINAL, id: 't1', title: `Terminal ${used}` }),
    ])
    expect(name).not.toBe(used)
  })

  it('does not return a name already used by a tab in a floating window', () => {
    // Floating-window tabs share the same array, distinguished only by tileId.
    const used = TAB_NAMES[0]
    vi.spyOn(Math, 'random').mockReturnValue(0)
    const name = pickTabName([
      tab({ type: TabType.AGENT, id: 'a1', title: `Agent ${used}`, tileId: 'floating-window-1' }),
    ])
    expect(name).not.toBe(used)
  })

  it('ignores tabs whose title does not match the auto-name regex', () => {
    // A user-renamed `My Olivia` should NOT block the picker from returning Olivia.
    const target = TAB_NAMES[0]
    vi.spyOn(Math, 'random').mockReturnValue(0)
    const name = pickTabName([
      tab({ type: TabType.AGENT, id: 'a1', title: `My ${target}` }),
      tab({ type: TabType.AGENT, id: 'a2', title: target }),
      tab({ type: TabType.AGENT, id: 'a3', title: 'Refactor auth' }),
      tab({ type: TabType.AGENT, id: 'a4', title: '' }),
      tab({ type: TabType.AGENT, id: 'a5' }),
    ])
    expect(name).toBe(target)
  })

  it('picks deterministically when Math.random is stubbed', () => {
    // available[Math.floor(0.5 * 256)] === TAB_NAMES[128]
    vi.spyOn(Math, 'random').mockReturnValue(0.5)
    expect(pickTabName([])).toBe(TAB_NAMES[128])
  })

  it('falls back to the full pool (allowing duplicates) when every name is in use', () => {
    const tabs = TAB_NAMES.map((n, i) => tab({
      type: i % 2 === 0 ? TabType.AGENT : TabType.TERMINAL,
      id: `t${i}`,
      title: `${i % 2 === 0 ? 'Agent' : 'Terminal'} ${n}`,
    }))
    vi.spyOn(Math, 'random').mockReturnValue(0)
    const name = pickTabName(tabs)
    // Returns SOMETHING from the pool; collision is expected and allowed here.
    expect(TAB_NAMES).toContain(name)
  })
})
