import { describe, expect, it } from 'vitest'
import { input } from '../../testUtils'
import { piAgentRequest, piAgentResult } from './agent'
import { piPairedResult } from './toolCommon'

const request = { type: 'tool_execution_start', toolCallId: 'call', toolName: 'Agent', args: { description: 'Inspect sample', prompt: 'Read sample.ts', subagent_type: 'Explore', model: 'requested-model', thinking: 'high', max_turns: 4 } }
const result = (details: Record<string, unknown>, text = 'Report') => ({ type: 'tool_execution_end', toolCallId: 'call', toolName: 'Agent', result: { content: [{ type: 'text', text }], details } })

describe('pi agent source', () => {
  it('extracts a retrieved report without request data', () => {
    const payload = { ...result({}, 'Agent: child-1\nType: Explore | Status: completed | Tool uses: 0 | Context: 0% | Duration: 0.0s\nDescription: Inspect sample\n\nReport\n\n--- Agent Conversation ---\nConversation'), toolName: 'get_subagent_result' }
    const source = piAgentResult(payload)
    expect(source.agentId).toBe('child-1')
    expect(source.outcome).toBe('completed')
    expect(source.body).toBe('Report\n\n--- Agent Conversation ---\nConversation')
    expect(source.metadata).toContainEqual({ label: 'Tool uses', value: '0' })
    expect(source.metadata).toContainEqual({ label: 'Context', value: '0%' })
  })

  it('preserves retrieval text when its identity or header is invalid', () => {
    const text = 'Agent: child-1\nType: Explore | Status: completed | Tool uses: 1 | Duration: 1.0s\nDescription: Inspect sample\n\nReport'
    const native = { ...result({}, text), toolName: 'get_subagent_result' }
    const foreign = input({ ...request, toolName: 'get_subagent_result', args: { agent_id: 'foreign' } })
    expect(piAgentResult(native, foreign).body).toBe(text)
    expect(piAgentResult(native, foreign).outcome).toBe('unknown')
    for (const malformed of [text.replace('Status: completed', 'Status: other'), text.replace('Tool uses: 1', 'Tool uses: -1'), text.replace('Description: ', 'Description\n')]) {
      const source = piAgentResult({ ...result({}, malformed), toolName: 'get_subagent_result' })
      expect(source.body).toBe(malformed)
      expect(source.outcome).toBe('unknown')
    }
  })

  it.each(['get_subagent_result', 'steer_subagent'])('recognizes a missing agent without the %s request', (toolName) => {
    const text = 'Agent not found: "child-1". It may have been cleaned up.'
    const source = piAgentResult({ ...result({}, text), toolName })
    expect(source.agentId).toBe('child-1')
    expect(source.outcome).toBe('failed')
    expect(source.body).toBe(text)
  })

  it.each([
    ['Steering message sent to agent child-1. The agent will process it after its current tool execution.\nCurrent state: 1 tool use', 'running'],
    ['Steering message queued for agent child-1. It will be delivered once the session initializes.', 'running'],
    ['Agent "child-1" is not running (status: completed). Cannot steer a non-running agent.', 'failed'],
    ['Failed to steer agent: Source unavailable', 'failed'],
    ['Provider returned an unknown response', 'unknown'],
  ])('preserves a native steering response: %s', (text, outcome) => {
    const source = piAgentResult({ ...result({}, text), toolName: 'steer_subagent' }, input({ ...request, toolName: 'steer_subagent', args: { agent_id: 'child-1' } }))
    expect(source.outcome).toBe(outcome)
    expect(source.body).toBe(text)
  })

  it('recovers request-only options and gives native model data priority', () => {
    const source = piAgentResult(result({ status: 'completed' }), input(request))
    expect(source.description).toBe('Inspect sample')
    expect(source.metadata).toContainEqual({ label: 'Model', value: 'requested-model' })
    expect(source.metadata).toContainEqual({ label: 'Thinking', value: 'high' })
    expect(source.metadata).toContainEqual({ label: 'Maximum turns', value: '4' })
    expect(source.metadata).toContainEqual({ label: 'Agent type', value: 'Explore' })
    const native = piAgentResult(result({ status: 'completed', modelName: 'resolved-model' }), input(request))
    expect(native.metadata.filter(item => item.label === 'Model')).toEqual([{ label: 'Model', value: 'resolved-model' }])
  })

  it.each([
    ['queued', 'running'],
    ['running', 'running'],
    ['background', 'running'],
    ['completed', 'completed'],
    ['error', 'failed'],
    ['stopped', 'stopped'],
    ['aborted', 'unknown'],
    ['steered', 'unknown'],
    ['new-status', 'unknown'],
    ['toString', 'unknown'],
    ['', 'unknown'],
  ])('preserves the native %s outcome', (status, outcome) => {
    expect(piAgentResult(result({ status })).outcome).toBe(outcome)
  })

  it('keeps turn-limit and user-stop notices after it removes native summaries', () => {
    for (const [status, note] of [
      ['aborted', ' (aborted — hit the turn limit before completion; output may be incomplete)'],
      ['steered', ' (wrapped up at the turn limit — output may be partial)'],
      ['stopped', ' (STOPPED BY THE USER before completion — output is partial; the task was NOT finished)'],
    ]) {
      const source = piAgentResult(result({ status, toolUses: 0 }, `Agent completed in 1.0s (0 tool uses)${note}.\n\nPartial report`))
      expect(source.body).toBe('Partial report')
      expect(source.metadata).toContainEqual({ label: 'Notice', value: note.trim().slice(1, -1) })
      expect(source.outcome).not.toBe('completed')
    }
  })

  it('preserves unknown summaries and summaries with mismatched counters', () => {
    const text = 'Agent completed in 1.0s (2 tool uses).\n\nReport'
    expect(piAgentResult(result({ status: 'completed', toolUses: 1 }, text)).body).toBe(text)
    expect(piAgentResult(result({ status: 'unknown', toolUses: 2 }, text)).body).toBe(text)
    const unknown = 'Provider notice\n\nReport'
    expect(piAgentResult(result({}, unknown)).body).toBe(unknown)
  })

  it('keeps zero counters and ignores malformed counters', () => {
    const source = piAgentResult(result({ toolUses: 0, turnCount: 0, durationMs: 0, maxTurns: -1 }))
    expect(source.metadata).toEqual([{ label: 'Tool uses', value: '0' }, { label: 'Turns', value: '0' }, { label: 'Duration', value: '0ms' }])
    for (const value of [-1, 0.5, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1, '', null])
      expect(piAgentResult(result({ toolUses: value, durationMs: value })).metadata).toEqual([])
  })

  it('preserves empty output and lets tool errors override child success', () => {
    expect(piAgentResult(result({}, '')).body).toBe('')
    expect(piAgentResult({ ...result({ status: 'completed' }), isError: true }).outcome).toBe('failed')
    expect(piAgentRequest({ type: 'tool_execution_start' }).prompt).toBe('')
  })

  it('accepts only the matching final result for a request', () => {
    const complete = result({ status: 'completed' })
    expect(piPairedResult(request, input(complete))?.parentObject).toEqual(complete)
    for (const changes of [{ toolCallId: '' }, { toolCallId: 'foreign' }, { toolCallId: 1 }, { toolName: 'read' }, { type: 'tool_execution_update' }])
      expect(piPairedResult(request, input({ ...complete, ...changes }))).toBeUndefined()
    expect(piPairedResult(complete, input(complete))).toBeUndefined()
    expect(piPairedResult({ ...request, toolCallId: '' }, input(complete))).toBeUndefined()
  })

  it('rejects foreign request fields and leaves source messages unchanged', () => {
    const payload = result({ status: 'completed' })
    const before = JSON.stringify(payload)
    expect(piAgentResult(payload, input({ ...request, toolCallId: 'foreign' })).description).toBe('')
    piAgentResult(payload, input(request))
    expect(JSON.stringify(payload)).toBe(before)
  })
})
