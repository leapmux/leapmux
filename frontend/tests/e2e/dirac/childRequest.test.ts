import { describe, expect, it } from 'vitest'
import { lastUserText, matchesRequest } from '../helpers/mockModelScript'
import { diracChildRequestMatcher } from './childRequest'

const TASK_MARKER = 'NATIVECHILDTASKCURRENT'

describe('diracChildRequestMatcher', () => {
  const task = `${TASK_MARKER} report one word.`
  function matchesChild(body: unknown) {
    return matchesRequest(diracChildRequestMatcher(task), {
      protocol: 'openai-chat-completions',
      systemText: '',
      userText: lastUserText('openai-chat-completions', body),
      body,
    })
  }

  it('accepts the child task as the current user input', () => {
    expect(matchesChild({
      messages: [
        { role: 'user', content: `<task>\n${task}\n</task>` },
      ],
    })).toBe(true)
  })

  it('refuses the parent history after a native spawn failure', () => {
    expect(matchesChild({
      messages: [
        { role: 'user', content: 'Create the scripted native child.' },
        {
          role: 'assistant',
          tool_calls: [
            {
              function: {
                arguments: JSON.stringify({ task_description: task }),
              },
            },
          ],
        },
        { role: 'tool', content: 'Execution failed: Subagent creation is disabled' },
      ],
    })).toBe(false)
  })

  it('refuses a task marker in unrelated request metadata', () => {
    expect(matchesChild({
      messages: [
        { role: 'user', content: 'Create a child.' },
      ],
      historicalTask: task,
    })).toBe(false)
  })

  it('refuses a missing task', () => {
    expect(() => diracChildRequestMatcher('')).toThrow('requires a task')
    expect(() => diracChildRequestMatcher('   ')).toThrow('requires a task')
  })
})
