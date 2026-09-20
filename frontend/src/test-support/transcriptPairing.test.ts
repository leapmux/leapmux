import type { TranscriptFrame } from '~/test-support/messageFactory'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { makeTranscriptMessage } from '~/test-support/messageFactory'
import { createTranscriptScenario } from '~/test-support/transcriptScenario'

// One request/result pair per protocol, through the whole scenario pipeline:
// the real parse, resolution, classification, span pairing, and row extraction.
// Each case states the four facts a protocol's pairing must hold — the span
// roles, the call kind, the row flags — and the one precedence rule that
// protocol's own status words follow. The calls keep their TYPED kind, which a
// degradation never does: the degrade always answers the generic row.
//
// The Agent Client Protocol's five speakers share one adapter; OpenCode stands
// for the family here, and the ACP-family deviations (Cursor's names, Goose's
// `_meta`, Reasonix's envelope) stay covered by their own extractor tests.

const SESSION = 'pairing-session'

function frame(id: string, provider: AgentProvider, spanId: string, spanType: string | undefined, content: unknown): TranscriptFrame {
  return { id, provider, spanId, agentSessionId: SESSION, content, ...(spanType === undefined ? {} : { spanType }) }
}

/** The event envelope Copilot reaches the browser as. */
function copilotEvent(type: string, data: Record<string, unknown>): Record<string, unknown> {
  return { jsonrpc: '2.0', method: 'session.event', params: { sessionId: 'session-1', event: { id: `${type}-1`, type, data } } }
}

/** One protocol's pair and the status its own words must derive. */
interface PairCase {
  /** The protocol under test, as the suite names it. */
  label: string
  provider: AgentProvider
  spanType?: string
  request: unknown
  result: unknown
  /** The kind the call on both sides must keep. */
  kind: string
  /** The status the protocol's own outcome words must derive on the result side. */
  status: 'completed' | 'failed' | 'cancelled' | 'declined'
  /**
   * The status the REQUEST row's merged call derives, when the protocol promotes
   * a landed result onto the request frame. Absent states the request keeps its
   * own unfinished word, which most protocols do.
   */
  requestStatus?: 'completed' | 'failed' | 'cancelled' | 'declined' | 'incomplete'
}

const CASES: PairCase[] = [
  {
    label: 'Agent Client Protocol through OpenCode',
    provider: AgentProvider.OPENCODE,
    request: { sessionUpdate: 'tool_call', toolCallId: 'acp-call', kind: 'execute', title: 'Run command', status: 'pending', rawInput: { command: 'printf acp' } },
    result: { sessionUpdate: 'tool_call_update', toolCallId: 'acp-call', kind: 'execute', status: 'completed', content: [{ type: 'content', content: { type: 'text', text: 'acp output' } }] },
    kind: 'execute',
    // The update frame's own status word completes the call; the request's
    // 'pending' is a fact of the request half alone.
    status: 'completed',
  },
  {
    label: 'Claude',
    provider: AgentProvider.CLAUDE_CODE,
    spanType: 'Edit',
    request: { type: 'assistant', message: { content: [{ type: 'tool_use', id: 'claude-call', name: 'Edit', input: { file_path: '/project/a.ts', old_string: 'before', new_string: 'after' } }] } },
    result: { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'claude-call', content: 'Edit failed', is_error: true }] } },
    kind: 'edit',
    // `isError` on the tool_result block is the failure the frame states, over
    // whatever completion the turn recorded.
    status: 'failed',
  },
  {
    label: 'Codex',
    provider: AgentProvider.CODEX,
    request: { item: { type: 'commandExecution', id: 'codex-call', command: 'printf codex', status: 'inProgress' } },
    result: { item: { type: 'commandExecution', id: 'codex-call', command: 'printf codex', status: 'completed', aggregatedOutput: 'codex output' } },
    kind: 'execute',
    // The item's own status word: 'completed' with an aggregated output is a
    // finished call, and the request's 'inProgress' frame must not outvote it.
    status: 'completed',
    // The request frame carries no output of its own. The landed sibling makes the
    // row final, but it does not let this frame invent the sibling's result body.
    requestStatus: 'incomplete',
  },
  {
    label: 'Copilot',
    provider: AgentProvider.GITHUB_COPILOT,
    spanType: 'bash',
    request: copilotEvent('tool.execution_start', { toolCallId: 'copilot-call', toolName: 'bash', arguments: { command: 'printf copilot' } }),
    result: copilotEvent('tool.execution_complete', { toolCallId: 'copilot-call', success: false, error: { code: 'tool_error', message: 'copilot failed' } }),
    kind: 'execute',
    // `success !== true` on the completion event is the runtime's own fault
    // flag, over the landed result body.
    status: 'failed',
  },
  {
    label: 'Pi',
    provider: AgentProvider.PI,
    spanType: 'bash',
    request: { type: 'tool_execution_start', toolCallId: 'pi-call', toolName: 'bash', args: { command: 'printf pi' } },
    result: { type: 'tool_execution_end', toolCallId: 'pi-call', toolName: 'bash', isError: true, result: { content: [{ type: 'text', text: 'pi failed' }] } },
    kind: 'execute',
    // `isError` on the end event is the failure, over the result body the same
    // frame carries.
    status: 'failed',
  },
  {
    label: 'ZCode',
    provider: AgentProvider.ZCODE,
    spanType: 'Bash',
    request: { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'zcode-call', toolName: 'Bash', input: { command: 'printf zcode' } } },
    result: { type: 'tool.updated', payload: { kind: 'result', toolCallId: 'zcode-call', result: { success: false, content: 'zcode failed' } } },
    kind: 'execute',
    // `success: false` on the result payload is the failure the app-server
    // reported, over the frame's own completion.
    status: 'failed',
  },
]

describe('one request/result pair per protocol', () => {
  for (const testCase of CASES) {
    it(`pairs and derives status for ${testCase.label}`, () => {
      const scenario = createTranscriptScenario({
        archive: [
          makeTranscriptMessage(frame('request', testCase.provider, 'pair-call', testCase.spanType, testCase.request), 1n),
          makeTranscriptMessage(frame('result', testCase.provider, 'pair-call', testCase.spanType, testCase.result), 2n),
        ],
      })
      const requestRow = scenario.toolRow('request')
      const resultRow = scenario.toolRow('result')
      expect(requestRow.role, 'the request frame files as the span\'s request side').toBe('request')
      expect(resultRow.role, 'the closing frame files as the span\'s result side').toBe('result')
      expect(requestRow.hasResultRow).toBe(true)
      expect(resultRow.hasRequestRow).toBe(true)
      // No degradation: the degrade answers the generic row, so a typed kind on
      // both sides states the draft held every invariant -- and the metadata a
      // degrade would leave states none.
      expect(requestRow.call.kind, `the request call keeps the ${testCase.kind} kind`).toBe(testCase.kind)
      expect(resultRow.call.kind, `the result call keeps the ${testCase.kind} kind`).toBe(testCase.kind)
      expect(requestRow.call.degradation).toBeUndefined()
      expect(resultRow.call.degradation).toBeUndefined()
      // The merged call reads every side the span resolved, so both rows carry
      // the one status the protocol's own outcome words derive.
      expect(resultRow.call.status, `the protocol's own status words derive ${testCase.status}`).toBe(testCase.status)
      if (testCase.requestStatus !== undefined)
        expect(requestRow.call.status, 'the request row carries the landed result\'s status too').toBe(testCase.requestStatus)
    })
  }
})
