import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { junieModelTurns } from './modelTurns'

function request(messages: unknown[]): MockModelRequestRecord {
  return { protocol: 'openai-chat-completions', path: '/chat/completions', body: { messages } }
}

describe('junieModelTurns', () => {
  it('splits the compressed previous exchange into its user and assistant turns, keeping the later rows in place', () => {
    const turns = junieModelTurns(request([
      { role: 'system', content: 'You are Junie.' },
      { role: 'user', content: '## CAPABILITIES CONTEXT\n\n### Custom Agents\n- leapmux-e2e-child: Read a local marker file.' },
      { role: 'user', content: 'History processor: During the current session, you have worked on the following `<previous_issue>`.\n<previous_issue>\nKeep RESUMEPROMPT for the stored session.\n</previous_issue>\n<previous_issue_solution>\nRESUMEANSWER\n</previous_issue_solution>' },
      { role: 'user', content: '## ISSUE DESCRIPTION\nReply to RESUMEDPROMPT in the reopened session.' },
      { role: 'user', content: '## PROJECT STRUCTURE\nProject root directory: /tmp/run\n\nNo entries have been found.' },
      'not a message',
    ]))
    expect(turns).toEqual([
      { role: 'user', text: 'Keep RESUMEPROMPT for the stored session.' },
      { role: 'assistant', text: 'RESUMEANSWER' },
      { role: 'user', text: '## ISSUE DESCRIPTION\nReply to RESUMEDPROMPT in the reopened session.' },
    ])
  })

  it('reads a history that no compression rewrote as plain user turns', () => {
    const turns = junieModelTurns(request([
      { role: 'user', content: 'Keep RESUMEPROMPT for the stored session.' },
      { role: 'user', content: 'Reply to RESUMEDPROMPT in the reopened session.' },
    ]))
    expect(turns).toEqual([
      { role: 'user', text: 'Keep RESUMEPROMPT for the stored session.' },
      { role: 'user', text: 'Reply to RESUMEDPROMPT in the reopened session.' },
    ])
  })

  it('keeps a user row that only mentions a context heading in prose', () => {
    const turns = junieModelTurns(request([
      { role: 'user', content: 'Read the `## PROJECT STRUCTURE` block and answer.' },
    ]))
    expect(turns).toEqual([{ role: 'user', text: 'Read the `## PROJECT STRUCTURE` block and answer.' }])
  })

  it('refuses a request of another protocol or without a message array', () => {
    expect(() => junieModelTurns({ protocol: 'anthropic-messages', path: '/v1/messages', body: {} })).toThrow('chat-completions')
    expect(() => junieModelTurns({ protocol: 'openai-chat-completions', path: '/chat/completions', body: { messages: 'not an array' } })).toThrow('no message array')
  })
})
