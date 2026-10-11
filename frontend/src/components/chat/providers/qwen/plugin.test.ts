import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { createControlAnswerState } from '../../controls/types'
import { failedResult } from '../../model/toolCall'
import { acpTextContent, describeACPProviderBasics } from '../acp/testUtils'
import { providerFor } from '../registry'
import { input } from '../testUtils'

import './plugin'

vi.mock('~/api/workerRpc', () => ({
  updateAgentSettings: vi.fn(),
}))

/** Qwen's question dialog: a permission request that its `_meta` marks as one. */
const QUESTION = {
  jsonrpc: '2.0',
  id: 3,
  method: 'session/request_permission',
  params: {
    sessionId: 's',
    options: [{ optionId: 'proceed_once', name: 'Submit', kind: 'allow_once' }, { optionId: 'cancel', name: 'Cancel', kind: 'reject_once' }],
    toolCall: { toolCallId: 'c', kind: 'think', rawInput: { questions: [] }, _meta: { toolName: 'ask_user_question', qwenInteractionKind: 'user_question', qwenQuestions: [] } },
  },
}

describe('qwen provider', () => {
  const plugin = providerFor(AgentProvider.QWEN_CODE)!

  describeACPProviderBasics(AgentProvider.QWEN_CODE, { text: true, image: true, pdf: true, binary: false })

  it('states its own reasoning axis for the effort chip', () => {
    expect(plugin.configuration?.effortGroupKey).toBe('reasoning_effort')
  })

  it('carries plan mode and its approval modes on the permission-mode axis', () => {
    expect(plugin.configuration?.planMode).toBeDefined()
    expect(plugin.configuration?.triggerModeGroupKey).toBe('permissionMode')
    expect(plugin.controls?.permissionPresets).toEqual({
      smart: { sets: { permissionMode: 'auto' } },
      bypass: { sets: { permissionMode: 'yolo' } },
    })
  })

  it('recognizes its question dialog among its permission requests', () => {
    expect(plugin.controls?.askUserQuestion?.isRequest(QUESTION)).toBe(true)
    expect(plugin.controls?.askUserQuestion?.isRequest({ method: 'session/request_permission', params: { toolCall: { _meta: { toolName: 'edit' } } } })).toBe(false)
  })

  // Qwen's reply takes one string for each question, so a note and a choice are
  // alternatives.
  it('keeps a typed answer and a chosen option apart', () => {
    expect(plugin.controls?.preservesSelectionNotes).toBeUndefined()
  })

  describe('the end of a turn Qwen started', () => {
    it('draws a divider from the reason of _qwencode/end_turn', () => {
      const parent = { jsonrpc: '2.0', method: '_qwencode/end_turn', params: { sessionId: 's', reason: 'end_turn', source: 'goal' } }
      expect(plugin.transcript.classify(input(parent, null, AgentProvider.QWEN_CODE))).toEqual({ kind: 'result_divider' })
      expect(plugin.transcript.extractDivider(parent)).toEqual({ label: 'Turn ended' })
    })

    it('draws an interrupted turn', () => {
      expect(plugin.transcript.extractDivider({ method: '_qwencode/end_turn', params: { reason: 'cancelled' } })).toEqual({ label: 'Turn interrupted' })
    })

    it('qualifies a turn that ended for another reason', () => {
      expect(plugin.transcript.extractDivider({ method: '_qwencode/end_turn', params: { reason: 'max_tokens' } })).toEqual({ label: 'Turn ended (max_tokens)' })
    })

    // The empty reason is a turn end too, not the absence of one.
    it('draws a plain end for a notification that states no reason', () => {
      const parent = { jsonrpc: '2.0', method: '_qwencode/end_turn', params: { sessionId: 's' } }
      expect(plugin.transcript.classify(input(parent, null, AgentProvider.QWEN_CODE))).toEqual({ kind: 'result_divider' })
      expect(plugin.transcript.extractDivider(parent)).toEqual({ label: 'Turn ended' })
    })
  })

  // Qwen's question reply and its dismissal go through Qwen's own reply shape, under
  // the request id the worker gave the request.
  describe('the question dialog hooks', () => {
    const QUESTIONS = [{ question: 'Which color?', header: 'Color', options: [{ label: 'Red' }, { label: 'Blue' }] }]
    const ASKED = { ...QUESTION, params: { ...QUESTION.params, toolCall: { ...QUESTION.params.toolCall, _meta: { ...QUESTION.params.toolCall._meta, qwenQuestions: QUESTIONS } } } }

    async function sent(send: (respond: (bytes: Uint8Array) => Promise<void>) => Promise<void>): Promise<unknown[]> {
      const replies: unknown[] = []
      await send(async (bytes) => {
        replies.push(JSON.parse(new TextDecoder().decode(bytes)))
      })
      return replies
    }

    it('reads the questions of the dialog', () => {
      expect(plugin.controls?.askUserQuestion?.extractQuestions(ASKED).map(question => [question.header, question.multiSelect])).toEqual([['Color', false]])
    })

    it('sends the answers under Qwen\'s submit option', async () => {
      const handling = plugin.controls!.askUserQuestion!
      const request = { requestId: 'jsonrpc:3', agentId: 'a', payload: ASKED }
      const replies = await sent(respond => handling.sendAnswer(request, respond, handling.extractQuestions(ASKED), createControlAnswerState({ selections: { 0: ['Blue'] } })))
      expect(replies).toEqual([{ jsonrpc: '2.0', id: 'jsonrpc:3', result: { outcome: { outcome: 'selected', optionId: 'proceed_once' }, answers: { 0: 'Blue' } } }])
    })

    it('dismisses the dialog with Qwen\'s cancel option', async () => {
      const handling = plugin.controls!.askUserQuestion!
      const replies = await sent(respond => handling.sendReject({ requestId: 'jsonrpc:3', agentId: 'a', payload: ASKED }, respond, ''))
      expect(replies).toEqual([{ jsonrpc: '2.0', id: 'jsonrpc:3', result: { outcome: { outcome: 'selected', optionId: 'cancel' } } }])
    })
  })

  // Qwen's permission reply has no field for a reason, so the composer selects
  // Qwen's own reject option and the reason follows as a message of its own.
  it('answers an ordinary permission from the composer with Qwen\'s own options', () => {
    const shell = { method: 'session/request_permission', params: {
      options: [{ optionId: 'proceed_always', name: 'Always', kind: 'allow_always' }, { optionId: 'proceed_once', name: 'Allow', kind: 'allow_once' }, { optionId: 'cancel', name: 'Reject', kind: 'reject_once' }],
      toolCall: { toolCallId: 's', kind: 'execute', rawInput: { command: 'touch x' }, _meta: { toolName: 'run_shell_command' } },
    } }
    expect(plugin.controls?.buildControlResponse?.(shell, 'Use the other file', 'jsonrpc:9')).toEqual({ jsonrpc: '2.0', id: 'jsonrpc:9', result: { outcome: { outcome: 'selected', optionId: 'cancel' } } })
    expect(plugin.controls?.buildControlResponse?.(shell, '', 'jsonrpc:9')).toEqual({ jsonrpc: '2.0', id: 'jsonrpc:9', result: { outcome: { outcome: 'selected', optionId: 'proceed_once' } } })
    expect(plugin.controls?.controlFeedbackAsFollowUpMessage?.(shell)).toBe(true)
  })

  it('reads a plan approval as a plan, and an ordinary permission as a permission', () => {
    const plan = { method: 'session/request_permission', params: { options: [], toolCall: { toolCallId: 'p', kind: 'switch_mode', rawInput: { plan: '1. X' }, _meta: { toolName: 'exit_plan_mode' } } } }
    expect(plugin.controls?.extractControl?.({ payload: plan })).toEqual({ kind: 'plan', text: '1. X' })
    const edit = { method: 'session/request_permission', params: { options: [{ optionId: 'proceed_once', name: 'Allow', kind: 'allow_once' }], toolCall: { toolCallId: 'e', kind: 'edit', _meta: { toolName: 'edit' } } } }
    expect(plugin.controls?.extractControl?.({ payload: edit })?.kind).toBe('permission')
  })

  // The composer's text rejects a plan through the shared plan envelope, which the
  // worker turns into Qwen's cancel option, and its reason follows as a message.
  it('answers a plan approval from the composer with the neutral deny', () => {
    const plan = { method: 'session/request_permission', params: { options: [{ optionId: 'cancel', name: 'No', kind: 'reject_once' }], toolCall: { toolCallId: 'p', _meta: { toolName: 'exit_plan_mode' } } } }
    expect(plugin.controls?.buildControlResponse?.(plan, 'Split step 2', 'jsonrpc:5')).toEqual({
      type: 'control_response',
      response: { subtype: 'success', request_id: 'jsonrpc:5', response: { behavior: 'deny', message: 'Split step 2' } },
    })
    expect(plugin.controls?.controlFeedbackAsFollowUpMessage?.(plan)).toBe(false)
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
  const opening = { sessionUpdate: 'tool_call', toolCallId: 'call_write', status: 'pending', title: 'WriteFile', kind: 'edit', content: [], locations: [], rawInput: {}, _meta: { toolName: 'write_file', phase: 'preparing' } }
  const supplement = { sessionUpdate: 'tool_call', toolCallId: 'call_write', status: 'pending', rawInput: { file_path: '/w/notes.txt', content: 'proposed' }, locations: [{ path: '/w/notes.txt' }] }
  const ending = (text: string, status = 'failed') => ({ sessionUpdate: 'tool_call_update', toolCallId: 'call_write', status, content: acpTextContent(text), _meta: { toolName: 'write_file' } })
  const stored = (frame: Record<string, unknown>, supplementalContent?: Record<string, unknown>): ParsedMessageContent => ({ rawText: '', topLevel: frame, parentObject: frame, wrapper: null, supplementalContent })
  const refused = (text: string, status?: string) => providerToolCall(AgentProvider.QWEN_CODE, ending(text, status), { request: stored(opening, supplement), spanType: 'edit' })

  it('reads a refused write as declined, with its file and the refusal as the result', () => {
    const call = refused(REFUSAL)
    expect(call?.degradation).toBeUndefined()
    expect(call?.kind).toBe('write')
    expect(call?.status).toBe('declined')
    expect(call?.kind === 'write' ? call.request.changes.map(change => change.filePath) : []).toStrictEqual(['/w/notes.txt'])
    expect(call?.result).toStrictEqual(failedResult(REFUSAL))
    expect(call?.images).toStrictEqual([])
  })

  it('reads the refused request row as declined too', () => {
    const call = providerToolCall(AgentProvider.QWEN_CODE, opening, { role: 'request', spanType: 'edit', supplementalContent: supplement, result: stored(ending(REFUSAL)) })
    expect(call?.status).toBe('declined')
    expect(call?.kind === 'write' ? call.request.changes.map(change => change.filePath) : []).toStrictEqual(['/w/notes.txt'])
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
