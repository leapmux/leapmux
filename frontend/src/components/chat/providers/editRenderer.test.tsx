import type { MessageCategory } from '../messageClassification'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import './claude/plugin'
import './testMocks'

vi.mock('~/lib/shikiWorkerClient', () => ({
  tokenizeAsync: async (_lang: string, code: string) => code.split('\n').map(() => []),
}))

vi.mock('~/lib/tokenCache', () => ({
  getCachedTokens: () => null,
  makeKey: (lang: string, code: string) => `${lang}\0${code}`,
}))

const { renderMessageContent } = await import('../rowRenderers')

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
  const category: MessageCategory = { kind: 'tool_use' }
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

  // The pending row draws the shared "Requested changes" card, which states the
  // substitution the call ASKS for and claims nothing about the file. The result
  // row replaces it with the diff that landed.
  it('states the substitution it requests, under the shared request heading', () => {
    const { container } = renderEditToolUse({
      file_path: '/tmp/example.ts',
      old_string: 'const beforeExpansion = true;\n',
      new_string: 'const afterExpansion = true;\n',
      replace_all: false,
    })

    const text = container.textContent ?? ''
    // Header surfaces the file path.
    expect(text).toContain('example.ts')
    expect(text).toContain('Requested changes')
    expect(text).toContain('beforeExpansion')
    expect(text).toContain('afterExpansion')
  })
})
