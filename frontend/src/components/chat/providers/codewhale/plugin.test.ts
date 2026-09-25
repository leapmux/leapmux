import type { ControlRequest } from '~/stores/control.store'
import { describe, expect, it } from 'vitest'
import { CODEWHALE_BLOCK_TYPE, CODEWHALE_DEFAULT_MODE, CODEWHALE_ITEM_KIND, CODEWHALE_MODE, CODEWHALE_OPTION, CODEWHALE_POSTURE, CODEWHALE_TOOL, CODEWHALE_TRANSCRIPT_ROLE, CODEWHALE_TURN_STATUS } from '~/generated/contracts/codewhale-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { makeTranscriptMessage } from '~/test-support/messageFactory'
import { createTranscriptScenario } from '~/test-support/transcriptScenario'
import { CONTROL_REJECTED_BY_USER_MESSAGE } from '~/utils/controlResponse'
import { createControlAnswerState } from '../../controls/types'
import { providerFor } from '../registry'
import { codewhaleIsQuestionRequest, codewhaleQuestionsFromPayload } from './askUserQuestion'
import { classifyCodewhaleMessage } from './classification'
import { codewhaleControlResponseSummary } from './controlResponse'
import { approvalPayload, questionPayload, QUESTIONS } from './controls.fixtures'
import { codewhaleExtractControl } from './extractControl'
import { codewhaleNotificationEntry } from './extractors/notification'
import { codewhaleResultDivider } from './extractors/resultDivider'
import { codewhaleExtractRow } from './extractors/row'
import { codewhaleConfiguration } from './pluginConfiguration'
import { codewhaleControls } from './pluginControls'
import { codewhaleRelatedMessages, codewhaleSpanRole } from './spanRole'
import { CALL, childBlock, itemFinished, toolCompleted, toolStarted, turnCompleted } from './toolResults.fixtures'
import '~/components/chat/providers'

const plugin = providerFor(AgentProvider.CODEWHALE)!
const SESSION = 'thr-1'

/** One stored message of this provider, in a span when it states one. */
function message(id: string, content: Record<string, unknown>, seq: bigint, span: { spanId?: string, spanType?: string, completion?: MessageCompletion } = {}) {
  return makeTranscriptMessage({ id, provider: AgentProvider.CODEWHALE, agentSessionId: SESSION, content, ...span }, seq)
}

/** The bytes one control capability sends, decoded. */
async function sent(send: (respond: (content: Uint8Array) => Promise<void>) => Promise<void>): Promise<unknown> {
  let captured: Uint8Array | undefined
  await send(async (content) => {
    captured = content
  })
  return JSON.parse(new TextDecoder().decode(captured))
}

function request(payload: Record<string, unknown>): ControlRequest {
  return { requestId: String(payload.request_id), agentId: 'agent-1', payload }
}

describe('codewhale plugin', () => {
  // Identity, not presence: a hook wired to the wrong reader is defined too.
  it('is registered for the CODEWHALE provider with each hook wired to its own reader', () => {
    expect(plugin.transcript).toStrictEqual({
      spanRole: codewhaleSpanRole,
      relatedMessages: codewhaleRelatedMessages,
      classify: classifyCodewhaleMessage,
      extractRow: codewhaleExtractRow,
      notificationEntry: codewhaleNotificationEntry,
      extractDivider: codewhaleResultDivider,
    })
    expect(plugin.controls).toBe(codewhaleControls)
    expect(plugin.configuration).toBe(codewhaleConfiguration)
    expect(plugin.controls?.controlResponseDisplay).toBe(codewhaleControlResponseSummary)
    expect(plugin.controls?.extractControl).toBe(codewhaleExtractControl)
    expect(plugin.controls?.askUserQuestion?.isRequest).toBe(codewhaleIsQuestionRequest)
    expect(plugin.controls?.askUserQuestion?.extractQuestions).toBe(codewhaleQuestionsFromPayload)
  })

  // A turn takes text and inline images, and has no file field.
  it('accepts text and images, and refuses a PDF and any other binary', () => {
    expect(plugin.configuration?.attachments).toStrictEqual({ text: true, image: true, pdf: false, binary: false })
  })

  it('drives the plan toggle and the trigger segment from the thread mode', () => {
    const configuration = plugin.configuration!
    expect(configuration.triggerModeGroupKey).toBe(CODEWHALE_OPTION.Mode)
    expect(configuration.planMode?.groupKey).toBe(CODEWHALE_OPTION.Mode)
    expect(configuration.planMode?.planValue).toBe(CODEWHALE_MODE.Plan)
    expect(configuration.planMode?.defaultValue).toBe(CODEWHALE_DEFAULT_MODE)
    expect(configuration.planMode?.currentMode({})).toBe(CODEWHALE_DEFAULT_MODE)
    expect(configuration.planMode?.currentMode({ optionValues: {} })).toBe(CODEWHALE_DEFAULT_MODE)
    expect(configuration.planMode?.currentMode({ optionValues: { permissionMode: CODEWHALE_POSTURE.FullAccess } })).toBe(CODEWHALE_DEFAULT_MODE)
    expect(configuration.planMode?.currentMode({ optionValues: { [CODEWHALE_OPTION.Mode]: CODEWHALE_MODE.Plan } })).toBe(CODEWHALE_MODE.Plan)
  })

  it('maps the two permission presets onto the runtime\'s postures', () => {
    expect(plugin.controls?.permissionPresets).toStrictEqual({
      smart: { sets: { permissionMode: CODEWHALE_POSTURE.AutoReview } },
      bypass: { sets: { permissionMode: CODEWHALE_POSTURE.FullAccess } },
    })
  })
})

describe('codewhale transcript through the whole pipeline', () => {
  it('pairs a command\'s two rows into one call', () => {
    const scenario = createTranscriptScenario({
      archive: [
        message('request', toolStarted(CODEWHALE_TOOL.Bash, { command: 'ls' }), 1n, { spanId: CALL, spanType: CODEWHALE_TOOL.Bash }),
        message('result', toolCompleted(CODEWHALE_TOOL.Bash, { command: 'ls' }, 'a.ts\n', { exit_code: 0 }), 2n, { spanId: CALL, spanType: CODEWHALE_TOOL.Bash }),
      ],
    })
    const requestRow = scenario.toolRow('request')
    const resultRow = scenario.toolRow('result')
    expect(requestRow.role).toBe('request')
    expect(resultRow.role).toBe('result')
    expect(requestRow.hasResultRow).toBe(true)
    expect(resultRow.hasRequestRow).toBe(true)
    expect(resultRow.call.kind).toBe('execute')
    expect(resultRow.call.status).toBe('completed')
    expect(requestRow.call.status).toBe('completed')
  })

  it('leaves the result row of a deferred tool\'s first call out, and states the call on its request row', () => {
    const words = 'Tool `apply_patch` was deferred and has now been loaded.'
    const scenario = createTranscriptScenario({
      archive: [
        message('first-request', toolStarted(CODEWHALE_TOOL.ApplyPatch, { patch: 'x' }), 1n, { spanId: CALL, spanType: CODEWHALE_TOOL.ApplyPatch }),
        message('first-result', toolCompleted(CODEWHALE_TOOL.ApplyPatch, { patch: 'x' }, words, { deferred_tool_loaded: true }), 2n, { spanId: CALL, spanType: CODEWHALE_TOOL.ApplyPatch }),
      ],
    })
    // A classified-hidden row leaves the transcript: the entry list holds no row for it.
    expect(() => scenario.entry('first-result')).toThrow('No classified entry for "first-result"')
    expect(scenario.toolRow('first-request').call.result).toStrictEqual({ unparsed: true, text: words })
  })

  it('states a call its turn cut short as cancelled', () => {
    const scenario = createTranscriptScenario({
      archive: [
        message('request', toolStarted(CODEWHALE_TOOL.Bash, { command: 'sleep 9' }), 1n, { spanId: CALL, spanType: CODEWHALE_TOOL.Bash }),
        message('retained', toolStarted(CODEWHALE_TOOL.Bash, { command: 'sleep 9' }), 2n, { spanId: CALL, spanType: CODEWHALE_TOOL.Bash, completion: MessageCompletion.INTERRUPTED }),
      ],
    })
    expect(scenario.toolRow('retained').role).toBe('result')
    expect(scenario.toolRow('retained').call.status).toBe('cancelled')
  })

  it('reads a subagent\'s transcript rows', () => {
    const scenario = createTranscriptScenario({
      archive: [
        message('thinking', childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.Thinking, thinking: 'Child thinking.' }), 1n),
        message('use', childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.ToolUse, id: 'b1', name: CODEWHALE_TOOL.Bash, input: { command: 'ls' } }), 2n, { spanId: 'b1', spanType: CODEWHALE_TOOL.Bash }),
        message('result', childBlock(CODEWHALE_TRANSCRIPT_ROLE.User, { type: CODEWHALE_BLOCK_TYPE.ToolResult, tool_use_id: 'b1', content: 'a.ts' }, 3), 3n, { spanId: 'b1', spanType: CODEWHALE_TOOL.Bash }),
        message('text', childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.Text, text: 'There is 1 file.' }, 4), 4n),
      ],
    })
    expect(scenario.extract('thinking')).toMatchObject({ kind: 'row', row: { kind: 'assistant-thinking', text: 'Child thinking.' } })
    expect(scenario.toolRow('result').call).toMatchObject({ kind: 'execute', status: 'completed', request: { command: 'ls' } })
    expect(scenario.extract('text')).toMatchObject({ kind: 'row', row: { kind: 'assistant-text', text: 'There is 1 file.' } })
  })

  it('reads a reply, a turn end and a notice', () => {
    const scenario = createTranscriptScenario({
      archive: [
        message('reply', itemFinished(CODEWHALE_ITEM_KIND.AgentMessage, 'Hello there.'), 1n),
        message('status', itemFinished(CODEWHALE_ITEM_KIND.Status, 'Checkpoint saved'), 2n),
        message('end', turnCompleted(CODEWHALE_TURN_STATUS.Completed, { duration_ms: 1500 }), 3n),
      ],
    })
    expect(scenario.extract('reply')).toMatchObject({ kind: 'row', row: { kind: 'assistant-text', text: 'Hello there.' } })
    expect(scenario.extract('status')).toMatchObject({ kind: 'row', row: { kind: 'notification', thread: { entries: [{ kind: 'status', text: 'Checkpoint saved' }] } } })
    expect(scenario.extract('end')).toMatchObject({ kind: 'row', row: { kind: 'divider', divider: { label: 'Turn ended (1.5s)' } } })
  })
})

describe('codewhale span roles', () => {
  it('asks for the result of a request row and the request of a result row', () => {
    const scenario = createTranscriptScenario({ archive: [message('request', toolStarted(CODEWHALE_TOOL.Bash, {}), 1n, { spanId: CALL })] })
    const resolved = scenario.entry('request').resolved
    expect(plugin.transcript.spanRole(resolved)).toBe('request')
    expect(plugin.transcript.relatedMessages?.(resolved)).toStrictEqual(['result'])
    const other = createTranscriptScenario({ archive: [message('result', toolCompleted(CODEWHALE_TOOL.Bash, {}, 'x'), 1n, { spanId: CALL })] })
    const result = other.entry('result').resolved
    expect(plugin.transcript.spanRole(result)).toBe('result')
    expect(plugin.transcript.relatedMessages?.(result)).toStrictEqual(['request'])
    const reply = createTranscriptScenario({ archive: [message('reply', itemFinished(CODEWHALE_ITEM_KIND.AgentMessage, 'Hi'), 1n)] })
    expect(plugin.transcript.spanRole(reply.entry('reply').resolved)).toBe('other')
    expect(plugin.transcript.relatedMessages?.(reply.entry('reply').resolved)).toStrictEqual([])
  })
})

describe('codewhale controls', () => {
  const controls = plugin.controls!

  it('sends the runtime\'s own answer list for a question', async () => {
    const payload = questionPayload()
    const questions = controls.askUserQuestion!.extractQuestions(payload)
    const state = createControlAnswerState({ selections: { 0: ['Blue'], 1: ['S', 'M'] } })
    const response = await sent(respond => controls.askUserQuestion!.sendAnswer(request(payload), respond, questions, state))
    expect(response).toStrictEqual({
      type: 'control_response',
      response: {
        subtype: 'success',
        request_id: 'user_input:q1',
        response: {
          behavior: 'allow',
          updatedInput: {
            questions: QUESTIONS,
            answers: [
              { id: 'color', label: 'Blue', value: 'Blue' },
              { id: 'sizes', label: 'S', value: 'S' },
              { id: 'sizes', label: 'M', value: 'M' },
            ],
          },
        },
      },
    })
  })

  it('sends a decline with the reason', async () => {
    const payload = questionPayload()
    const response = await sent(respond => controls.askUserQuestion!.sendReject(request(payload), respond, 'User stopped'))
    expect(response).toMatchObject({ response: { request_id: 'user_input:q1', response: { behavior: 'deny', message: 'User stopped' } } })
  })

  it('answers an approval from the composer as a denial', () => {
    expect(controls.buildControlResponse?.(approvalPayload(CODEWHALE_TOOL.Bash, {}), 'Use a dry run.', 'approval:ap1'))
      .toMatchObject({ response: { request_id: 'approval:ap1', response: { behavior: 'deny', message: 'Use a dry run.' } } })
  })

  // An empty send denies with the shared placeholder, which the worker reads as a
  // deny with no reason, so no empty message becomes the reader's next turn.
  it('answers an empty send from the composer as a denial with no reason', () => {
    expect(controls.buildControlResponse?.(approvalPayload(CODEWHALE_TOOL.Bash, {}), '', 'approval:ap1'))
      .toMatchObject({ response: { request_id: 'approval:ap1', response: { behavior: 'deny', message: CONTROL_REJECTED_BY_USER_MESSAGE } } })
  })

  it('sends free text as the Other answer, and an empty answer list for no answer', async () => {
    const payload = questionPayload()
    const questions = controls.askUserQuestion!.extractQuestions(payload)
    const typed = await sent(respond => controls.askUserQuestion!.sendAnswer(request(payload), respond, questions, createControlAnswerState({ customTexts: { 0: 'Green' } })))
    expect(typed).toMatchObject({ response: { response: { behavior: 'allow', updatedInput: { answers: [{ id: 'color', label: 'Other', value: 'Green' }] } } } })
    const unanswered = await sent(respond => controls.askUserQuestion!.sendAnswer(request(payload), respond, questions, createControlAnswerState({})))
    expect(unanswered).toMatchObject({ response: { response: { behavior: 'allow', updatedInput: { questions: QUESTIONS, answers: [] } } } })
  })

  it('recognizes the question, and reads an approval into the permission row', () => {
    expect(controls.askUserQuestion!.isRequest(questionPayload())).toBe(true)
    expect(controls.askUserQuestion!.isRequest(approvalPayload(CODEWHALE_TOOL.Bash, {}))).toBe(false)
    expect(controls.extractControl?.({ payload: approvalPayload(CODEWHALE_TOOL.Bash, { command: 'ls' }) })?.kind).toBe('permission')
  })
})
