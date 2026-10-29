import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { classifyDeepseekHarnessMessage } from './classification'

describe('classifyDeepseekHarnessMessage', () => {
  it.each([
    'turn/start',
    'assistant/attempt',
    'request/context',
    'tool-workflow/run-start',
    'tool-workflow/run-end',
    'tool-workflow/agent-start',
    'tool-workflow/agent-end',
    'goal/change',
    'plan/mode',
    'subagent/catalog',
    'compaction/start',
  ])('draws a stored %s event as unknown', (type) => {
    expect(classifyDeepseekHarnessMessage(input({ type, data: {} }, null, AgentProvider.DEEPSEEK_HARNESS))).toEqual({ kind: 'unknown' })
  })

  it('draws a stored unsupported assistant block as unknown', () => {
    expect(classifyDeepseekHarnessMessage(input({
      type: 'assistant/message',
      blockIndex: 0,
      data: {
        message: {
          content: [
            { type: 'image' },
          ],
        },
      },
    }, null, AgentProvider.DEEPSEEK_HARNESS))).toEqual({ kind: 'unknown' })
  })
})
