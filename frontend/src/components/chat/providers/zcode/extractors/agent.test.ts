import { describe, expect, it } from 'vitest'
import { input } from '../../testUtils'
import { zcodeAgentResult } from './agent'
import { zcodeRow } from './toolCommon'

const request = input({ type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'call', toolName: 'Agent', input: { description: 'Inspect the code', prompt: 'Read the entry points.' } } })
const footer = (tokens = '123') => `\nagentId: agent_child (use SendMessage with to: 'agent_child' to continue this agent)\n<usage>subagent_tokens: ${tokens}\ntool_uses: 0\nduration_ms: 0</usage>`
const launch = [
  'Async agent launched successfully.',
  'agentId: agent_child (internal ID - do not mention to user. Use SendMessage with to: \'agent_child\' to continue this agent.)',
  'The agent is working in the background. You will be notified automatically when it completes.',
  'Briefly tell the user what you launched and end your response. Do not generate any other text - agent results will arrive in a subsequent message.',
].join('\n')

function result(content: string, success = true) {
  return zcodeAgentResult(zcodeRow({ type: 'tool.updated', payload: { kind: 'result', toolCallId: 'call', result: { success, content } } }, 'Agent', request))
}

describe('zcode agent result extraction', () => {
  it('preserves the report exactly and keeps zero usage values', () => {
    const report = 'First paragraph\n\n```xml\n<usage>example</usage>\n```\n'
    const source = result(report + footer())!
    expect(source.body).toBe(report)
    expect(source.metadata).toContainEqual({ label: 'Tool uses', value: '0' })
    expect(source.metadata).toContainEqual({ label: 'Duration', value: '0ms' })
  })

  it('preserves counters larger than the safe integer limit', () => {
    expect(result(`Report${footer('9007199254740993')}`)!.metadata).toContainEqual({ label: 'Tokens', value: '9007199254740993' })
  })

  it.each([
    footer().replace('to: \'agent_child\'', 'to: \'another-child\''),
    `${footer()}\nUnrecognized suffix`,
    footer().replace('</usage>', ''),
  ])('preserves an incomplete or unrecognized footer (%s)', (text) => {
    expect(result(text)!.body).toBe(text)
    expect(result(text)!.metadata).toEqual([])
  })

  it('does not treat failed tool output as a successful agent result', () => {
    expect(result(`Error${footer()}`, false)).toMatchObject({ outcome: 'failed', body: `Error${footer()}`, metadata: [] })
  })

  it('uses the prompt for a native background launch', () => {
    expect(result(launch)).toMatchObject({ outcome: 'running', status: 'launched asynchronously', agentId: 'agent_child', body: 'Read the entry points.', bodyLabel: 'Prompt' })
  })

  it('keeps a completed report that quotes a background launch', () => {
    expect(result(launch + footer())).toMatchObject({ outcome: 'completed', body: launch })
  })

  it('does not claim that an agent completed when its result format is unknown', () => {
    const text = `${launch}\nNew provider information`
    expect(result(text)).toMatchObject({ outcome: 'unknown', body: text })
  })

  it('recovers a background output path from the native launch message', () => {
    const text = [
      ...launch.split('\n').slice(0, 3),
      'Do not duplicate this agent\'s work - avoid working with the same files or topics it is using. Work on non-overlapping tasks, or briefly tell the user what you launched and end your response.',
      'output_file: /project/agent.output',
      'Do NOT Read or tail this file via the shell tool. If the user asks for progress, say the agent is still running; you\'ll get a completion notification.',
    ].join('\n')
    const source = result(text)!
    expect(source.outcome).toBe('running')
    expect(source.metadata).toContainEqual({ label: 'Output', value: '/project/agent.output' })
    expect(source.body).toBe('Read the entry points.')
  })
})
