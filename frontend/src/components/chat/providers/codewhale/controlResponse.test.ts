import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { CODEWHALE_ANSWER_TEXT, CODEWHALE_TOOL } from '~/generated/contracts/codewhale-protocol'
import { codewhaleControlResponseSummary } from './controlResponse'
import { approvalPayload, questionPayload } from './controls.fixtures'

function saved(request: Record<string, unknown>, response: Record<string, unknown> | undefined): PersistedControlResponse {
  return { requestId: String(request.request_id), claimToken: 'claim', request, response }
}

describe('codewhaleControlResponseSummary', () => {
  it('reads an approval as the word its button carried', () => {
    expect(codewhaleControlResponseSummary(saved(approvalPayload(CODEWHALE_TOOL.Bash, {}), { frame: 'approval', approval_id: 'ap1', decision: 'allow' })))
      .toStrictEqual({ kind: 'label', text: 'Allow' })
    expect(codewhaleControlResponseSummary(saved(approvalPayload(CODEWHALE_TOOL.Bash, {}), { frame: 'approval', approval_id: 'ap1', decision: 'deny' })))
      .toStrictEqual({ kind: 'label', text: 'Deny' })
    expect(codewhaleControlResponseSummary(saved(approvalPayload(CODEWHALE_TOOL.Bash, {}), { frame: 'approval', decision: 'escalate' }))).toBeNull()
  })

  it('reads the answers of a question, question by question', () => {
    const response = { frame: 'user_input', thread_id: 't', input_id: 'q1', answers: [{ id: 'color', label: 'Red', value: 'Red' }, { id: 'sizes', label: 'S', value: 'S' }, { id: 'sizes', label: 'M', value: 'M' }] }
    expect(codewhaleControlResponseSummary(saved(questionPayload(), response))).toStrictEqual({ kind: 'label', text: 'Which color?: Red\nWhich sizes?: S, M' })
  })

  it('answers null for a question the answers do not address', () => {
    expect(codewhaleControlResponseSummary(saved(questionPayload(), { frame: 'user_input', answers: [] }))).toBeNull()
    expect(codewhaleControlResponseSummary(saved(questionPayload(), { frame: 'user_input', answers: [{ id: 'unasked', label: 'X', value: 'X' }] }))).toBeNull()
    expect(codewhaleControlResponseSummary(saved(questionPayload(), { frame: 'user_input' }))).toBeNull()
  })

  it('leaves out a question whose answers hold only blank values', () => {
    const response = { frame: 'user_input', answers: [{ id: 'color', label: 'Other', value: '  ' }, { id: 'sizes', label: 'S', value: 'S' }] }
    expect(codewhaleControlResponseSummary(saved(questionPayload(), response))).toStrictEqual({ kind: 'label', text: 'Which sizes?: S' })
  })

  // The questions come from the saved request, so a saved answer with no request
  // has no question to put its answers under.
  it('answers null for answers whose request was not saved', () => {
    const response = { frame: 'user_input', answers: [{ id: 'color', label: 'Red', value: 'Red' }] }
    expect(codewhaleControlResponseSummary({ requestId: 'user_input:q1', claimToken: 'claim', request: undefined, response })).toBeNull()
    expect(codewhaleControlResponseSummary({ requestId: 'user_input:q1', claimToken: 'claim', request: undefined, response: { ...response, declined: true } }))
      .toStrictEqual({ kind: 'label', text: 'Declined' })
  })

  it('states the answers in the order the runtime asked, whatever order the frame lists them in', () => {
    const response = { frame: 'user_input', answers: [{ id: 'sizes', label: 'M', value: 'M' }, { id: 'color', label: 'Blue', value: 'Blue' }] }
    expect(codewhaleControlResponseSummary(saved(questionPayload(), response))).toStrictEqual({ kind: 'label', text: 'Which color?: Blue\nWhich sizes?: M' })
  })

  it('reads a decline and the reason it carried', () => {
    const declined = (value: string) => ({ frame: 'user_input', declined: true, answers: [{ id: 'color', label: 'Other', value }, { id: 'sizes', label: 'Other', value }] })
    expect(codewhaleControlResponseSummary(saved(questionPayload(), declined(CODEWHALE_ANSWER_TEXT.Declined)))).toStrictEqual({ kind: 'label', text: 'Declined' })
    expect(codewhaleControlResponseSummary(saved(questionPayload(), declined('User stopped')))).toStrictEqual({ kind: 'label', text: 'Declined\nUser stopped' })
    expect(codewhaleControlResponseSummary(saved(questionPayload(), { frame: 'user_input', declined: true }))).toStrictEqual({ kind: 'label', text: 'Declined' })
  })

  it('reads a decline\'s reason from the first question that carries one', () => {
    const response = { frame: 'user_input', declined: true, answers: [{ id: 'sizes', label: 'Other', value: 'Not now' }] }
    expect(codewhaleControlResponseSummary(saved(questionPayload(), response))).toStrictEqual({ kind: 'label', text: 'Declined\nNot now' })
  })

  // Only a boolean `true` marks a decline. Any other value is an answer frame.
  it('reads a frame whose declined flag is not true as answers', () => {
    const response = { frame: 'user_input', declined: 'true', answers: [{ id: 'color', label: 'Red', value: 'Red' }] }
    expect(codewhaleControlResponseSummary(saved(questionPayload(), response))).toStrictEqual({ kind: 'label', text: 'Which color?: Red' })
  })

  it('answers null for an approval frame that states no decision', () => {
    expect(codewhaleControlResponseSummary(saved(approvalPayload(CODEWHALE_TOOL.Bash, {}), { frame: 'approval', approval_id: 'ap1' }))).toBeNull()
  })

  it('answers null for a frame it does not know', () => {
    expect(codewhaleControlResponseSummary(saved(approvalPayload(CODEWHALE_TOOL.Bash, {}), { frame: 'interrupt' }))).toBeNull()
    expect(codewhaleControlResponseSummary(saved(approvalPayload(CODEWHALE_TOOL.Bash, {}), undefined))).toBeNull()
  })
})
