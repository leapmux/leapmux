import { describe, expect, it } from 'vitest'
import { COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'
import { copilotExtractControl, copilotQuestions } from './extractControl'

function questionPayload(data: Record<string, unknown>) {
  return {
    jsonrpc: '2.0',
    method: 'session.event',
    params: {
      sessionId: 'session-1',
      event: {
        id: 'event-1',
        type: COPILOT_EVENT.UserInputRequested,
        data: {
          requestId: 'native-1',
          question: 'Which one?',
          choices: ['A', 'B'],
          ...data,
        },
      },
    },
  }
}

describe('copilotQuestions', () => {
  it('reads the question and its choices, and accepts an empty answer', () => {
    expect(copilotQuestions(questionPayload({ allowFreeform: true }))).toEqual([
      {
        question: 'Which one?',
        options: [
          { label: 'A' },
          { label: 'B' },
        ],
        allowEmpty: true,
      },
    ])
  })

  // The empty answer is the shortest answer that is not one of the choices, so a
  // request that refuses one refuses the other. The submit then waits for a choice.
  it.each([
    [
      'refuses one',
      { allowFreeform: false },
    ],
    ['states nothing', {}],
  ])('offers no empty answer when the request %s', (_label, data) => {
    expect(copilotQuestions(questionPayload(data))[0]?.allowEmpty).toBe(false)
  })
})

describe('copilotExtractControl', () => {
  it.each(['', 'future_code'])('keeps an unknown switch reason in the raw request only: %s', (errorCode) => {
    const control = copilotExtractControl({
      payload: {
        method: 'session.event',
        params: {
          event: {
            type: 'auto_mode_switch.requested',
            data: { requestId: 'switch', errorCode },
          },
        },
      },
    })
    if (control?.kind !== 'permission')
      throw new Error('Expected a permission card')
    expect(control.permission.reason).toBeUndefined()
    expect(control.permission.input).toBeUndefined()
  })

  it('extracts the auto-mode-switch card with three options', () => {
    const payload = {
      method: 'session.event',
      params: {
        sessionId: 'session',
        event: {
          type: 'auto_mode_switch.requested',
          data: { requestId: 'switch', errorCode: 'user_model_rate_limited' },
        },
      },
    }
    const control = copilotExtractControl({ payload })
    expect(control?.kind).toBe('permission')
    if (control?.kind !== 'permission')
      throw new Error('Expected a permission card')
    expect(control.permission.title).toBe('Copilot asks to switch to Auto')
    expect(control.permission.independentOptions).toBe(true)
    expect(control.permission.reason).toBe('The current model reached its rate limit.')
    expect(control.permission.input).toBeUndefined()
    expect(control.permission.options).toEqual([
      { optionId: 'once', kind: 'allow_once', name: 'Switch this turn' },
      { optionId: 'session', kind: 'allow_always', name: 'Always switch' },
      { optionId: 'reject', kind: 'reject_once', name: 'Decline' },
    ])
  })
})

describe('copilotExtractControl sampling', () => {
  it('extracts the sampling card', () => {
    const request = {
      messages: [
        {
          role: 'user',
          content: { type: 'text', text: 'Reply with SAMPLED.' },
        },
      ],
      maxTokens: 20,
    }
    const payload = {
      method: 'session.event',
      params: {
        event: {
          type: 'sampling.requested',
          data: { serverName: 'sampler', request },
        },
      },
    }
    expect(copilotExtractControl({ payload })).toEqual({
      kind: 'permission',
      permission: {
        title: 'The MCP server sampler asks the model to answer',
        input: request,
        options: [
          { optionId: 'once', kind: 'allow_once', name: 'Allow' },
          { optionId: 'reject', kind: 'reject_once', name: 'Deny' },
        ],
      },
    })
  })
})
