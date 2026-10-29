import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { COPILOT_PLAN_ENDED_NOTICE, isCopilotPlanTurn, latestCopilotModeNotice } from './modeSignals'

// The shapes below come from the model requests of Copilot CLI 1.0.105.
const SYSTEM_PROMPT = 'You are GitHub Copilot, an AI coding agent built by GitHub.'
const PLAN_NOTICE = '<mode_changed_notice>\n\n<plan_mode>\nWhen user messages are prefixed with [[PLAN]], you handle them in "plan mode".\n</plan_mode>\n\n</mode_changed_notice>'
const PLAN_ENDED_NOTICE = '<mode_changed_notice>\nPlan mode is no longer active. Do not follow plan-mode-only instructions from earlier in the conversation.\n</mode_changed_notice>'

function userTurn(text: string, plan: boolean): { role: 'user', content: string } {
  return { role: 'user', content: `<current_datetime>2026-10-29T11:33:07.632+09:00</current_datetime>\n\n${plan ? '[[PLAN]] ' : ''}${text}` }
}

function chatRequest(messages: readonly unknown[]): MockModelRequestRecord {
  return {
    protocol: 'openai-chat-completions',
    path: '/chat/completions',
    body: { messages },
  }
}

describe('isCopilotPlanTurn', () => {
  it('reads the plan prefix after the date line of the last user turn', () => {
    expect(isCopilotPlanTurn(chatRequest([
      { role: 'system', content: SYSTEM_PROMPT },
      userTurn('Plan the change.', true),
    ]))).toBe(true)
  })

  it('reads a turn without the prefix as a turn outside plan mode', () => {
    expect(isCopilotPlanTurn(chatRequest([
      { role: 'system', content: SYSTEM_PROMPT },
      userTurn('Make the change.', false),
    ]))).toBe(false)
  })

  it('reads the plan prefix from the text part of a user turn with an image', () => {
    const turn = userTurn('Plan the change in the screenshot.', true)
    expect(isCopilotPlanTurn(chatRequest([
      {
        role: 'user',
        content: [
          { type: 'text', text: turn.content },
          {
            type: 'image_url',
            image_url: { url: 'data:image/png;base64,iVBORw0KGgo=' },
          },
        ],
      },
    ]))).toBe(true)
  })

  it('ignores the prefix of an earlier user turn', () => {
    expect(isCopilotPlanTurn(chatRequest([
      userTurn('Plan the change.', true),
      { role: 'assistant', content: 'The plan is ready.' },
      userTurn('Make the change.', false),
    ]))).toBe(false)
  })

  it('ignores the prefix inside a line of the user text', () => {
    expect(isCopilotPlanTurn(chatRequest([
      userTurn('Explain what [[PLAN]] means.', false),
    ]))).toBe(false)
  })

  it('ignores the prefix that only the system prompt states', () => {
    expect(isCopilotPlanTurn(chatRequest([
      { role: 'system', content: `${SYSTEM_PROMPT}\n${PLAN_NOTICE}` },
      userTurn('Make the change.', false),
    ]))).toBe(false)
  })

  it('throws for a request without a user turn', () => {
    expect(() => isCopilotPlanTurn(chatRequest([
      { role: 'system', content: SYSTEM_PROMPT },
    ]))).toThrow('contains no user message')
  })
})

describe('latestCopilotModeNotice', () => {
  it('returns undefined for a session that never changed its mode', () => {
    expect(latestCopilotModeNotice(chatRequest([
      { role: 'system', content: SYSTEM_PROMPT },
      userTurn('Make the change.', false),
    ]))).toBeUndefined()
  })

  it('returns the trimmed body of the only notice', () => {
    expect(latestCopilotModeNotice(chatRequest([
      { role: 'system', content: `${SYSTEM_PROMPT}\n\n${PLAN_ENDED_NOTICE}` },
      userTurn('Make the change.', false),
    ])))
      .toBe('Plan mode is no longer active. Do not follow plan-mode-only instructions from earlier in the conversation.')
  })

  it('returns the last notice when the system prompt keeps an earlier one', () => {
    const notice = latestCopilotModeNotice(chatRequest([
      { role: 'system', content: `${SYSTEM_PROMPT}\n\n${PLAN_NOTICE}\n\n${PLAN_ENDED_NOTICE}` },
      userTurn('Make the change.', false),
    ]))
    expect(notice).toContain(COPILOT_PLAN_ENDED_NOTICE)
    expect(notice).not.toContain('<plan_mode>')
  })

  it('returns the plan notice while plan mode is active', () => {
    const notice = latestCopilotModeNotice(chatRequest([
      { role: 'system', content: `${SYSTEM_PROMPT}\n\n${PLAN_NOTICE}` },
      userTurn('Plan the change.', true),
    ]))
    expect(notice).toContain('<plan_mode>')
    expect(notice).not.toContain(COPILOT_PLAN_ENDED_NOTICE)
  })

  it('ignores a notice without its closing tag', () => {
    expect(latestCopilotModeNotice(chatRequest([
      { role: 'system', content: `${SYSTEM_PROMPT}\n<mode_changed_notice>\n${COPILOT_PLAN_ENDED_NOTICE}` },
      userTurn('Make the change.', false),
    ]))).toBeUndefined()
  })
})
