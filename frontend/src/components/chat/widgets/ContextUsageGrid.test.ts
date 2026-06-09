import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { computePercentage, contextBufferPct, contextSize } from './ContextUsageGrid'

// Side-effect import: register the Claude plugin so contextBufferPct can resolve
// its autocompact buffer through the registry.
import '../providers/claude/plugin'

describe('context usage grid token math', () => {
  it('prefers provider-reported contextTokens when present', () => {
    expect(contextSize({
      inputTokens: 100,
      cacheCreationInputTokens: 10,
      cacheReadInputTokens: 20,
      outputTokens: 5,
      contextTokens: 1000,
    })).toBe(1000)
  })

  it('falls back to input/cache/output token components', () => {
    expect(contextSize({
      inputTokens: 100,
      cacheCreationInputTokens: 10,
      cacheReadInputTokens: 20,
      outputTokens: 5,
    })).toBe(135)
  })

  it('computes percentage from provider-reported contextTokens', () => {
    expect(computePercentage({
      inputTokens: 0,
      cacheCreationInputTokens: 0,
      cacheReadInputTokens: 0,
      outputTokens: 0,
      contextTokens: 50,
      contextWindow: 200,
    })).toBe(25)
  })

  it('applies the provider autocompact buffer from its plugin', () => {
    // Claude reserves 16.5% headroom, so 50/200 measures against usable capacity
    // 200*(1-0.165)=167 -> ~29.94%, not the bufferless 25%. Providers with no
    // plugin buffer (default 0) keep the bufferless math.
    expect(contextBufferPct(AgentProvider.CLAUDE_CODE)).toBe(16.5)
    expect(contextBufferPct(AgentProvider.CODEX)).toBe(0)
    const claudePct = computePercentage(
      { inputTokens: 0, cacheCreationInputTokens: 0, cacheReadInputTokens: 0, outputTokens: 0, contextTokens: 50, contextWindow: 200 },
      undefined,
      AgentProvider.CLAUDE_CODE,
    )
    expect(claudePct).toBeCloseTo(29.94, 1)
  })
})
