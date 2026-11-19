import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { failedResult } from '../../model/toolCall'
import { acpTextContent, describeACPProviderBasics } from '../acp/testUtils'
import { providerFor } from '../registry'
import { input } from '../testUtils'

import './plugin'

vi.mock('~/api/workerRpc', () => ({
  workerAgent: {
    updateAgentSettings: vi.fn(),
  },
}))

describe('qwen provider', () => {
  const plugin = providerFor(AgentProvider.QWEN_CODE)!

  describeACPProviderBasics(AgentProvider.QWEN_CODE, {
    text: true,
    image: true,
    pdf: true,
    audio: [
      'audio/wav',
      'audio/x-wav',
      'audio/wave',
      'audio/vnd.wave',
      'audio/mpeg',
      'audio/mp3',
      'audio/mp4',
      'audio/m4a',
      'audio/x-m4a',
      'audio/ogg',
      'audio/flac',
      'audio/x-flac',
    ],
    binary: false,
  })

  it('states its own reasoning axis for the effort chip', () => {
    expect(plugin.configuration?.effortGroupKey).toBe('reasoning_effort')
  })

  it('carries plan mode and its approval modes on the permission-mode axis', () => {
    expect(plugin.configuration?.planMode).toBeDefined()
    expect(plugin.configuration?.triggerModeGroupKey).toBe('permissionMode')
    expect(plugin.controls?.permissionPresets).toEqual({
      smart: {
        sets: { permissionMode: 'auto' },
      },
      bypass: {
        sets: { permissionMode: 'yolo' },
      },
    })
  })

  describe('the end of a turn Qwen started', () => {
    it('draws a divider from the reason of _qwencode/end_turn', () => {
      const parent = {
        jsonrpc: '2.0',
        method: '_qwencode/end_turn',
        params: { sessionId: 's', reason: 'end_turn', source: 'goal' },
      }
      expect(plugin.transcript.classify(input(parent, null, AgentProvider.QWEN_CODE))).toEqual({ kind: 'result_divider' })
      expect(plugin.transcript.extractDivider(parent)).toEqual({ label: 'Turn ended' })
    })

    it('draws an interrupted turn', () => {
      expect(plugin.transcript.extractDivider({
        method: '_qwencode/end_turn',
        params: { reason: 'cancelled' },
      })).toEqual({ label: 'Turn interrupted' })
    })

    it('qualifies a turn that ended for another reason', () => {
      expect(plugin.transcript.extractDivider({
        method: '_qwencode/end_turn',
        params: { reason: 'max_tokens' },
      })).toEqual({ label: 'Turn ended (max_tokens)' })
    })

    // The empty reason is a turn end too, not the absence of one.
    it('draws a plain end for a notification that states no reason', () => {
      const parent = {
        jsonrpc: '2.0',
        method: '_qwencode/end_turn',
        params: { sessionId: 's' },
      }
      expect(plugin.transcript.classify(input(parent, null, AgentProvider.QWEN_CODE))).toEqual({ kind: 'result_divider' })
      expect(plugin.transcript.extractDivider(parent)).toEqual({ label: 'Turn ended' })
    })
  })
})

/**
 * A call that the reader refused never ran.
 *
 * Qwen Code reads the Deny answer as the outcome `cancel`. It then sends a failed update.
 * The update has one content block: `Tool "<name>" was canceled by the user.`
 * (`stopAfterPermissionCancel` in its ACP session, 0.24.7). The update has no refusal
 * field. `qwen-code/plan-approval-banner.spec.ts` reads that frame from a live session.
 *
 * A write that the model STREAMED opens before the arguments arrive. The preparing frame
 * states the display name as its title, `rawInput: {}` and `locations: []`. The
 * permission request states the arguments and the file. The worker folds them into the
 * supplement of the stored request row (`conversation.notePermissionToolCall`).
 */
describe('qwen refused tool calls', () => {
  const REFUSAL = 'Tool "write_file" was canceled by the user.'
  const opening = {
    sessionUpdate: 'tool_call',
    toolCallId: 'call_write',
    status: 'pending',
    title: 'WriteFile',
    kind: 'edit',
    content: [],
    locations: [],
    rawInput: {},
    _meta: { toolName: 'write_file', phase: 'preparing' },
  }
  const supplement = {
    sessionUpdate: 'tool_call',
    toolCallId: 'call_write',
    status: 'pending',
    rawInput: { file_path: '/w/notes.txt', content: 'proposed' },
    locations: [
      { path: '/w/notes.txt' },
    ],
  }
  const ending = (text: string, status = 'failed') => ({
    sessionUpdate: 'tool_call_update',
    toolCallId: 'call_write',
    status,
    content: acpTextContent(text),
    _meta: { toolName: 'write_file' },
  })
  const stored = (frame: Record<string, unknown>, supplementalContent?: Record<string, unknown>): ParsedMessageContent => ({
    rawText: '',
    topLevel: frame,
    parentObject: frame,
    wrapper: null,
    supplementalContent,
  })
  const refused = (text: string, status?: string) => providerToolCall(AgentProvider.QWEN_CODE, ending(text, status), {
    request: stored(opening, supplement),
    spanType: 'edit',
  })

  it('reads a refused write as declined, with its file and the refusal as the result', () => {
    const call = refused(REFUSAL)
    expect(call?.degradation).toBeUndefined()
    expect(call?.kind).toBe('write')
    expect(call?.status).toBe('declined')
    expect(call?.kind === 'write'
      ? call.request.changes.map(change => change.filePath)
      : []).toStrictEqual(['/w/notes.txt'])
    expect(call?.result).toStrictEqual(failedResult(REFUSAL))
    expect(call?.images).toStrictEqual([])
  })

  // The request row reads its own frame alone, so it states the write that the call
  // asked for and knows nothing of the refusal. The result row states the refusal.
  it('reads the request row of a refused write as a write of its file that has not finished', () => {
    const call = providerToolCall(AgentProvider.QWEN_CODE, opening, {
      role: 'request',
      spanType: 'edit',
      supplementalContent: supplement,
      request: stored(opening, supplement),
    })
    expect(call?.degradation).toBeUndefined()
    expect(call?.status).toBe('pending')
    expect(call?.result).toBeUndefined()
    expect(call?.kind === 'write'
      ? call.request.changes.map(change => change.filePath)
      : []).toStrictEqual(['/w/notes.txt'])
  })

  it('needs the request for the refused result row alone', () => {
    const plugin = providerFor(AgentProvider.QWEN_CODE)!
    expect(plugin.transcript.needsRequest?.(input(opening, null, AgentProvider.QWEN_CODE))).toBe(false)
    expect(plugin.transcript.needsRequest?.(input(ending(REFUSAL), null, AgentProvider.QWEN_CODE))).toBe(true)
  })

  // A refusal is the WHOLE text. The same words inside a longer text are a failure of
  // the tool.
  it.each([
    ['words before the sentence', `Error: ${REFUSAL}`],
    ['words after the sentence', `${REFUSAL} Try again.`],
    ['a second line', `${REFUSAL}\nTry again.`],
    ['another sentence', 'Tool call was cancelled before execution.'],
  ])('keeps a failure with %s failed', (_case, text) => {
    expect(refused(text)?.status).toBe('failed')
  })

  // A call that ran and PRINTED the words is a call that completed.
  it('keeps a completed call that printed the refusal words completed', () => {
    expect(refused(REFUSAL, 'completed')?.status).toBe('completed')
  })
})
