import { describe, expect, it } from 'vitest'
import { matchesRequest, systemText } from '../helpers/mockModelScript'
import { CODEX_MANUAL_APPROVAL_PREFIX, CODEX_REVIEWER_POLICY, codexAutoReviewRule, codexManualApproval } from './autoReview'

const verdict = { outcome: 'deny', risk_level: 'high', user_authorization: 'low', rationale: 'The fixture refuses the write.' } as const

/** A Responses request with the given developer rows and one user prompt. */
function responsesRequest(developerTexts: readonly string[], prompt = 'Run the command.') {
  return {
    protocol: 'openai-responses' as const,
    body: {
      instructions: 'You are Codex.',
      input: [
        ...developerTexts.map(text => ({ role: 'developer', content: [{ type: 'input_text', text }] })),
        { role: 'user', content: [{ type: 'input_text', text: prompt }] },
      ],
    },
  }
}

function matches(request: ReturnType<typeof responsesRequest>): boolean {
  const rule = codexAutoReviewRule('review', verdict)
  return matchesRequest(rule.when, { ...request, systemText: systemText(request.protocol, request.body), userText: '' })
}

describe('codexAutoReviewRule', () => {
  it('answers the reviewer with the verdict as JSON text', () => {
    const rule = codexAutoReviewRule('review', verdict)
    expect(rule.name).toBe('review')
    expect(JSON.parse(rule.respond.text!)).toEqual(verdict)
  })

  it('matches a request whose developer row holds the reviewer policy', () => {
    expect(matches(responsesRequest([`# Transcript provenance\n\n${CODEX_REVIEWER_POLICY}.\nAssess the exact action.`]))).toBe(true)
  })

  it('does not match a turn of the agent', () => {
    expect(matches(responsesRequest(['<permissions instructions>Approval policy is on-request.</permissions instructions>']))).toBe(false)
  })

  it('refuses an empty name and an empty rationale', () => {
    expect(() => codexAutoReviewRule(' ', verdict)).toThrow('needs a name')
    expect(() => codexAutoReviewRule('review', { ...verdict, rationale: ' ' })).toThrow('needs a rationale')
  })
})

describe('codexManualApproval', () => {
  it('reads the approval text from the prefix to the end of the system text', () => {
    const approval = `${CODEX_MANUAL_APPROVAL_PREFIX}\n\nApproved action:\n{\n  "action": {},\n  "outcome": "allowed"\n}`
    expect(codexManualApproval(responsesRequest(['First developer row.', approval]))).toBe(approval)
  })

  it('answers undefined for a request that states no approval', () => {
    expect(codexManualApproval(responsesRequest(['First developer row.']))).toBeUndefined()
  })
})
