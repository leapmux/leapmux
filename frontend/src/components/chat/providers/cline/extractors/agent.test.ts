import { describe, expect, it } from 'vitest'
import { CLINE_RUN_REASON } from '~/generated/contracts/cline-protocol'
import { CLINE_SUBAGENT_TITLE, clineAgentRequest, clineAgentResult } from './agent'

describe('clineAgentRequest', () => {
  it('titles the call with the first line of its task, and keys the row by the call', () => {
    expect(clineAgentRequest({ systemPrompt: 'You help.', task: '\n  Find the bug.\nThen report.' }, 'call_1')).toEqual({
      description: 'Find the bug.',
      prompt: '\n  Find the bug.\nThen report.',
      promptFormat: 'markdown',
      registryKey: 'call_1',
    })
  })

  // `spawn_agent` states `task`, and a configured agent states `prompt`.
  it('reads the task first, then the prompt of a configured agent', () => {
    expect(clineAgentRequest({ task: 'The task.', prompt: 'The prompt.' }, 'c').prompt).toBe('The task.')
    expect(clineAgentRequest({ task: '', prompt: 'The prompt.' }, 'c').prompt).toBe('The prompt.')
  })

  it('titles a call whose task states nothing with the worker\'s title', () => {
    for (const args of [{}, { task: '  \n ' }, { task: 3 }])
      expect(clineAgentRequest(args, 'c'), JSON.stringify(args)).toMatchObject({ description: CLINE_SUBAGENT_TITLE })
  })
})

describe('clineAgentResult', () => {
  const request = clineAgentRequest({ task: 'Look.' }, 'call_1')
  const outcomeOf = (output: unknown) => clineAgentResult(request, output).agents[0]?.outcome
  const bodyOf = (output: unknown) => clineAgentResult(request, output).agents[0]?.body

  it('states one run with the report, keyed by the call', () => {
    expect(clineAgentResult(request, { text: 'Found it.', finishReason: CLINE_RUN_REASON.Completed })).toEqual({
      agents: [{ description: 'Look.', registryKey: 'call_1', agentId: '', outcome: 'completed', metadata: [], body: 'Found it.' }],
    })
  })

  it('reads the outcome from the reason the run ended', () => {
    expect(outcomeOf({ text: 'x', finishReason: CLINE_RUN_REASON.Completed })).toBe('completed')
    // An iteration limit ends the run with the report it has.
    expect(outcomeOf({ text: 'x', finishReason: CLINE_RUN_REASON.MaxIterations })).toBe('completed')
    expect(outcomeOf({ text: 'x', finishReason: CLINE_RUN_REASON.Aborted })).toBe('stopped')
    expect(outcomeOf({ text: 'x', finishReason: CLINE_RUN_REASON.MistakeLimit })).toBe('failed')
    expect(outcomeOf({ text: 'x', finishReason: CLINE_RUN_REASON.Error })).toBe('failed')
    expect(outcomeOf({ text: 'x', finishReason: 'a reason of a later Cline' })).toBe('failed')
    expect(outcomeOf({ text: 'x' })).toBe('completed')
  })

  it('reads the report of a stored run, which Cline keeps as JSON text', () => {
    expect(bodyOf(JSON.stringify({ text: 'Stored.', finishReason: CLINE_RUN_REASON.Completed }))).toBe('Stored.')
  })

  it('reads a result that is text, and not the run record, as the report', () => {
    expect(clineAgentResult(request, 'Plain words.').agents[0]).toMatchObject({ outcome: 'completed', body: 'Plain words.' })
    expect(bodyOf(JSON.stringify('Quoted words.'))).toBe('Quoted words.')
  })

  // A text that reads as a JSON value other than a record or a string is still the
  // words the call returned. It is not a record with no text.
  it('keeps a text result that reads as a JSON number, boolean or null', () => {
    for (const text of ['42', 'true', 'null'])
      expect(bodyOf(text), text).toBe(text)
  })

  it('states no report for a result that is neither a record nor text', () => {
    for (const output of [undefined, null, 3, ['x']])
      expect(clineAgentResult(request, output).agents[0], JSON.stringify(output)).toMatchObject({ outcome: 'completed', body: '' })
  })
})
