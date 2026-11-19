import { describe, expect, it } from 'vitest'
import { matchesRequest } from '../helpers/mockModelScript'
import { ZCODE_CHILD } from './childScenario'

const task = 'Use Bash to run printf child-proof, then report its bytes.'
const reminder = '<system-reminder>\n<total_tokens>14999998 tokens left</total_tokens>\n</system-reminder>'

describe('ZCODE_CHILD', () => {
  it('matches the child prompt before the native token-budget reminder', () => {
    expect(matchesRequest(ZCODE_CHILD.childTask(task), {
      protocol: 'openai-chat-completions',
      systemText: '',
      userText: reminder,
      body: {
        messages: [
          { role: 'user', content: task },
          { role: 'user', content: reminder },
        ],
      },
    })).toBe(true)
  })
  it('keeps the parent tool arguments separate from child user prompts', () => {
    expect(matchesRequest(ZCODE_CHILD.childTask(task), {
      protocol: 'openai-chat-completions',
      systemText: '',
      userText: reminder,
      body: {
        messages: [
          { role: 'user', content: 'Spawn one child.' },
          {
            role: 'assistant',
            tool_calls: [
              {
                id: 'spawn',
                function: {
                  name: 'Agent',
                  arguments: JSON.stringify({ prompt: task }),
                },
              },
            ],
          },
          { role: 'user', content: reminder },
        ],
      },
    })).toBe(false)
  })
})
