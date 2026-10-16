import { describe, expect, it } from 'vitest'
import { matchesRequest } from '../helpers/mockModelScript'
import { QODER_REVIEWER_POLICY, qoderAutoReviewRule } from './autoReview'

describe('qoderAutoReviewRule', () => {
  it.each([false, true])('preserves the native shouldBlock value %s', (shouldBlock) => {
    const rule = qoderAutoReviewRule('review', shouldBlock, 'The fixture supplies this decision.')
    expect(JSON.parse(rule.respond.text!)).toEqual({ shouldBlock, reason: 'The fixture supplies this decision.' })
    expect(matchesRequest(rule.when, { protocol: 'openai-chat-completions', body: {}, systemText: QODER_REVIEWER_POLICY, userText: 'Inspect this tool.' })).toBe(true)
    expect(matchesRequest(rule.when, { protocol: 'openai-chat-completions', body: {}, systemText: 'You are the coding agent.', userText: QODER_REVIEWER_POLICY })).toBe(false)
  })

  it.each([['', 'Reason'], [' ', 'Reason'], ['review', ''], ['review', ' ']])('rejects an empty rule name or reason: %j, %j', (name, reason) => {
    expect(() => qoderAutoReviewRule(name, false, reason)).toThrow('requires a name and reason')
  })
})
