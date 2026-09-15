import type { StatusResultSource } from './statusResult'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { COLLAPSED_RESULT_ROWS } from './collapse'
import { StatusResultBody, statusResultCollapsible } from './statusResult'
import '../providers/testMocks'

function source(overrides: Partial<StatusResultSource> = {}): StatusResultSource {
  return { title: 'Stopped task task-42', outcome: 'stopped', output: 'The task stopped.', ...overrides }
}

describe('statusResultBody', () => {
  it('draws the reported state above its note', () => {
    const { container } = render(() => <StatusResultBody source={source()} />)
    expect(container.textContent).toContain('Stopped task task-42')
    expect(container.textContent).toContain('The task stopped.')
    expect(container.querySelector('.lucide-octagon-x')).not.toBeNull()
  })

  it.each([
    ['succeeded', 'lucide-check'],
    ['failed', 'lucide-circle-alert'],
    ['waiting', 'lucide-clock-fading'],
    ['stopped', 'lucide-octagon-x'],
  ] as const)('draws the %s icon', (outcome, icon) => {
    const { container } = render(() => <StatusResultBody source={source({ outcome })} />)
    expect(container.querySelector(`.${icon}`)).not.toBeNull()
  })

  it('draws the command the reported operation ran', () => {
    const { container } = render(() => <StatusResultBody source={source({ command: 'npm run dev' })} />)
    expect(container.textContent).toContain('npm run dev')
  })

  // A state with no note is the whole answer. An empty block below it would read as
  // output that the operation produced and did not.
  it('draws no body for a state that carries no note', () => {
    const { container } = render(() => <StatusResultBody source={source({ output: '' })} />)
    expect(container.textContent).toBe('Stopped task task-42')
  })
})

describe('statusResultCollapsible', () => {
  it('reports a note that fits the collapsed row', () => {
    expect(statusResultCollapsible(source({ output: 'one\ntwo' }))).toBe(false)
  })

  it('reports a note longer than the collapsed row', () => {
    const output = Array.from({ length: COLLAPSED_RESULT_ROWS + 2 }, (_, index) => `line ${index}`).join('\n')
    expect(statusResultCollapsible(source({ output }))).toBe(true)
  })
})
