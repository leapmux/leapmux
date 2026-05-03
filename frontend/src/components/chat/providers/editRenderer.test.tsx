import type { MessageCategory } from '../messageClassification'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import './claude'
import './testMocks'

vi.mock('~/lib/shikiWorkerClient', () => ({
  tokenizeAsync: async (_lang: string, code: string) => code.split('\n').map(() => []),
}))

vi.mock('~/lib/tokenCache', () => ({
  getCachedTokens: () => null,
}))

const { renderMessageContent } = await import('../messageRenderers')

/** Build a Claude-style Edit tool_use message. */
function makeEditToolUse(input: Record<string, unknown>) {
  return {
    type: 'assistant',
    message: {
      content: [{
        type: 'tool_use',
        id: 'toolu_edit_1',
        name: 'Edit',
        input,
      }],
    },
  }
}

function renderEditToolUse(input: Record<string, unknown>) {
  const msg = makeEditToolUse(input)
  const toolUse = (msg.message.content as Array<Record<string, unknown>>)[0]
  const category: MessageCategory = {
    kind: 'tool_use',
    toolName: 'Edit',
    toolUse,
    content: msg.message.content as Array<Record<string, unknown>>,
  }
  const result = renderMessageContent(msg, undefined, category, AgentProvider.CLAUDE_CODE)
  return render(() => result)
}

describe('claude Edit tool_use rendering', () => {
  it('appends "(replace all)" next to diff stats when replace_all is true', () => {
    const { container } = renderEditToolUse({
      file_path: '/tmp/example.ts',
      old_string: 'const oldValue = 1;\n',
      new_string: 'const newValue = 1;\n',
      replace_all: true,
    })

    const text = container.textContent ?? ''
    expect(text).toContain('(replace all)')
  })

  it('does not render the diff body — diffs live on the result message', () => {
    const { container } = renderEditToolUse({
      file_path: '/tmp/example.ts',
      old_string: 'const beforeExpansion = true;\n',
      new_string: 'const afterExpansion = true;\n',
      replace_all: false,
    })

    const text = container.textContent ?? ''
    // Header surfaces the file path.
    expect(text).toContain('example.ts')
    // The old/new contents are not rendered on the tool_use side.
    expect(text).not.toContain('beforeExpansion')
    expect(text).not.toContain('afterExpansion')
  })
})
