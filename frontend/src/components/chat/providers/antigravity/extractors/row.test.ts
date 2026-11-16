import { describe, expect, it, vi } from 'vitest'
import { providerFor, resolveMessageForRendering } from '~/components/chat/providers/registry'
import { input } from '~/components/chat/providers/testUtils'
import { extractChatRow } from '~/components/chat/rowExtraction'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerRow, providerToolCall } from '~/test-support/toolCallFixture'
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

  it('parses the own tool argument object once', () => {
    const ownArguments = JSON.stringify({
      Path: '/p/a.ts',
      Content: 'own content'.repeat(10000),
    })
    const own = {
      type: 'CORTEX_STEP_TYPE_CODE_ACTION',
      status: 'CORTEX_STEP_STATUS_RUNNING',
      metadata: {
        toolCall: { id: 'same-call', name: 'write_file', argumentsJson: ownArguments },
      },
    }
    const parse = vi.spyOn(JSON, 'parse')
    try {
      const call = providerToolCall(PROVIDER, own, { role: 'request' })
      expect(JSON.stringify(call?.request)).toContain('own content'.repeat(10000))
      expect(parse.mock.calls.filter(args => args[0] === ownArguments)).toHaveLength(1)
    }
    finally {
      parse.mockRestore()
    }
  })

  describe('an open step whose finished step states more', () => {
    // The finished step of Antigravity repeats the tool call and adds what the run
    // reported: the working directory, the server name and the landed diff.
    const command = {
      type: 'CORTEX_STEP_TYPE_RUN_COMMAND',
      metadata: {
        toolCall: { id: 'run', name: 'run_command', argumentsJson: '{"CommandLine":"echo probe"}' },
      },
    }
    const running = { ...command, status: 'CORTEX_STEP_STATUS_RUNNING' }
    const finished = {
      ...command,
      status: 'CORTEX_STEP_STATUS_DONE',
      runCommand: {
        commandLine: 'echo probe',
        cwd: '/p',
        exitCode: 0,
        combinedOutput: { full: 'probe\n' },
      },
    }

    it('builds the request row from its own step, with no result and no finished status', () => {
      const row = providerRow(PROVIDER, running, {
        request: input(running, undefined, PROVIDER),
        role: 'request',
      })
      if (row?.kind !== 'tool' || row.call.kind !== 'execute')
        throw new Error('The open native command requires an execute row.')
      expect(row.role).toBe('request')
      expect(row.call.request).toEqual({ command: 'echo probe' })
      expect(row.call.status).toBe('in_progress')
      expect(row.call).not.toHaveProperty('result')
      expect(row.call.images).toEqual([])
    })

    // The Worker stores the open step again, with a completion, when the turn ends while
    // it runs. That copy is the final row of its call, so it draws the outcome.
    it('builds the closing copy of an open step as the result row', () => {
      const row = providerRow(PROVIDER, running, {
        completion: MessageCompletion.INTERRUPTED,
        role: 'result',
      })
      if (row?.kind !== 'tool')
        throw new Error('The closing copy requires a tool row.')
      expect(row.role).toBe('result')
      expect(row.call.status).toBe('cancelled')
    })

    // A finished step that states no working directory states none as a fact either.
    it('states no working directory for a finished step that reports none', () => {
      const { cwd: _cwd, ...run } = finished.runCommand
      const row = providerRow(PROVIDER, { ...finished, runCommand: run }, {
        request: input(running, undefined, PROVIDER),
        role: 'result',
      })
      if (row?.kind !== 'tool' || row.call.kind !== 'execute')
        throw new Error('The finished native command requires an execute row.')
      expect(row.call.request).toEqual({ command: 'echo probe' })
      expect(row.call.resultMetadata).toBeUndefined()
    })

    it('builds the result row from its own finished step', () => {
      const row = providerRow(PROVIDER, finished, {
        request: input(running, undefined, PROVIDER),
        role: 'result',
      })
      if (row?.kind !== 'tool' || row.call.kind !== 'execute')
        throw new Error('The finished native command requires an execute row.')
      expect(row.role).toBe('result')
      // Only the finished step states the working directory, so the row that draws the
      // result states it: beside its request row, it draws no request summary.
      expect(row.call.request).toEqual({ command: 'echo probe' })
      // A path, which the row shows relative to the working directory, as the request side
      // of a command does.
      expect(row.call.resultMetadata).toEqual([
        { label: 'Working directory', value: '/p', path: true },
      ])
      expect(row.call.status).toBe('completed')
      expect(row.call.result).toEqual({
        commands: [
          { output: 'probe\n', exitCode: 0 },
        ],
        unresolvedTerminals: [],
      })
    })

    it('reads no step of its span, so it needs no request', () => {
      expect(providerFor(PROVIDER)?.transcript.needsRequest).toBeUndefined()
    })
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
