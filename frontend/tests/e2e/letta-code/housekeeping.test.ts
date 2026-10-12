import { describe, expect, it } from 'vitest'
import { answerHousekeepingBesideContentRule, CONTENT_RULE_ANSWER } from '../helpers/housekeepingPriority'
import { MOCK_SESSION_TITLE } from '../helpers/mockModelScenario'
import { LETTA_TITLE_RULE } from './housekeeping'

describe('LETTA_TITLE_RULE', () => {
  // The system text holds "session title" and no sentence that the shared title rules match, so only this rule can
  // answer it.
  it('answers a title request that repeats the prompt, ahead of a content rule for the same prompt', async () => {
    const answers = await answerHousekeepingBesideContentRule([LETTA_TITLE_RULE], prompt => [
      { role: 'system', content: 'Reply with the session title of this conversation.' },
      { role: 'user', content: prompt },
    ])
    expect(answers).toEqual({ housekeeping: MOCK_SESSION_TITLE, content: CONTENT_RULE_ANSWER, ruleMatches: { 'title-letta': 1, 'content-rule': 1 } })
  })
})
