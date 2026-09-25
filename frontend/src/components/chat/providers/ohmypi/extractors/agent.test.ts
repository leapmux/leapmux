import { describe, expect, it } from 'vitest'
import { ohMyPiAgentRequest, ohMyPiAgentRuns } from './agent'

describe('ohMyPiAgentRequest', () => {
  it('reads one task under its name, after the shared context', () => {
    expect(ohMyPiAgentRequest({ context: '# Goal\nProbe.', tasks: [{ name: 'ScoutOne', agent: 'task', task: 'Say hello.' }] })).toEqual({
      description: 'ScoutOne',
      agentType: 'task',
      prompt: '# Goal\nProbe.\n\nSay hello.',
      promptFormat: 'markdown',
    })
  })

  it('states how many subagents a batch launches, and lists each task', () => {
    const request = ohMyPiAgentRequest({ tasks: [{ name: 'A', agent: 'task', task: 'Do A.' }, { agent: 'explore', task: 'Find B.\nMore.' }] })
    expect(request.description).toBe('2 subagents')
    expect(request.agentType).toBeUndefined()
    expect(request.prompt).toBe('### A\n\nDo A.\n\n### Find B.\n\nFind B.\nMore.')
  })

  it('reads the single-task form', () => {
    expect(ohMyPiAgentRequest({ agent: 'task', task: 'Summarize the README.\nIn detail.' })).toMatchObject({
      description: 'Summarize the README.',
      prompt: 'Summarize the README.\nIn detail.',
    })
  })

  it('answers a launch with no task', () => {
    expect(ohMyPiAgentRequest({})).toEqual({ description: 'Subagent', prompt: '', promptFormat: 'markdown' })
    expect(ohMyPiAgentRequest({ tasks: ['bad', { name: '', task: '' }] }).description).toBe('Subagent')
  })

  it('states the agent type that every task of a batch shares', () => {
    const request = ohMyPiAgentRequest({ context: 'Shared.', tasks: [{ name: 'A', agent: 'explore', task: 'Do A.' }, { name: 'B', agent: 'explore', task: 'Do B.' }] })
    expect(request).toEqual({
      description: '2 subagents',
      agentType: 'explore',
      prompt: 'Shared.\n\n### A\n\nDo A.\n\n### B\n\nDo B.',
      promptFormat: 'markdown',
    })
  })

  it('keeps a task that states a name and no text, and heads a task with no name by its first line', () => {
    expect(ohMyPiAgentRequest({ tasks: [{ name: 'Scout' }] })).toMatchObject({ description: 'Scout', prompt: '' })
    const request = ohMyPiAgentRequest({ tasks: [{ name: 'Scout' }, { task: '\n  \nLook around.' }] })
    expect(request.description).toBe('2 subagents')
    expect(request.prompt.split('\n').filter(line => line.startsWith('### '))).toEqual(['### Scout', '### Look around.'])
  })

  it('reads a single-task form that states no task as no launch', () => {
    expect(ohMyPiAgentRequest({ name: 'Scout', agent: 'task' })).toEqual({ description: 'Subagent', prompt: '', promptFormat: 'markdown' })
  })
})

describe('ohMyPiAgentRuns', () => {
  it('reads each run a waiting call states, with its output', () => {
    // omp 18.2.11's own details (probe s3, sync), shortened.
    const runs = ohMyPiAgentRuns({
      results: [{ id: 'ScoutOne', assignment: 'Say hello.', exitCode: 0, output: '"child says hello"', stderr: '', durationMs: 373, tokens: 2460, resolvedModel: 'mock/mock-model:high' }],
    }, '')
    expect(runs).toEqual([{
      description: 'Say hello.',
      agentId: 'ScoutOne',
      outcome: 'completed',
      metadata: [
        { label: 'Agent ID', value: 'ScoutOne' },
        { label: 'Model', value: 'mock/mock-model:high' },
        { label: 'Duration', value: '373ms' },
        { label: 'Tokens', value: '2,460' },
      ],
      body: '"child says hello"',
    }])
  })

  it('reads a failed, an aborted and an unknown run', () => {
    const runs = ohMyPiAgentRuns({
      results: [
        { id: 'A', exitCode: 1, output: '', stderr: 'boom' },
        { id: 'B', aborted: true, exitCode: 0, output: 'partial' },
        { id: 'C', output: 'x' },
      ],
    }, '')
    expect(runs.map(run => [run.agentId, run.outcome, run.body])).toEqual([
      ['A', 'failed', 'boom'],
      ['B', 'stopped', 'partial'],
      ['C', 'unknown', 'x'],
    ])
  })

  it('states the error of a failed run that printed nothing', () => {
    // omp 18.2.11's SingleResult of a subagent whose model call failed.
    const runs = ohMyPiAgentRuns({ results: [{ id: 'A', exitCode: 1, output: '', stderr: '', error: '429 rate limited' }] }, '')
    expect(runs[0]).toMatchObject({ agentId: 'A', outcome: 'failed', body: '429 rate limited', bodyLabel: 'Error' })
  })

  it('states the reason of a stopped run that printed nothing', () => {
    const runs = ohMyPiAgentRuns({ results: [{ id: 'B', exitCode: 1, aborted: true, abortReason: 'Cancelled by the parent', error: 'aborted', output: '  ' }] }, '')
    expect(runs[0]).toMatchObject({ agentId: 'B', outcome: 'stopped', body: 'Cancelled by the parent', bodyLabel: 'Abort reason' })
  })

  it('prefers the output, then the error output, then the error', () => {
    const runs = ohMyPiAgentRuns({
      results: [
        { id: 'A', exitCode: 1, output: 'partial', stderr: 'warn', error: 'boom' },
        { id: 'B', exitCode: 1, output: '', stderr: 'warn', error: 'boom' },
        { id: 'C', exitCode: 1, output: '', stderr: '' },
      ],
    }, '')
    expect(runs.map(run => [run.body, run.bodyLabel])).toEqual([['partial', undefined], ['warn', undefined], ['', undefined]])
  })

  it('reads each run a background call left running', () => {
    // omp 18.2.11's own details (probe s3, async), shortened.
    const runs = ohMyPiAgentRuns({
      results: [],
      progress: [{ index: 0, id: 'ScoutOne', status: 'pending', assignment: 'Say hello from the child and yield it.' }],
      async: { state: 'running', jobId: 'ScoutOne', type: 'task' },
    }, 'Spawned agent ScoutOne')
    expect(runs).toEqual([{
      description: 'Say hello from the child and yield it.',
      agentId: 'ScoutOne',
      outcome: 'running',
      statusLabel: 'running in the background',
      metadata: [{ label: 'Agent ID', value: 'ScoutOne' }],
      body: '',
    }])
  })

  it('reads a progress word when the call is not in the background', () => {
    const runs = ohMyPiAgentRuns({ progress: [{ id: 'A', status: 'failed' }, { id: 'B', status: 'aborted' }, { id: 'C', status: 'odd' }] }, 'text')
    expect(runs.map(run => run.outcome)).toEqual(['failed', 'stopped', 'unknown'])
  })

  it('reads every progress word, and draws the call\'s text as the body of a run that is not in the background', () => {
    const runs = ohMyPiAgentRuns({
      progress: [{ id: 'A', status: 'completed' }, { id: 'B', status: 'pending' }, { id: 'C', status: 'running' }],
      async: { state: 'completed', jobId: 'A', type: 'task' },
    }, 'All done.')
    expect(runs.map(run => [run.outcome, run.body, run.statusLabel])).toEqual([
      ['completed', 'All done.', undefined],
      ['running', 'All done.', undefined],
      ['running', 'All done.', undefined],
    ])
  })

  it('heads a run by its assignment, else its task, else its id, else a word of its own', () => {
    const runs = ohMyPiAgentRuns({
      results: [
        { id: 'A', assignment: '\n  First line.\nSecond.', exitCode: 0 },
        { id: 'B', task: 'From the task.', exitCode: 0 },
        { id: 'C', exitCode: 0 },
        { exitCode: 0 },
      ],
    }, '')
    expect(runs.map(run => [run.description, run.agentId])).toEqual([
      ['First line.', 'A'],
      ['From the task.', 'B'],
      ['C', 'C'],
      ['Subagent', ''],
    ])
  })

  it('states no token count of zero, and a duration of zero', () => {
    const runs = ohMyPiAgentRuns({ results: [{ id: 'A', exitCode: 0, output: 'x', durationMs: 0, tokens: 0 }] }, '')
    expect(runs[0]?.metadata).toEqual([{ label: 'Agent ID', value: 'A' }, { label: 'Duration', value: '0ms' }])
  })

  it('reads the progress list when the results list holds no records', () => {
    const runs = ohMyPiAgentRuns({ results: ['x', null], progress: [{ id: 'A', status: 'completed' }] }, 'text')
    expect(runs.map(run => run.agentId)).toEqual(['A'])
  })

  it('states nothing for a result with no runs', () => {
    expect(ohMyPiAgentRuns({}, 'text')).toEqual([])
  })
})
