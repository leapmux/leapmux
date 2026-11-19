import { describe, expect, it } from 'vitest'
import { matchesRequest } from '../helpers/mockModelScript'
import { STEER_BEFORE_TOOL_ANSWER } from '../helpers/nativeToolSteering'
import { COPILOT_STEER_COMPLETION_RULE } from './steeringScenario'

const COMPLETION = '<system_notification>\nShell command "Run the scripted command" (shellId: 0) has completed successfully.\n</system_notification>'

/** The two observed native histories differ in whether the ordered answer precedes shell completion. */
function completionRequest(answered: boolean, userText = COMPLETION): Parameters<typeof matchesRequest>[1] {
  return {
    protocol: 'openai-chat-completions',
    systemText: 'You are GitHub Copilot.',
    userText,
    body: {
      messages: [
        { role: 'user', content: 'Run the scripted shell command, then reply.' },
        { role: 'user', content: 'Also include the word STEEREDWORD in your reply.' },
        ...(answered
          ? [
              { role: 'assistant', content: STEER_BEFORE_TOOL_ANSWER },
            ]
          : []),
        { role: 'user', content: userText },
        { role: 'tool', content: 'provider-steer-ready\n<shellId: 0 completed with exit code 0>' },
      ],
    },
  }
}

describe('COPILOT_STEER_COMPLETION_RULE', () => {
  it('handles completion after the ordered steering answer', () => {
    expect(matchesRequest(COPILOT_STEER_COMPLETION_RULE.when, completionRequest(true))).toBe(true)
  })

  it('leaves completion before the ordered answer for the queued step', () => {
    expect(matchesRequest(COPILOT_STEER_COMPLETION_RULE.when, completionRequest(false))).toBe(false)
  })

  it.each([
    '',
    COMPLETION.replace('Run the scripted command', 'Another command'),
    COMPLETION.replace('successfully.', 'with exit code 1.'),
    COMPLETION.replace('</system_notification>', ''),
  ])('refuses a different or incomplete notification: %j', (notification) => {
    expect(matchesRequest(COPILOT_STEER_COMPLETION_RULE.when, completionRequest(true, notification))).toBe(false)
  })
})
