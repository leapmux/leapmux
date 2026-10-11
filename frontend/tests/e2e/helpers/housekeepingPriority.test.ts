import { describe, expect, it } from 'vitest'
import { answerHousekeepingBesideContentRule, CONTENT_RULE_ANSWER } from './housekeepingPriority'
import { MOCK_SESSION_TITLE } from './mockModelScenario'

describe('answerHousekeepingBesideContentRule', () => {
  it('answers a title request that repeats the prompt through the registered housekeeping title', async () => {
    const answers = await answerHousekeepingBesideContentRule([], prompt => [
      { role: 'system', content: 'Generate a short title for this conversation.' },
      { role: 'user', content: prompt },
    ])
    expect(answers).toEqual({
      housekeeping: MOCK_SESSION_TITLE,
      content: CONTENT_RULE_ANSWER,
      ruleMatches: { 'title-system': 1, 'content-rule': 1 },
    })
  })

  it('answers through a rule that the caller adds, ahead of the content rule', async () => {
    const answers = await answerHousekeepingBesideContentRule(
      [{ name: 'provider-summary', priority: 'high', when: { system: 'You summarize tasks' }, respond: { text: 'Summary' } }],
      prompt => [{ role: 'system', content: 'You summarize tasks.' }, { role: 'user', content: prompt }],
    )
    expect(answers).toEqual({ housekeeping: 'Summary', content: CONTENT_RULE_ANSWER, ruleMatches: { 'provider-summary': 1, 'content-rule': 1 } })
  })

  it('lets the content rule take a request that no housekeeping rule matches', async () => {
    const answers = await answerHousekeepingBesideContentRule([], prompt => [
      { role: 'system', content: 'You are a coding agent.' },
      { role: 'user', content: prompt },
    ])
    expect(answers).toEqual({ housekeeping: CONTENT_RULE_ANSWER, content: CONTENT_RULE_ANSWER, ruleMatches: { 'content-rule': 2 } })
  })
})
