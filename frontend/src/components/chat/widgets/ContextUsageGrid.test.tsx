import type { ContextUsageInfo } from '~/stores/agentSession.store'
import { cleanup, render } from '@solidjs/testing-library'
import { afterEach, describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { computePercentage, contextBufferPct, contextSize, ContextUsageGrid, DEFAULT_CONTEXT_WINDOW, resolveContextWindow } from './ContextUsageGrid'

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

  it('honours a reported zero instead of reading it as absent', () => {
    // A provider that reports an empty context says 0, not nothing. Testing
    // `contextTokens` for truthiness instead of for null would fall through to
    // the component sum here and report a used context that does not exist.
    expect(contextSize({
      inputTokens: 100,
      cacheCreationInputTokens: 10,
      cacheReadInputTokens: 20,
      outputTokens: 5,
      contextTokens: 0,
    })).toBe(0)
  })

  it('treats a missing outputTokens as zero rather than NaN', () => {
    expect(contextSize({
      inputTokens: 100,
      cacheCreationInputTokens: 0,
      cacheReadInputTokens: 0,
    })).toBe(100)
  })
})

describe('context window resolution', () => {
  const empty = { inputTokens: 0, cacheCreationInputTokens: 0, cacheReadInputTokens: 0 }

  it('prefers the window the usage data reports', () => {
    expect(resolveContextWindow({ ...empty, contextWindow: 500 }, 900)).toBe(500)
  })

  it('falls back to the model metadata when the usage reports none', () => {
    expect(resolveContextWindow(empty, 900)).toBe(900)
  })

  it('falls back to the default when neither is known', () => {
    expect(resolveContextWindow(empty)).toBe(DEFAULT_CONTEXT_WINDOW)
    expect(DEFAULT_CONTEXT_WINDOW).toBe(200_000)
  })

  it('steps past a zero or negative window rather than dividing by it', () => {
    // computePercentage divides by this, so a 0 that reached the caller would
    // make every percentage Infinity and fill the whole meter.
    expect(resolveContextWindow({ ...empty, contextWindow: 0 }, 900)).toBe(900)
    expect(resolveContextWindow({ ...empty, contextWindow: -1 }, 900)).toBe(900)
    expect(resolveContextWindow({ ...empty, contextWindow: 0 }, 0)).toBe(DEFAULT_CONTEXT_WINDOW)
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

afterEach(cleanup)

const INACTIVE = 'var(--context-grid-inactive)'
const WARNING = 'var(--context-grid-warning)'
const ACTIVE = 'currentColor'

/** Usage that reports `pct` of a 100-token window, with no provider buffer. */
function usageAt(pct: number): ContextUsageInfo {
  return {
    inputTokens: 0,
    cacheCreationInputTokens: 0,
    cacheReadInputTokens: 0,
    outputTokens: 0,
    contextTokens: pct,
    contextWindow: 100,
  }
}

/**
 * The meter as three rows of three, so a case can state a shape rather than a
 * list of nine colours: `.` unfilled, `#` filled, `!` filled and over the
 * warning threshold.
 *
 * Reading row-major, top row first, which is how `PipGrid` takes its fills --
 * so a case reads in the direction the pips are laid out, and the meter's own
 * bottom-up fill order shows as the bottom row filling first.
 */
function meterOf(container: HTMLElement): string {
  const glyphs: Record<string, string> = { [INACTIVE]: '.', [ACTIVE]: '#', [WARNING]: '!' }
  const rows = [...container.querySelectorAll('rect')].map((rect) => {
    const fill = rect.getAttribute('fill') ?? ''
    const glyph = glyphs[fill]
    if (!glyph)
      throw new Error(`unexpected pip fill ${fill}`)
    return glyph
  })
  return [rows.slice(0, 3), rows.slice(3, 6), rows.slice(6, 9)].map(r => r.join('')).join('/')
}

describe('contextUsageGrid rendering', () => {
  it('falls back to the info icon when there is no usage yet', () => {
    const { container } = render(() => <ContextUsageGrid size={12} />)
    expect(container.querySelectorAll('rect')).toHaveLength(0)
    expect(container.querySelector('svg.lucide-info')).not.toBeNull()
  })

  it('falls back to the info icon when the context is empty', () => {
    const { container } = render(() => <ContextUsageGrid size={12} contextUsage={usageAt(0)} />)
    expect(container.querySelectorAll('rect')).toHaveLength(0)
  })

  it('fills bottom-left first, so the meter grows upwards', () => {
    // 35% -> ceil(3.5) = 4 pips: the whole bottom row, then the leftmost pip of
    // the middle row. The fills reach PipGrid row-major, so this pins the
    // mapping from fill order to position.
    const { container } = render(() => <ContextUsageGrid size={12} contextUsage={usageAt(35)} />)
    expect(meterOf(container)).toBe('.../#../###')
  })

  it('fills one pip at the bottom left for a barely used context', () => {
    const { container } = render(() => <ContextUsageGrid size={12} contextUsage={usageAt(1)} />)
    expect(meterOf(container)).toBe('.../.../#..')
  })

  it('fills the bottom two rows at 60%', () => {
    const { container } = render(() => <ContextUsageGrid size={12} contextUsage={usageAt(60)} />)
    expect(meterOf(container)).toBe('.../###/###')
  })

  it('fills every pip from 81% up, while staying in the normal colour', () => {
    const { container } = render(() => <ContextUsageGrid size={12} contextUsage={usageAt(81)} />)
    expect(meterOf(container)).toBe('###/###/###')
  })

  it('switches to the warning colour at 91%', () => {
    const { container } = render(() => <ContextUsageGrid size={12} contextUsage={usageAt(91)} />)
    expect(meterOf(container)).toBe('!!!/!!!/!!!')
  })

  it('stays in the normal colour just below the warning threshold', () => {
    const { container } = render(() => <ContextUsageGrid size={12} contextUsage={usageAt(90)} />)
    expect(meterOf(container)).toBe('###/###/###')
  })

  it('caps a context past its window at nine warning pips', () => {
    const { container } = render(() => <ContextUsageGrid size={12} contextUsage={usageAt(400)} />)
    expect(meterOf(container)).toBe('!!!/!!!/!!!')
  })

  it('renders at the size it is given, under a test id the e2e suite can find', () => {
    const { container } = render(() => <ContextUsageGrid size={12} contextUsage={usageAt(50)} />)
    const svg = container.querySelector('[data-testid="context-usage-grid"]')!
    expect(svg.getAttribute('width')).toBe('12')
    expect(svg.getAttribute('height')).toBe('12')
  })

  it('reports the rounded percentage as its accessible name', () => {
    const { getByLabelText } = render(() => <ContextUsageGrid size={12} contextUsage={usageAt(35)} />)
    expect(getByLabelText('Context: 35%')).toBeTruthy()
  })
})
