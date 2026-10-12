import { describe, expect, it } from 'vitest'
import { lastUserText, matchesRequest } from '../helpers/mockModelScript'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { diracChildResultRule } from './childResult'

const TASK_MARKER = 'NATIVECHILDTASKCURRENT'

function matches(body: unknown, taskMarker = TASK_MARKER): boolean {
  const rule = diracChildResultRule(taskMarker, {
    toolCalls: [diracRespondToolCall('native-parent-complete', 'complete', 'The native parent completed.')],
  })
  return matchesRequest(rule.when, {
    protocol: 'openai-chat-completions',
    systemText: 'The native Dirac parent.',
    userText: lastUserText('openai-chat-completions', body),
    body,
  })
}

describe('diracChildResultRule', () => {
  it('rejects old child results combined with a new task marker in another message', () => {
    expect(matches({ messages: [
      { role: 'tool', tool_call_id: 'old-child', content: 'Subagent results:\nChild #1: completed - NATIVECHILDTASKOLD\nThe old child completed.' },
      { role: 'user', content: `Create ${TASK_MARKER} and report one word.` },
    ] })).toBe(false)
  })

  it('accepts the current child marker in its actual native result string', () => {
    expect(matches({ messages: [
      { role: 'user', content: 'Create the scripted native child.' },
      { role: 'tool', tool_call_id: 'current-child', content: `Subagent results:\nTotal: 1\nSucceeded: 1\nChild #2: Native held child · COMPLETED - ${TASK_MARKER} report one word.\nNATIVECHILDCOMPLETE` },
    ] })).toBe(true)
  })

  it('rejects a child request before its native result exists', () => {
    expect(matches({ messages: [
      { role: 'user', content: `${TASK_MARKER} report one word.` },
    ] })).toBe(false)
  })

  it('keeps escaped quotes and backslashes inside the current result string', () => {
    expect(matches({ messages: [
      { role: 'tool', tool_call_id: 'current-child', content: `Subagent results:\nThe child read "C:\\project\\file" before ${TASK_MARKER}.` },
    ] })).toBe(true)
    expect(matches({ messages: [
      { role: 'tool', tool_call_id: 'old-child', content: 'Subagent results:\nThe old child read "C:\\project\\file".' },
      { role: 'user', content: TASK_MARKER },
    ] })).toBe(false)
  })

  it('matches quote, backslash, and regex characters in the marker as literal text', () => {
    const marker = 'NATIVECHILDTASK"quoted"\\path[1].+?(a)|$'
    expect(matches({ messages: [
      { role: 'tool', tool_call_id: 'current-child', content: `Subagent results:\nThe child completed ${marker}.` },
    ] }, marker)).toBe(true)
    expect(matches({ messages: [
      { role: 'tool', tool_call_id: 'old-child', content: 'Subagent results:\nThe old child completed NATIVECHILDTASKquotedpath1ZZZZa.' },
    ] }, marker)).toBe(false)
    expect(matches({ messages: [
      { role: 'tool', tool_call_id: 'old-child', content: 'Subagent results:\nThe old child completed.' },
      { role: 'user', content: marker },
    ] }, marker)).toBe(false)
  })

  it('accepts the current native result inside an Anthropic tool-result block', () => {
    expect(matches({ messages: [
      { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'current-child', content: `Subagent results:\nThe child completed ${TASK_MARKER}.` }] },
    ] })).toBe(true)
  })

  it('rejects an absent or whitespace task marker', () => {
    expect(() => diracChildResultRule('', { text: 'A native parent answer.' })).toThrow('task marker')
    expect(() => diracChildResultRule('   ', { text: 'A native parent answer.' })).toThrow('task marker')
  })
})
