import type { TranscriptFrame } from '~/test-support/messageFactory'
import { describe, expect, it } from 'vitest'
import { MIMO_TOOL, MIMO_TOOL_STATUS } from '~/generated/contracts/mimo-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiToolResult, kimiToolStart } from '~/test-support/kimiFixtures'
import { makeTranscriptMessage } from '~/test-support/messageFactory'
import { openingFrame, toolFrame } from '~/test-support/mimoFixtures'
import { createTranscriptScenario } from '~/test-support/transcriptScenario'

// One request/result pair per protocol, through the whole scenario pipeline:
// the real parse, resolution, classification, span pairing, and row extraction.
// Each case states the four facts a protocol's pairing must hold — the span
// roles, the call kind, the row flags — and the one precedence rule that
// protocol's own status words follow. The calls keep their TYPED kind, which a
// degradation never does: the degrade always answers the generic row.
//
// Every Agent Client Protocol speaker shares one adapter, so OpenCode stands for
// the family here. The deviations of each family member (Cursor's names, Goose's
// `_meta`, Reasonix's envelope, and the tool identities of Grok Build, Qwen Code
// and Kiro) stay covered by that member's own extractor tests.

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
    label: 'Oh My Pi',
    provider: AgentProvider.OH_MY_PI,
    spanType: 'read',
    // omp 18.2.11's own pair (probe), path shortened.
    request: { type: 'tool_execution_start', toolCallId: 'omp-call', toolName: 'read', args: { path: 'notes.txt' } },
    result: { type: 'tool_execution_end', toolCallId: 'omp-call', toolName: 'read', result: { content: [{ type: 'text', text: '[notes.txt#C789]\n1:alpha one' }], details: { displayContent: { text: 'alpha one', startLine: 1 } } }, isError: false },
    kind: 'read',
    status: 'completed',
    // The start row reads the result the landed end frame states, so a running
    // call's card and a finished call's card state one answer.
    requestStatus: 'completed',
  },
  {
    label: 'Amp',
    provider: AgentProvider.AMP,
    spanType: 'shell_command',
    // The pair of a probe of the real CLI: a call the LeapMux helper refused with the
    // reader's reason.
    request: { type: 'assistant', message: { role: 'assistant', content: [{ type: 'tool_use', id: 'TU-034UC69EBKMhee2GCskCKE', name: 'shell_command', input: { command: 'printf b', workdir: '/work' } }], stop_reason: 'tool_use' }, session_id: 'T-1' },
    result: { type: 'user', message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'TU-034UC69EBKMhee2GCskCKE', content: 'Plugin error: the user rejected printf b\n', is_error: true }] }, session_id: 'T-1' },
    kind: 'execute',
    // The refusal's prefix states that the call never ran.
    status: 'declined',
  },
  {
    label: 'Cline',
    provider: AgentProvider.CLINE,
    spanType: 'run_commands',
    // The pair of a probe of the real daemon: a call the reader refused, whose error
    // closes with Cline's own rejection words.
    request: { version: 'v1', event: 'tool.started', sessionId: 's1', payload: { toolCallId: 'call_bash_2', toolName: 'run_commands', input: { commands: ['printf b'] } } },
    result: { version: 'v1', event: 'tool.finished', sessionId: 's1', payload: { toolCallId: 'call_bash_2', toolName: 'run_commands', output: { error: 'probe rejects -- NOT a tool or system failure. Clarify with user before proceeding.' }, error: '{"error":"probe rejects -- NOT a tool or system failure. Clarify with user before proceeding."}' } },
    kind: 'execute',
    // The rejection words state that the reader refused the call.
    status: 'declined',
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
  {
    label: 'Codewhale',
    provider: AgentProvider.CODEWHALE,
    spanType: 'bash',
    request: { event: 'item.started', payload: { item: { kind: 'tool_call', metadata: { tool_use_id: 'codewhale-call', tool_name: 'bash' } }, tool: { id: 'codewhale-call', name: 'bash', input: { command: 'printf codewhale' } } } },
    result: { event: 'item.failed', payload: { item: { kind: 'tool_call', detail: 'codewhale failed', metadata: { tool_use_id: 'codewhale-call', tool_name: 'bash' } } } },
    kind: 'execute',
    // The `item.failed` event is the failure, and the request row draws the landed
    // result too, so both rows state it.
    status: 'failed',
    requestStatus: 'failed',
  },
  {
    label: 'Kimi Code',
    provider: AgentProvider.KIMI_CODE,
    spanType: 'Bash',
    request: kimiToolStart('kimi-call', 'Bash', { command: 'printf kimi' }),
    result: kimiToolResult('kimi-call', 'kimi failed\nCommand failed with exit code: 1.', { isError: true }),
    kind: 'execute',
    // `isError` on the result is the failure the server reported, over the frame's
    // own completion.
    status: 'failed',
  },
  {
    label: 'MiMo',
    provider: AgentProvider.MIMO_CODE,
    spanType: MIMO_TOOL.Bash,
    request: openingFrame(MIMO_TOOL.Bash, { command: 'printf mimo' }, 'mimo-call'),
    result: toolFrame(MIMO_TOOL.Bash, { status: MIMO_TOOL_STATUS.Error, input: { command: 'printf mimo' }, error: 'Command exited with code 1', metadata: { output: 'mimo failed', exit: 1 } }, 'mimo-call'),
    kind: 'execute',
    // `error` is the final status word of the tool part, over the output the same
    // frame carries.
    status: 'failed',
    // Every MiMo frame states the whole call, so the request row reads the landed
    // final frame and states its status.
    requestStatus: 'failed',
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
