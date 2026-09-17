import type { AgentRun } from '../ir/tools/agent'
import type { TaskResult } from '../ir/tools/task'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentResultBody } from './agentResult'
import { StatusResultBody } from './statusResult'
import '../providers/testMocks'

function glyphOf(container: HTMLElement): string | undefined {
  return [...container.querySelectorAll('svg')].map(node => node.getAttribute('class') ?? '').find(name => name.includes('lucide-'))
}

function agentGlyph(outcome: AgentRun['outcome']): string | undefined {
  const source: AgentRun = { description: 'Inspect the parser', agentId: 'a-1', outcome, metadata: [], body: '' }
  return glyphOf(render(() => <AgentResultBody source={source} />).container)
}

function taskGlyph(outcome: TaskResult['outcome']): string | undefined {
  const source: TaskResult = { title: 'Background task task-42', outcome, output: '' }
  return glyphOf(render(() => <StatusResultBody source={source} />).container)
}

/**
 * The two cards that state an outcome draw the SAME glyph for the same one.
 *
 * They held two copies of these three mappings, under two different words for one
 * outcome -- a subagent that finished was `completed` and a background task that
 * finished was `succeeded` -- so a change to the "it stopped" glyph reached one card
 * and left the other. One vocabulary and one shared table is what this pins.
 */
describe('the ended-outcome glyphs', () => {
  it.each(['completed', 'failed', 'stopped'] as const)('draws the same glyph on both cards for %s', (outcome) => {
    const agent = agentGlyph(outcome)
    expect(agent).toBeDefined()
    expect(taskGlyph(outcome)).toBe(agent)
  })

  /**
   * `running` is the one outcome the two answer differently, and deliberately.
   *
   * A task surface always states one of four, so its table is exhaustive and gives
   * `running` the waiting glyph. The subagent card reads a MISSING entry as "this run
   * has not ended", which is how the row decides whether to draw its own shared outcome
   * header -- so adding a glyph there would suppress that header for a live run.
   */
  it('parts company for a run that has not ended', () => {
    expect(taskGlyph('running')).toContain('lucide-clock-fading')
    expect(agentGlyph('running')).toContain('lucide-bot')
  })
})
