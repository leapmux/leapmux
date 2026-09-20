import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { ResultDivider } from './resultDividerRenderers'

function totalsOf(meta: Parameters<typeof ResultDivider>[0]['model']['meta']): string {
  const { container } = render(() => <ResultDivider model={{ label: 'Turn ended', ...(meta !== undefined ? { meta } : {}) }} />)
  return container.querySelector('[data-testid="result-divider-totals"]')?.textContent ?? ''
}

/*
 * The turn totals the worker measured. They reached no reader before the divider row
 * moved onto the shared model: the tool count was parsed and thrown away, and the cost
 * went to the session panel alone.
 */
describe('result divider totals', () => {
  it('states the tool count and the cost', () => {
    expect(totalsOf({ numToolUses: 5, costUsd: 0.1234 })).toBe('5 tools · $0.1234')
  })

  it('states one tool in the singular', () => {
    expect(totalsOf({ numToolUses: 1 })).toBe('1 tool')
  })

  // A turn that used no tool, or cost nothing, says so by saying NOTHING. "0 tools"
  // and "$0.0000" are noise beside a rule that already states the turn ended.
  it.each([
    ['no tool', { numToolUses: 0 }],
    ['no cost', { costUsd: 0 }],
    ['neither', { numToolUses: 0, costUsd: 0 }],
    ['no measurement at all', undefined],
  ])('draws nothing for %s', (_name, meta) => {
    expect(totalsOf(meta)).toBe('')
  })

  // The duration is the one total the row does NOT repeat: every provider writes it
  // into its own label, so a second copy would state it twice in a different voice.
  it('leaves the duration to the label', () => {
    expect(totalsOf({ durationMs: 12_000 })).toBe('')
  })

  it('keeps the label and the danger colour beside the totals', () => {
    const { container } = render(() => (
      <ResultDivider model={{ label: 'Turn failed', isError: true, meta: { numToolUses: 2 } }} />
    ))
    const rule = container.querySelector('[data-testid="result-divider"]') as HTMLElement
    expect(rule.textContent).toContain('Turn failed')
    expect(rule.textContent).toContain('2 tools')
    expect(rule.style.color).toBe('var(--danger)')
  })
})
