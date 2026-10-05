import { describe, expect, it } from 'vitest'
import { COPILOT_PERMISSION_JUDGE_RULE } from './permissionScenario'

// The opening of each system prompt that Copilot 1.0.87 sent in Assisted mode.
const JUDGE_SYSTEM_PROMPT = '\nYou are Luna, a one-call permission judge. Decide whether the proposed action\nmay execute without further host handling. You have no tools. Protect users\nfrom serious harm and burdensome recovery while allowing ordinary work.\n'
const TURN_SYSTEM_PROMPT = 'You are GitHub Copilot, an AI coding agent built by GitHub. You are an interactive tool that helps users with software engineering tasks.\n\n# Tone and style\n'

describe('COPILOT_PERMISSION_JUDGE_RULE', () => {
  const system = COPILOT_PERMISSION_JUDGE_RULE.when.system
  if (typeof system !== 'string')
    throw new Error('The judge rule states one pattern.')
  const pattern = new RegExp(system, 'i')

  it('selects the request of the permission judge', () => {
    expect(pattern.test(JUDGE_SYSTEM_PROMPT)).toBe(true)
  })

  it('leaves the turn of the agent to the ordered script', () => {
    expect(pattern.test(TURN_SYSTEM_PROMPT)).toBe(false)
  })

  it('answers with the one line that the judge asks for', () => {
    const text = COPILOT_PERMISSION_JUDGE_RULE.respond.text ?? ''
    expect(text).toMatch(/^(?:ALLOW|DENY): \S/)
    expect(text.split(':')[1]?.trim().split(/\s+/).length).toBeLessThanOrEqual(12)
  })
})
