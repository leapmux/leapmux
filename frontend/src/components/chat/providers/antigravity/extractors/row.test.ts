import { describe, expect, it, vi } from 'vitest'
import { resolveMessageForRendering } from '~/components/chat/providers/registry'
import { input } from '~/components/chat/providers/testUtils'
import { extractChatRow } from '~/components/chat/rowExtraction'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { classifyAntigravityMessage } from '../classification'
import '~/components/chat/providers'

const PROVIDER = AgentProvider.ANTIGRAVITY_CLI

function extraction(payload: Record<string, unknown>) {
  const resolved = resolveMessageForRendering(input(payload, undefined, PROVIDER), PROVIDER)
  return extractChatRow(PROVIDER, resolved, classifyAntigravityMessage({ ...resolved, agentProvider: PROVIDER }))
}

describe('antigravityExtractRow', () => {
  it.each([
    '{broken',
    'null',
    '[]',
    '0',
    '"scalar"',
  ])('preserves the complete unsupported payload with arguments %s', (argumentsJson) => {
    const payload = {
      type: 'CORTEX_STEP_TYPE_GENERIC',
      status: 'CORTEX_STEP_STATUS_DONE',
      metadata: {
        toolCall: { id: 'future', name: 'future_tool', argumentsJson },
      },
      futurePayload: { text: 'keep this field' },
    }
    expect(extraction(payload)).toEqual({ kind: 'unsupported', payload, completion: null })
  })

  it('preserves an unknown native step and all of its fields', () => {
    const payload = {
      type: 'CORTEX_STEP_TYPE_FUTURE',
      status: 'CORTEX_STEP_STATUS_DONE',
      futurePayload: { text: 'retain me', count: '9007199254740993' },
    }
    expect(extraction(payload)).toEqual({ kind: 'unsupported', payload, completion: null })
  })

  it.each([undefined, '{}'])('keeps a valid no-argument generic call: %s', (argumentsJson) => {
    const payload = {
      type: 'CORTEX_STEP_TYPE_GENERIC',
      status: 'CORTEX_STEP_STATUS_DONE',
      metadata: {
        toolCall: {
          id: 'valid',
          name: 'future_tool',
          ...(argumentsJson === undefined
            ? {}
            : { argumentsJson }),
        },
      },
    }
    expect(extraction(payload).kind).toBe('row')
  })

  it('parses each own and paired tool argument object once', () => {
    const ownArguments = JSON.stringify({
      Path: '/p/a.ts',
      Content: 'own content'.repeat(10000),
    })
    const resultArguments = JSON.stringify({
      Path: '/p/a.ts',
      Content: 'result content'.repeat(10000),
    })
    const own = {
      type: 'CORTEX_STEP_TYPE_CODE_ACTION',
      status: 'CORTEX_STEP_STATUS_RUNNING',
      metadata: {
        toolCall: { id: 'same-call', name: 'write_file', argumentsJson: ownArguments },
      },
    }
    const result = {
      ...own,
      status: 'CORTEX_STEP_STATUS_DONE',
      metadata: {
        toolCall: { ...own.metadata.toolCall, argumentsJson: resultArguments },
      },
    }
    const parse = vi.spyOn(JSON, 'parse')
    try {
      const call = providerToolCall(PROVIDER, own, {
        result: input(result, undefined, PROVIDER),
      })
      expect(JSON.stringify(call?.request)).toContain('result content'.repeat(10000))
      expect(parse.mock.calls.filter(args => args[0] === ownArguments)).toHaveLength(1)
      expect(parse.mock.calls.filter(args => args[0] === resultArguments)).toHaveLength(1)
    }
    finally {
      parse.mockRestore()
    }
  })

  it('keeps a valid empty structured result typed', () => {
    const call = providerToolCall(PROVIDER, {
      type: 'CORTEX_STEP_TYPE_VIEW_FILE',
      status: 'CORTEX_STEP_STATUS_DONE',
      metadata: {
        toolCall: { id: 'empty', name: 'read_file', argumentsJson: '{"Path":"/p/empty.txt"}' },
      },
      viewFile: {},
      generic: {
        result: { result: 'generic text' },
      },
    })
    expect(call?.result).toEqual({ lines: [], fallbackContent: '' })
  })
})
