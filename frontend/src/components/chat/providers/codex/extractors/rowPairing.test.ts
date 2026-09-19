import type { TranscriptFrame } from '~/test-support/messageFactory'
import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { makeTranscriptMessage } from '~/test-support/messageFactory'
import { createTranscriptScenario } from '~/test-support/transcriptScenario'

function collabFrame(id: string, item: Record<string, unknown>, completion?: MessageCompletion): TranscriptFrame {
  return {
    id,
    provider: AgentProvider.CODEX,
    spanId: 'shared-agent-call',
    spanType: 'collabAgentToolCall',
    agentSessionId: 'sess-1',
    content: { item, startedAtMs: 1 },
    ...(completion !== undefined ? { completion } : {}),
  }
}

/**
 * The span a `collabAgentToolCall` pair becomes, through the REAL parse,
 * resolution, classification, pairing, cache and extraction paths -- the same
 * route a mounted row takes, on the transcript scenario.
 *
 * A request row whose result side has landed carries it, so its own `inProgress`
 * word must not keep the envelope in progress: the validating builder refuses an
 * in-progress envelope over a result, and the degrade traded the typed agent card
 * for the generic one -- the request drew its prompt as raw JSON and the result
 * stood alone, which the e2e suite saw as a missing "Show prompt" button.
 */
describe('codex tool span pairing', () => {
  it('keeps both sides of a paired spawnAgent call the agent kind, whatever the opener frame still says', () => {
    const prompt = '**Instruction marker**\n\nRead the fixture and report the findings.'
    const report = '**Report marker**\n\n- First finding'
    const base = { id: 'shared-agent-call', type: 'collabAgentToolCall', tool: 'spawnAgent', prompt }
    const scenario = createTranscriptScenario({
      archive: [
        makeTranscriptMessage(collabFrame('request', { ...base, status: 'inProgress', receiverThreadIds: [] }), 1n),
        makeTranscriptMessage(collabFrame('result', { ...base, status: 'completed', receiverThreadIds: ['child'], agentsStates: { child: { status: 'completed', message: report } } }), 2n),
      ],
    })
    // The request row keeps the agent kind and the landed result side.
    expect(scenario.toolRow('request')).toMatchObject({ call: { kind: 'agent' }, role: 'request', hasResultRow: true })
    expect(scenario.toolRow('result')).toMatchObject({ call: { kind: 'agent' }, role: 'result', hasRequestRow: true })
  })

  // The span's own outcome word outranks BOTH the item word and the landed
  // result: a frame the reader interrupted still says `inProgress` in its own
  // bytes, and the completion column is what says the call never finished. The
  // row must stay cancelled -- `answered`-promotion to completed would resurrect
  // a call the reader stopped.
  it('keeps an interrupted completion over the frame\'s own in-progress word', () => {
    const scenario = createTranscriptScenario({
      archive: [makeTranscriptMessage(
        collabFrame('request', { id: 'shared-agent-call', type: 'collabAgentToolCall', tool: 'spawnAgent', prompt: 'Look', status: 'inProgress', receiverThreadIds: [] }, MessageCompletion.INTERRUPTED),
        1n,
      )],
    })
    const call = scenario.toolRow('request').call
    expect(call.kind).toBe('agent')
    expect(call.status).toBe('cancelled')
  })
})
