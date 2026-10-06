import { describe, expect, it } from 'vitest'
import { MOCK_SESSION_TITLE } from '../helpers/mockModelScenario'
import { matchesRequest } from '../helpers/mockModelScript'
import { JUNIE_HOUSEKEEPING_RULES } from './housekeeping'

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
})
