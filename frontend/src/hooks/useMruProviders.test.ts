import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'

const CC = AgentProvider.CLAUDE_CODE
const CODEX = AgentProvider.CODEX
const OPENCODE = AgentProvider.OPENCODE
const GEMINI = AgentProvider.GEMINI_CLI

// Mutable state the mock reads from
let mockStored: AgentProvider[] = []

vi.mock('~/lib/mruAgentProviders', () => ({
  getMruProviders: () => mockStored,
  touchMruProvider: (p: AgentProvider) => {
    mockStored = [p, ...mockStored.filter(x => x !== p)]
  },
}))

// Must import after vi.mock so the mock is in place
const { useMruProviders } = await import('./useMruProviders')

/** Helper: create a hook with a controllable available-providers list. */
function setup(initial: AgentProvider[], maxCount = 2) {
  let available = initial
  const hook = useMruProviders(() => available, maxCount)
  return {
    get: () => hook.mruProviders(),
    record: hook.recordProviderUse,
    setAvailable: (v: AgentProvider[]) => { available = v },
  }
}

describe('useMruProviders stabilization', () => {
  it('returns backfill order on first call with empty mru', () => {
    mockStored = []
    const h = setup([CC, CODEX])
    expect(h.get()).toEqual([CC, CODEX])
  })

  it('keeps previous order when clicking an existing button', () => {
    mockStored = []
    const h = setup([CC, CODEX])
    expect(h.get()).toEqual([CC, CODEX])

    // Clicking Codex moves it to front of MRU, but display should be stable
    h.record(CODEX)
    expect(h.get()).toEqual([CC, CODEX])

    // Clicking Claude Code should also not swap
    h.record(CC)
    expect(h.get()).toEqual([CC, CODEX])
  })

  it('replaces lru provider while keeping incumbent position', () => {
    mockStored = []
    const h = setup([CC, CODEX])
    expect(h.get()).toEqual([CC, CODEX])

    // OpenCode enters the top 2, replacing Codex (least recently used)
    mockStored = [OPENCODE, CC]
    h.setAvailable([CC, CODEX, OPENCODE])
    expect(h.get()).toEqual([CC, OPENCODE])
  })

  it('fills vacated slot when incumbent becomes unavailable', () => {
    mockStored = [CC, CODEX]
    const h = setup([CC, CODEX])
    expect(h.get()).toEqual([CC, CODEX])

    // CC becomes unavailable, Gemini enters
    mockStored = [CODEX, GEMINI]
    h.setAvailable([CODEX, GEMINI])
    expect(h.get()).toEqual([GEMINI, CODEX])
  })

  it('handles complete replacement of both providers', () => {
    mockStored = [CC, CODEX]
    const h = setup([CC, CODEX])
    expect(h.get()).toEqual([CC, CODEX])

    // Both replaced
    mockStored = [OPENCODE, GEMINI]
    h.setAvailable([OPENCODE, GEMINI])
    expect(h.get()).toEqual([OPENCODE, GEMINI])
  })

  it('respects maxCount', () => {
    mockStored = []
    const h = setup([CC, CODEX, OPENCODE], 3)
    expect(h.get()).toEqual([CC, CODEX, OPENCODE])
  })

  it('works when prevDisplay is shorter than ideal (growing set)', () => {
    mockStored = []
    const h = setup([CC], 2)
    expect(h.get()).toEqual([CC])

    // A second provider becomes available
    h.setAvailable([CC, CODEX])
    expect(h.get()).toEqual([CC, CODEX])
  })
})
