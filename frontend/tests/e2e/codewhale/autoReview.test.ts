import { describe, expect, it } from 'vitest'
import { matchesRequest } from '../helpers/mockModelScript'
import { codewhaleAutoReviewRule } from './autoReview'

describe('codewhaleAutoReviewRule', () => {
  it.each(['allow', 'deny'] as const)('encodes the native %s verdict and matches only the reviewer', (decision) => {
    const rule = codewhaleAutoReviewRule('review', decision, 'The fixture supplies this decision.')
    expect(JSON.parse(rule.respond.text!)).toEqual({ risk_level: decision === 'allow' ? 'low' : 'high', decision, reason: 'The fixture supplies this decision.' })
    expect(matchesRequest(rule.when, { protocol: 'openai-chat-completions', body: {}, systemText: 'You are the Auto-Review guardian for a coding agent. Inspect the operation.', userText: '' })).toBe(true)
    expect(matchesRequest(rule.when, { protocol: 'openai-chat-completions', body: {}, systemText: 'You are a coding agent.', userText: 'You are the Auto-Review guardian for a coding agent' })).toBe(false)
  })

  it.each([['', 'Reason'], [' ', 'Reason'], ['review', ''], ['review', ' ']])('rejects an empty rule name or reason: %j, %j', (name, reason) => {
    expect(() => codewhaleAutoReviewRule(name, 'allow', reason)).toThrow('requires a name and reason')
  })
})
