import type { ControlRequest } from '~/stores/control.store'
import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerRow } from '~/test-support/toolCallFixture'
import { createControlAnswerState } from '../../controls/types'
import { providerFor } from '../registry'
import { classifyOhMyPiMessage } from './classification'
import { ohMyPiCompactionBoundary, ohMyPiNotificationEntry } from './extractors/notification'
import { ohMyPiResultDivider } from './extractors/resultDivider'
import { ohMyPiExtractRow } from './extractors/row'
import { ohMyPiControls } from './pluginControls'
import { resolveOhMyPiMessage } from './resolveMessage'
import { ohMyPiValidateResumeHandle } from './resumeHandle'
import { ohMyPiContextUsageFromMessage } from './sessionMetadata'
import { ohMyPiRelatedMessages, ohMyPiSpanRole } from './spanRole'
import './plugin'

const plugin = providerFor(AgentProvider.OH_MY_PI)!
const controls = plugin.controls!
const ask = controls.askUserQuestion!

function request(payload: Record<string, unknown>, requestId = 'r1'): ControlRequest {
  return { requestId, agentId: 'a1', payload }
}

/** The JSON each call of a sender carried. */
function sent(onRespond: ReturnType<typeof vi.fn>): unknown[] {
  return onRespond.mock.calls.map(call => JSON.parse(new TextDecoder().decode(call[0] as Uint8Array)))
}

const approval = { type: 'extension_ui_request', id: 'a1', method: 'select', title: 'Allow tool: bash\nCommand: ls', options: ['Approve', 'Deny'] }

describe('ohmypi plugin', () => {
  // The test compares each hook by identity: a plugin that registers another
  // provider's reader for one hook is still "defined", and only the identity finds it.
  it('is registered for the OH_MY_PI provider with the omp reader for each hook', () => {
    expect(plugin.transcript).toEqual({
      resolveMessage: resolveOhMyPiMessage,
      spanRole: ohMyPiSpanRole,
      relatedMessages: ohMyPiRelatedMessages,
      classify: classifyOhMyPiMessage,
      extractRow: ohMyPiExtractRow,
      notificationEntry: ohMyPiNotificationEntry,
      extractDivider: ohMyPiResultDivider,
    })
    expect(plugin.controls).toBe(ohMyPiControls)
    expect(plugin.session?.contextUsageFromMessage).toBe(ohMyPiContextUsageFromMessage)
    expect(plugin.session?.compactionBoundaryFromMessage).toBe(ohMyPiCompactionBoundary)
    expect(plugin.session?.validateResumeHandle).toBe(ohMyPiValidateResumeHandle)
  })

  // The worker's ValidateAttachment refuses a PDF and a binary file with the same policy.
  it('accepts text and images, and no PDF or binary', () => {
    expect(plugin.configuration?.attachments).toEqual({ text: true, image: true, pdf: false, binary: false })
  })

  it('labels the settings trigger with the approval mode', () => {
    expect(plugin.configuration?.triggerModeGroupKey).toBe('permissionMode')
  })

  it('offers the Bypass preset as yolo and no Smart preset', () => {
    expect(controls.permissionPresets).toEqual({ bypass: { sets: { permissionMode: 'yolo' } } })
  })

  it('states the session as a file', () => {
    expect(plugin.session?.sessionIdIsFilePath).toBe(true)
    expect(plugin.session?.validateResumeHandle?.('01a0cf77')).toBeNull()
  })
})

describe('ohmypi controls', () => {
  it('routes the bridge\'s request and a select to the question form, each other extension dialog to the dialog row, and the approval to the permission row', () => {
    expect(ask.isRequest({ type: 'leapmux_ask', id: 'q', questions: [] })).toBe(true)
    expect(ask.isRequest({ type: 'extension_ui_request', method: 'select', title: 'Pick', options: ['a'] })).toBe(true)
    for (const method of ['confirm', 'input', 'editor']) {
      const dialog = { type: 'extension_ui_request', id: 'd1', method, title: 'Proceed?' }
      expect(ask.isRequest(dialog), method).toBe(false)
      expect(controls.extractControl?.({ payload: dialog })?.kind, method).toBe('dialog')
    }
    expect(ask.isRequest(approval)).toBe(false)
    expect(controls.extractControl?.({ payload: approval })?.kind).toBe('permission')
  })

  it('answers the bridge\'s request with one envelope for the whole call', async () => {
    const payload = { type: 'leapmux_ask', id: 'q1', questions: [{ id: 'db', question: 'Which database?', options: [{ label: 'SQLite' }] }] }
    const onRespond = vi.fn(async () => {})
    await createRoot(async (dispose) => {
      const state = createControlAnswerState({ selections: { 0: ['SQLite'] } })
      await ask.sendAnswer(request(payload, 'q1'), onRespond, ask.extractQuestions(payload), state)
      dispose()
    })
    expect(sent(onRespond)).toEqual([{ type: 'leapmux_ask_answer', id: 'q1', answers: [{ id: 'db', selected: ['SQLite'] }] }])
  })

  it('answers a dialog with omp\'s own confirm, value and cancellation envelopes', () => {
    const responder = controls.dialogResponder!
    expect(responder.confirm('c1', true)).toEqual({ type: 'extension_ui_response', id: 'c1', confirmed: true })
    expect(responder.confirm('c1', false)).toEqual({ type: 'extension_ui_response', id: 'c1', confirmed: false })
    // omp tells an empty answer apart from a dismissal.
    expect(responder.value('i1', '')).toEqual({ type: 'extension_ui_response', id: 'i1', value: '' })
    expect(responder.value('e1', 'fix: typo')).toEqual({ type: 'extension_ui_response', id: 'e1', value: 'fix: typo' })
    expect(responder.cancel('i1')).toEqual({ type: 'extension_ui_response', id: 'i1', cancelled: true })
  })

  it('answers a select with its option, and dismisses it with none', async () => {
    const payload = { type: 'extension_ui_request', id: 's1', method: 'select', title: 'Pick', options: ['a', 'b'] }
    const onRespond = vi.fn(async () => {})
    await createRoot(async (dispose) => {
      await ask.sendAnswer(request(payload, 's1'), onRespond, ask.extractQuestions(payload), createControlAnswerState({ selections: { 0: ['b'] } }))
      await ask.sendAnswer(request(payload, 's1'), onRespond, ask.extractQuestions(payload), createControlAnswerState())
      dispose()
    })
    expect(sent(onRespond)).toEqual([
      { type: 'extension_ui_response', id: 's1', value: 'b' },
      { type: 'extension_ui_response', id: 's1', cancelled: true },
    ])
  })

  it('answers a select with the typed text, and dismisses it when the typed text is blank', async () => {
    const payload = { type: 'extension_ui_request', id: 's1', method: 'select', title: 'Pick', options: ['a', 'b'] }
    const onRespond = vi.fn(async () => {})
    await createRoot(async (dispose) => {
      await ask.sendAnswer(request(payload, 's1'), onRespond, ask.extractQuestions(payload), createControlAnswerState({ customTexts: { 0: 'c' } }))
      // omp refuses an empty value for a select, so a blank answer is a dismissal.
      await ask.sendAnswer(request(payload, 's1'), onRespond, ask.extractQuestions(payload), createControlAnswerState({ customTexts: { 0: '  \n' } }))
      dispose()
    })
    expect(sent(onRespond)).toEqual([
      { type: 'extension_ui_response', id: 's1', value: 'c' },
      { type: 'extension_ui_response', id: 's1', cancelled: true },
    ])
  })

  it('answers the bridge\'s request with an answer for each question, the unanswered ones included', async () => {
    const payload = {
      type: 'leapmux_ask',
      id: 'q2',
      questions: [
        { id: 'name', question: 'Project name?', options: [{ label: 'alpha' }] },
        { id: 'langs', question: 'Which languages?', multi: true, options: [{ label: 'Go' }, { label: 'Rust' }] },
      ],
    }
    const onRespond = vi.fn(async () => {})
    await createRoot(async (dispose) => {
      const state = createControlAnswerState({ selections: { 1: ['Go', 'Rust'] } })
      await ask.sendAnswer(request(payload, 'q2'), onRespond, ask.extractQuestions(payload), state)
      dispose()
    })
    // The bridge matches each answer to its question by id, so a question with no
    // answer still states its id.
    expect(sent(onRespond)).toEqual([{ type: 'leapmux_ask_answer', id: 'q2', answers: [{ id: 'name' }, { id: 'langs', selected: ['Go', 'Rust'] }] }])
  })

  it('dismisses a question with omp\'s own cancellation', async () => {
    const onRespond = vi.fn(async () => {})
    await ask.sendReject(request({ type: 'leapmux_ask', id: 'q1', questions: [] }, 'q1'), onRespond, '')
    expect(sent(onRespond)).toEqual([{ type: 'extension_ui_response', id: 'q1', cancelled: true }])
  })

  it('sends a chosen approval option as the dialog\'s value', async () => {
    const onRespond = vi.fn(async () => {})
    await controls.sendPermissionOption!(onRespond, 'a1', 'Approve')
    expect(sent(onRespond)).toEqual([{ type: 'extension_ui_response', id: 'a1', value: 'Approve' }])
  })

  it('refuses an approval with typed feedback, and sends the feedback as the next message', () => {
    expect(controls.buildControlResponse?.(approval, 'use a safer command', 'a1')).toEqual({ type: 'extension_ui_response', id: 'a1', value: 'Deny' })
    expect(controls.controlFeedbackAsFollowUpMessage?.(approval)).toBe(true)
    expect(controls.controlEditorPurpose?.(approval)).toBe('feedback')
  })

  it('dismisses any other request the shared editor answers', () => {
    const other = { type: 'extension_ui_request', id: 'x', method: 'hologram' }
    expect(controls.buildControlResponse?.(other, '', 'x')).toEqual({ type: 'extension_ui_response', id: 'x', cancelled: true })
    expect(controls.controlFeedbackAsFollowUpMessage?.(other)).toBe(false)
    expect(controls.controlEditorPurpose?.(other)).toBe('none')
  })
})

describe('ohmypi rows', () => {
  it('reads an assistant message as its text, and leaves its thinking to the row the worker writes for it', () => {
    // omp 18.2.11's own frame (probe s1), shortened. The worker persists the thinking
    // as a reasoning row before this one, which the shared transcript draws.
    const text = providerRow(AgentProvider.OH_MY_PI, { type: 'message_end', message: { role: 'assistant', content: [{ type: 'thinking', thinking: 'Greet.' }, { type: 'text', text: 'Hello from the mock model.' }], stopReason: 'stop' } })
    expect(text).toEqual({ kind: 'assistant-text', text: 'Hello from the mock model.' })
    const thinking = providerRow(AgentProvider.OH_MY_PI, { type: 'message_end', message: { role: 'assistant', content: [{ type: 'thinking', thinking: 'Greet.' }] } })
    expect(thinking).toEqual({ kind: 'hidden' })
  })

  it('reads LeapMux\'s user row', () => {
    expect(providerRow(AgentProvider.OH_MY_PI, { content: 'say hello' })).toEqual({ kind: 'user', text: 'say hello', attachments: [] })
  })

  it('reads LeapMux\'s plan-execution row', () => {
    expect(providerRow(AgentProvider.OH_MY_PI, { content: 'Run the plan.', planExecution: true })).toEqual({ kind: 'plan-execution', text: 'Run the plan.' })
  })

  it('reads the end of a run as a divider', () => {
    expect(providerRow(AgentProvider.OH_MY_PI, { type: 'agent_end', isTerminal: true, messages: [{ role: 'assistant', stopReason: 'stop' }] })).toMatchObject({ kind: 'divider' })
  })

  it('reads a tool frame as the row of its call', () => {
    const row = providerRow(AgentProvider.OH_MY_PI, { type: 'tool_execution_start', toolCallId: 'call_1', toolName: 'bash', args: { command: 'ls' } }, { role: 'request' })
    expect(row?.kind).toBe('tool')
    expect(row?.kind === 'tool' ? row.call.kind : null).toBe('execute')
  })
})
