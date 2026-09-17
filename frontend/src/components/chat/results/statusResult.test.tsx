import type { TaskResult } from '../ir/tools/task'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { COLLAPSED_RESULT_ROWS } from '../ir/collapse'
import { taskResultCollapsible } from '../ir/tools/task'
import { toolUseHeader } from '../toolStyles.css'
import { StatusResultBody } from './statusResult'
import '../providers/testMocks'

function source(overrides: Partial<TaskResult> = {}): TaskResult {
  return { title: 'Stopped task task-42', outcome: 'stopped', output: 'The task stopped.', ...overrides }
}

describe('StatusResultBody', () => {
  it('draws the reported state above its note', () => {
    const { container } = render(() => <StatusResultBody source={source()} />)
    expect(container.textContent).toContain('Stopped task task-42')
    expect(container.textContent).toContain('The task stopped.')
    expect(container.querySelector('.lucide-octagon-x')).not.toBeNull()
  })

  // A header needs WORDS. `TaskResult.title` is optional, and drawing the header for a
  // result that states no state word put a lone coloured glyph above the note -- which
  // tells a reader that something ended and not what. The row's shared outcome header
  // states the CALL's outcome instead.
  it('draws no header for a result that states no state word', () => {
    // The absence itself is the case: the title key leaves the fixture rather than
    // turning explicitly undefined, which the exact-optional type refuses.
    const titleless = source()
    delete titleless.title
    const { container } = render(() => <StatusResultBody source={titleless} />)
    expect(container.textContent).toContain('The task stopped.')
    expect(container.querySelector('.lucide-octagon-x')).toBeNull()
    expect(container.querySelector(`.${toolUseHeader}`)).toBeNull()
  })

  it.each([
    ['completed', 'lucide-check'],
    ['failed', 'lucide-circle-alert'],
    ['running', 'lucide-clock-fading'],
    ['stopped', 'lucide-octagon-x'],
  ] as const)('draws the %s icon', (outcome, icon) => {
    const { container } = render(() => <StatusResultBody source={source({ outcome })} />)
    expect(container.querySelector(`.${icon}`)).not.toBeNull()
  })

  /**
   * The outcome picks the GLYPH and never a word.
   *
   * This card and the subagent card now share one outcome vocabulary, and they state it
   * differently: the subagent card falls back to the outcome word when a provider names
   * none, while this one always draws `title` -- the words of the surface that reported
   * the state. Renaming a member of the shared vocabulary must therefore not reach this
   * header, which is what makes the two safe to share.
   */
  it.each(['completed', 'failed', 'running', 'stopped'] as const)('never states the %s outcome as words', (outcome) => {
    // A note that carries none of the outcome words itself, so the only way one could
    // reach the header is the outcome field.
    const { container } = render(() => <StatusResultBody source={source({ outcome, title: 'Background task task-42', output: 'Read 4 files.' })} />)
    expect(container.textContent).toContain('Background task task-42')
    expect(container.textContent).not.toContain(outcome)
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

describe('taskResultCollapsible', () => {
  it('reports a note that fits the collapsed row', () => {
    expect(taskResultCollapsible(source({ output: 'one\ntwo' }))).toBe(false)
  })

  it('reports a note longer than the collapsed row', () => {
    const output = Array.from({ length: COLLAPSED_RESULT_ROWS + 2 }, (_, index) => `line ${index}`).join('\n')
    expect(taskResultCollapsible(source({ output }))).toBe(true)
  })
})
