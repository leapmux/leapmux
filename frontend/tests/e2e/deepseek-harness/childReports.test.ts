import { describe, expect, it } from 'vitest'
import { matchesRequest } from '../helpers/mockModelScript'
import { deepseekHarnessChildReportRule } from './childReports'

describe('deepseekHarnessChildReportRule', () => {
  function request(text: string, role = 'user') {
    return { protocol: 'anthropic-messages' as const, systemText: '', userText: text, body: { messages: [{ role, content: [{ type: 'text', text }] }] } }
  }

  it.each(['finished and will do no further work unless you send it more.', 'was stopped before it finished.'])('matches the exact native report: %s', (ending) => {
    const rule = deepseekHarnessChildReportRule('native-child')
    expect(matchesRequest(rule.when, request(`Background subagent native-child ${ending}\nIts closing message:\nThe actual child result.`))).toBe(true)
  })

  it('rejects another child and an assistant that quotes the report', () => {
    const rule = deepseekHarnessChildReportRule('native-child')
    expect(matchesRequest(rule.when, request('Background subagent other-child was stopped before it finished.'))).toBe(false)
    expect(matchesRequest(rule.when, request('Background subagent native-child was stopped before it finished.', 'assistant'))).toBe(false)
    expect(matchesRequest(rule.when, request('Quoted: Background subagent native-child was stopped before it finished.'))).toBe(false)
  })

  it('escapes a native identity and rejects absent identities', () => {
    const rule = deepseekHarnessChildReportRule('child.+')
    expect(matchesRequest(rule.when, request('Background subagent child.+ was stopped before it finished.'))).toBe(true)
    expect(matchesRequest(rule.when, request('Background subagent child-123 was stopped before it finished.'))).toBe(false)
    expect(() => deepseekHarnessChildReportRule('')).toThrow('exact native Session identity')
    expect(() => deepseekHarnessChildReportRule('child\0')).toThrow('exact native Session identity')
  })
})
