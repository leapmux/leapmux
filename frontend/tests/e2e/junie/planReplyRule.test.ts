import { describe, expect, it } from 'vitest'
import { lastUserText, matchesRequest, systemText } from '../helpers/mockModelScript'
import { JUNIE_PLAN_REPLY_RULE } from './planReplyRule'

/** The system prompt of Junie 26.9.22 for the reply to a pending plan, as the mock recorded it. */
const CLASSIFIER_SYSTEM = [
  'You are classifying a user\'s reply after they just saw a proposed plan.',
  'Reply YES if the user wants to start implementing the plan now (examples: "approve", "approved", "go ahead", "implement it", "do it", "ship it", "looks good, proceed", "yes").',
  'Reply DROP if the user wants to stop planning and leave plan mode without implementing this plan, or to abandon it and move on to something else (examples: "exit plan mode", "leave plan mode", "quit plan mode", "stop planning", "forget this plan", "scrap it", "discard the plan", "never mind, let\'s do something else").',
  'Reply NO otherwise: a request to change the plan, a side question, a clarification, hesitation, or any non-explicit approval.',
  '',
  'Leaving or exiting plan mode is DROP, never YES: it stops planning, it is not approval to implement the plan.',
  '',
  'Respond with EXACTLY one word: YES, DROP or NO. No punctuation, no explanation.',
].join('\n')

const SUMMARIZER_SYSTEM = 'You are a programming task description summarizer'

function chatRequest(system: string, user: string) {
  const body = { model: 'junie-e2e', temperature: 0, stream: false, messages: [{ role: 'system', content: system }, { role: 'user', content: user }] }
  return { protocol: 'openai-chat-completions' as const, systemText: systemText(body), userText: lastUserText(body), body }
}

describe('JUNIE_PLAN_REPLY_RULE', () => {
  it('matches the classifier request that Junie sends for a prompt after a pending plan', () => {
    const request = chatRequest(CLASSIFIER_SYSTEM, 'Plan the change.\n\nLEAPMUXE2ESCENARIO:test-1')
    expect(matchesRequest(JUNIE_PLAN_REPLY_RULE.when, request)).toBe(true)
  })

  it('answers with the verdict that keeps Junie planning', () => {
    const verdicts = /Respond with EXACTLY one word: ([A-Z]+), ([A-Z]+) or ([A-Z]+)\./.exec(CLASSIFIER_SYSTEM)?.slice(1)
    expect(verdicts).toEqual(['YES', 'DROP', 'NO'])
    const answer = JUNIE_PLAN_REPLY_RULE.respond.text ?? ''
    // YES approves the plan, and DROP leaves Plan mode. Only the third verdict changes the plan.
    expect(verdicts).toContain(answer)
    expect(['YES', 'DROP']).not.toContain(answer)
  })

  it('leaves the requests that carry a task for the ordered steps', () => {
    const planner = 'You are Junie, an autonomous planning assistant developed by JetBrains.\nYour job is to produce a Plan for the programming task.'
    const programmer = '## ENVIRONMENT\n  You are Junie, an autonomous programmer developed by JetBrains.'
    for (const system of [planner, programmer, SUMMARIZER_SYSTEM])
      expect(matchesRequest(JUNIE_PLAN_REPLY_RULE.when, chatRequest(system, 'Plan the change.'))).toBe(false)
  })

  it('ignores a user prompt that quotes the classifier text', () => {
    const request = chatRequest(SUMMARIZER_SYSTEM, 'You are classifying a user\'s reply after they just saw a proposed plan.')
    expect(matchesRequest(JUNIE_PLAN_REPLY_RULE.when, request)).toBe(false)
  })
})
