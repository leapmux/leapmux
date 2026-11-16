import type { TranscriptFrame } from '~/test-support/messageFactory'
import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { makeTranscriptMessage } from '~/test-support/messageFactory'
import { createTranscriptScenario } from '~/test-support/transcriptScenario'

function nativeExecFrame(id: string, result: boolean, completion?: MessageCompletion): TranscriptFrame {
  return {
    id,
    provider: AgentProvider.CODEX,
    spanId: 'native-exec-call',
    spanType: 'exec',
    agentSessionId: 'native-exec-session',
    content: {
      threadId: 'native-thread',
      turnId: 'native-turn',
      item: result
        ? {
            type: 'custom_tool_call_output',
            call_id: 'native-exec-call',
            output: [
              { type: 'input_text', text: 'Script completed\nWall time 0.0 seconds\nOutput:\n' },
              { type: 'input_text', text: 'Native computed: 42' },
            ],
          }
        : {
            type: 'custom_tool_call',
            call_id: 'native-exec-call',
            name: 'exec',
            input: 'text("Native computed: " + (40 + 2));',
            status: 'completed',
          },
    },
    ...(completion !== undefined
      ? { completion }
      : {}),
  }
}

describe('codex native exec pairing', () => {
  it('keeps a generated custom request in progress until its output arrives', () => {
    const scenario = createTranscriptScenario({
      archive: [
        makeTranscriptMessage(nativeExecFrame('native-request', false), 1n),
      ],
    })
    expect(scenario.toolRow('native-request')).toMatchObject({
      role: 'request',
      call: {
        id: 'native-exec-call',
        kind: 'execute',
        status: 'in_progress',
        request: { command: 'text("Native computed: " + (40 + 2));', language: 'javascript' },
      },
    })
  })

  it('joins one raw request and one raw output through the exact call ID', () => {
    const scenario = createTranscriptScenario({
      archive: [
        makeTranscriptMessage(nativeExecFrame('native-request', false), 1n),
        makeTranscriptMessage(nativeExecFrame('native-result', true), 2n),
      ],
    })
    // The request row reads its own call alone, so the output does not change it.
    expect(scenario.toolRow('native-request')).toMatchObject({
      role: 'request',
      call: { id: 'native-exec-call', kind: 'execute', status: 'in_progress' },
    })
    expect(scenario.toolRow('native-request').call.result).toBeUndefined()
    expect(scenario.toolRow('native-request').call.images).toEqual([])
    expect(scenario.toolRow('native-result')).toMatchObject({
      role: 'result',
      hasRequestRow: true,
      call: {
        id: 'native-exec-call',
        kind: 'execute',
        status: 'completed',
        request: { command: 'text("Native computed: " + (40 + 2));' },
        result: {
          commands: [
            {
              output: expect.stringContaining('Native computed: 42'),
            },
          ],
        },
      },
    })
  })

  it('keeps retained interruption over the model generation status', () => {
    const scenario = createTranscriptScenario({
      archive: [
        makeTranscriptMessage(nativeExecFrame('native-request', false, MessageCompletion.INTERRUPTED), 1n),
      ],
    })
    expect(scenario.toolRow('native-request').call).toMatchObject({ kind: 'execute', status: 'cancelled' })
  })
})

function collabFrame(id: string, item: Record<string, unknown>, completion?: MessageCompletion): TranscriptFrame {
  return {
    id,
    provider: AgentProvider.CODEX,
    spanId: 'shared-agent-call',
    spanType: 'collabAgentToolCall',
    agentSessionId: 'sess-1',
    content: { item, startedAtMs: 1 },
    ...(completion !== undefined
      ? { completion }
      : {}),
  }
}

/**
 * The span a `collabAgentToolCall` pair becomes, through the REAL parse,
 * resolution, classification, pairing, cache and extraction paths -- the same
 * route a mounted row takes, on the transcript scenario.
 *
 * Both rows keep the typed agent card. A degrade to the generic card draws the
 * prompt as raw JSON, and the e2e suite then finds no "Show prompt" button.
 */
describe('codex tool span pairing', () => {
  it('keeps both sides of a paired spawnAgent call at the agent kind, whatever the request frame still says', () => {
    const prompt = '**Instruction marker**\n\nRead the fixture and report the findings.'
    const report = '**Report marker**\n\n- First finding'
    const base = {
      id: 'shared-agent-call',
      type: 'collabAgentToolCall',
      tool: 'spawnAgent',
      prompt,
    }
    const scenario = createTranscriptScenario({
      archive: [
        makeTranscriptMessage(collabFrame('request', { ...base, status: 'inProgress', receiverThreadIds: [] }), 1n),
        makeTranscriptMessage(collabFrame('result', {
          ...base,
          status: 'completed',
          receiverThreadIds: ['child'],
          agentsStates: {
            child: { status: 'completed', message: report },
          },
        }), 2n),
      ],
    })
    // The request row keeps the agent kind and reads its own item alone: it states no
    // run and no target, because its own item lists no receiver yet.
    expect(scenario.toolRow('request')).toMatchObject({
      call: {
        kind: 'agent',
        status: 'in_progress',
        request: { description: 'Subagent', prompt, metadata: [] },
      },
      role: 'request',
    })
    expect(scenario.toolRow('request').call.result).toBeUndefined()
    expect(scenario.toolRow('request').call.request).not.toHaveProperty('registryKey')
    // The result row carries the target that only the closing item states.
    expect(scenario.toolRow('result')).toMatchObject({
      call: {
        kind: 'agent',
        status: 'completed',
        request: {
          prompt,
          metadata: [
            { label: 'Agent ID', value: 'child' },
          ],
          registryKey: 'child',
        },
        result: {
          agents: [
            { agentId: 'child', outcome: 'completed', body: report },
          ],
        },
      },
      role: 'result',
      hasRequestRow: true,
    })
  })

  // The span's own outcome word outranks BOTH the item word and the landed
  // result: a frame the reader interrupted still says `inProgress` in its own
  // bytes, and the completion column is what says the call never finished. The
  // row must stay cancelled -- `answered`-promotion to completed would resurrect
  // a call the reader stopped.
  it('keeps an interrupted completion over the frame\'s own in-progress word', () => {
    const scenario = createTranscriptScenario({
      archive: [
        makeTranscriptMessage(
          collabFrame('request', {
            id: 'shared-agent-call',
            type: 'collabAgentToolCall',
            tool: 'spawnAgent',
            prompt: 'Look',
            status: 'inProgress',
            receiverThreadIds: [],
          }, MessageCompletion.INTERRUPTED),
          1n,
        ),
      ],
    })
    const call = scenario.toolRow('request').call
    expect(call.kind).toBe('agent')
    expect(call.status).toBe('cancelled')
  })
})
