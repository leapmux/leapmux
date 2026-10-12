import { describe, expect, it } from 'vitest'
import { answerHousekeepingBesideContentRule, CONTENT_RULE_ANSWER } from '../helpers/housekeepingPriority'
import { MOCK_SESSION_TITLE } from '../helpers/mockModelScenario'
import { DROID_TITLE_RULE } from './housekeeping'

describe('DROID_TITLE_RULE', () => {
  // The system text holds the fragment of the Droid title helper and no sentence that the shared title rules match,
  // so only this rule can answer it.
  it('answers a title request that repeats the prompt, ahead of a content rule for the same prompt', async () => {
    const answers = await answerHousekeepingBesideContentRule([DROID_TITLE_RULE], prompt => [
      { role: 'system', content: 'You are the helper that names session titles for a session picker.' },
      { role: 'user', content: prompt },
    ])
    expect(answers).toEqual({ housekeeping: MOCK_SESSION_TITLE, content: CONTENT_RULE_ANSWER, ruleMatches: { 'title-droid': 1, 'content-rule': 1 } })
  })
})
