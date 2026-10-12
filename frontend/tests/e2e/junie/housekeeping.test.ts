import { describe, expect, it } from 'vitest'
import { answerHousekeepingBesideContentRule, CONTENT_RULE_ANSWER } from '../helpers/housekeepingPriority'
import { MOCK_SESSION_TITLE } from '../helpers/mockModelScenario'
import { matchesRequest } from '../helpers/mockModelScript'
import { JUNIE_HOUSEKEEPING_RULES, junieCapabilityAnswer } from './housekeeping'

/** A Junie request whose system prompt opens with the given sentence. */
function turn(systemText: string) {
  return { protocol: 'openai-chat-completions' as const, path: '/v1/chat/completions', body: {}, systemText, userText: 'Reply once.' }
}

/** The names of the rules that match one request, in registration order. */
function matchingNames(systemText: string): string[] {
  return JUNIE_HOUSEKEEPING_RULES.filter(rule => matchesRequest(rule.when, turn(systemText))).map(rule => rule.name)
}

describe('JUNIE_HOUSEKEEPING_RULES', () => {
  // The model script accepts each rule name once, so a spec rule for another answer needs another name.
  it('gives each rule its own name', () => {
    const names = JUNIE_HOUSEKEEPING_RULES.map(rule => rule.name)
    expect(new Set(names).size).toBe(names.length)
  })

  it('answers the capability filter with no capability, the task namer with a title, and the task summarizer with both tags', () => {
    expect(JUNIE_HOUSEKEEPING_RULES.map(rule => [rule.when, rule.respond])).toEqual([
      [{ system: 'capability filter agent' }, { text: '' }],
      [{ system: 'task description summarizer' }, { text: MOCK_SESSION_TITLE }],
      [{ system: 'You are a task summarizer' }, { text: `<summary>The child task completed.</summary><title>${MOCK_SESSION_TITLE}</title>` }],
    ])
  })

  it('answers each housekeeping prompt through exactly its own rule', () => {
    expect(matchingNames('You are a capability filter agent. Select the capabilities that the request needs.')).toEqual(['junie-capability-filter'])
    expect(matchingNames('You are a programming task description summarizer')).toEqual(['junie-task-name'])
    expect(matchingNames('You are a task summarizer. Summarize the finished task.')).toEqual(['junie-task-summary'])
  })

  it('matches no rule on an ordinary user turn', () => {
    expect(matchingNames('You are Junie.')).toEqual([])
  })

  // A housekeeping request can repeat the text of the task, so a spec rule for the task matches it too.
  it.each([
    ['junie-capability-filter', 'You are a capability filter agent. Select the capabilities that the request needs.', ''],
    ['junie-task-name', 'You are a programming task description summarizer', MOCK_SESSION_TITLE],
    ['junie-task-summary', 'You are a task summarizer. Summarize the finished task.', `<summary>The child task completed.</summary><title>${MOCK_SESSION_TITLE}</title>`],
  ])('answers the %s request ahead of a content rule for the same task', async (name, system, answer) => {
    const answers = await answerHousekeepingBesideContentRule(JUNIE_HOUSEKEEPING_RULES, prompt => [
      { role: 'system', content: system },
      { role: 'user', content: prompt },
    ])
    expect(answers).toEqual({ housekeeping: answer, content: CONTENT_RULE_ANSWER, ruleMatches: { [name]: 1, 'content-rule': 1 } })
  })
})

describe('junieCapabilityAnswer', () => {
  // One registration lists the newer rule first, as a later registration of a spec places it.
  it('replaces the empty capability answer of the housekeeping rules, ahead of a content rule for the same task', async () => {
    const answers = await answerHousekeepingBesideContentRule([junieCapabilityAnswer('junie-mcp-capability', '1'), ...JUNIE_HOUSEKEEPING_RULES], prompt => [
      { role: 'system', content: 'You are a capability filter agent. Select the capabilities that the request needs.' },
      { role: 'user', content: prompt },
    ])
    expect(answers).toEqual({ housekeeping: '1', content: CONTENT_RULE_ANSWER, ruleMatches: { 'junie-mcp-capability': 1, 'content-rule': 1 } })
  })
})
