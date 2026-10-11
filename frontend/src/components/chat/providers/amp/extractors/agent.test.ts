import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { ampAgentRequest, ampAgentResult } from './agent'

interface TitleCase {
  tool: string
  input: Record<string, unknown>
  title: string
  why: string
}

/**
 * The browser half of `testdata/amp_subagent_title_conformance.json`. The worker
 * suite replays the same file against the registry row's title, so the row and the
 * transcript card of one call show one title.
 */
describe('amp subagent title conformance', () => {
  const fixturePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../../../testdata/amp_subagent_title_conformance.json')
  const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as { cases: TitleCase[] }

  // A fixture that loads no case would pass while it asserts nothing.
  it('loads the shared fixture', () => {
    expect(fixture.cases.length).toBeGreaterThan(0)
  })

  it.each(fixture.cases)('$why', (c) => {
    expect(ampAgentRequest(c.tool, c.input, 'TU-1').description).toBe(c.title)
  })
})

describe('ampAgentRequest', () => {
  it('reads a Task call by its description', () => {
    expect(ampAgentRequest('Task', { description: 'Return pong', prompt: 'Reply with pong.' }, 'TU-1')).toEqual({
      description: 'Return pong',
      prompt: 'Reply with pong.',
      promptFormat: 'markdown',
      registryKey: 'TU-1',
    })
  })

  it('titles a Task call with no description by its prompt', () => {
    expect(ampAgentRequest('Task', { description: ' ', prompt: '\n  Count files.\nThen report.' }, 'TU-1').description).toBe('Count files.')
    expect(ampAgentRequest('Task', {}, 'TU-1').description).toBe('Subagent')
  })

  it('titles each specialist by its name and its request', () => {
    expect(ampAgentRequest('oracle', { task: 'Review the lock order', context: 'CI deadlocks.', files: ['a.go', 'b.go'] }, 'TU-2')).toEqual({
      description: 'Oracle: Review the lock order',
      agentType: 'oracle',
      prompt: 'Review the lock order\n\nCI deadlocks.\n\n- a.go\n- b.go',
      promptFormat: 'markdown',
      registryKey: 'TU-2',
    })
    expect(ampAgentRequest('librarian', { query: 'How does quartz trap timers?' }, 'TU-3')).toMatchObject({
      description: 'Librarian: How does quartz trap timers?',
      agentType: 'librarian',
      prompt: 'How does quartz trap timers?',
    })
    expect(ampAgentRequest('finder', { query: 'where the bridge closes' }, 'TU-4')).toMatchObject({ description: 'Finder: where the bridge closes', agentType: 'finder' })
  })

  it('keeps the specialist\'s name for a call with no request', () => {
    expect(ampAgentRequest('oracle', {}, 'TU-5').description).toBe('Oracle')
    expect(ampAgentRequest('finder', {}, 'TU-6').description).toBe('Finder')
  })

  it('puts a librarian\'s context below its query', () => {
    expect(ampAgentRequest('librarian', { query: 'How does quartz trap timers?', context: 'The test hangs.' }, 'TU-7').prompt)
      .toBe('How does quartz trap timers?\n\nThe test hangs.')
  })

  // A blank part adds no empty paragraph, and a file entry that is not a path adds no
  // list item.
  it('leaves blank parts and entries that are not paths out of a specialist\'s prompt', () => {
    expect(ampAgentRequest('oracle', { task: 'Review.', context: '  ', files: [] }, 'TU-8').prompt).toBe('Review.')
    expect(ampAgentRequest('oracle', { task: 'Review.', files: ['a.go', 3, null] }, 'TU-9').prompt).toBe('Review.\n\n- a.go')
    expect(ampAgentRequest('oracle', { context: 'Only context.' }, 'TU-10')).toMatchObject({ description: 'Oracle', prompt: 'Only context.' })
  })
})

describe('ampAgentResult', () => {
  it('states one run with the report and the registry row', () => {
    const request = ampAgentRequest('Task', { description: 'Return pong', prompt: 'x' }, 'TU-1')
    expect(ampAgentResult(request, 'pong', false)).toEqual({
      agents: [{ description: 'Return pong', registryKey: 'TU-1', agentId: '', outcome: 'completed', metadata: [], body: 'pong' }],
    })
    expect(ampAgentResult(request, 'boom', true).agents[0]?.outcome).toBe('failed')
  })
})
