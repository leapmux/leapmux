import type { PersistedControlResponse } from '../../persistedControlResponse'
import type { ControlRequest } from '~/stores/control.store'
import { describe, expect, it } from 'vitest'
import { CLINE_DECLINE_REASON, CLINE_PERMISSION_MODE, CLINE_QUESTION_ANSWER } from '~/generated/contracts/cline-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { buildDenyResponse, CONTROL_REJECTED_BY_USER_MESSAGE } from '~/utils/controlResponse'
import { createControlAnswerState } from '../../controls/types'
import { providerFor } from '../registry'
import { classifyClineMessage } from './classification'
import { clineCompactionBoundary, clineNotificationEntry } from './extractors/notification'
import { clineResultDivider } from './extractors/resultDivider'
import { clineExtractRow } from './extractors/row'
import { clineConfiguration } from './pluginConfiguration'
import { clineControls } from './pluginControls'
import { clineRelatedMessages, clineSpanRole } from './spanRole'
import { CLINE_SPAWN_WARNING } from './spawnWarning'
import './plugin'

const plugin = providerFor(AgentProvider.CLINE)!
const controls = plugin.controls!

/** A stored approval request, as the worker publishes it. */
function approval(toolName: string, input: Record<string, unknown>): Record<string, unknown> {
  return {
    version: 'v1',
    event: 'approval.requested',
    sessionId: 's1',
    payload: { approvalId: 'approval_1', sessionId: 's1', agentId: 'agent_1', toolCallId: 'call_1', toolName, inputJson: JSON.stringify(input) },
  }
}

/** A stored question request, as the worker publishes it. */
const question: Record<string, unknown> = {
  version: 'v1',
  event: 'capability.requested',
  sessionId: 's1',
  payload: {
    requestId: 'capreq_1',
    targetClientId: 'leapmux-a',
    capabilityName: 'tool_executor.askQuestion',
    payload: { executor: 'askQuestion', args: ['Which color do you prefer?', ['Red', 'Blue']], context: { toolCallId: 'call_q' } },
  },
}

function saved(request: Record<string, unknown>, response: Record<string, unknown> | undefined): PersistedControlResponse {
  return { requestId: 'r1', claimToken: '', request, response }
}

describe('cline plugin', () => {
  // A registration that is present but holds the readers of another provider passes a
  // presence check, so the test compares each hook with Cline's own function.
  it('registers the Cline readers for the CLINE provider', () => {
    expect(Object.keys(plugin.transcript).sort()).toEqual(['classify', 'extractDivider', 'extractRow', 'notificationEntry', 'relatedMessages', 'spanRole'])
    expect(plugin.transcript.classify).toBe(classifyClineMessage)
    expect(plugin.transcript.extractRow).toBe(clineExtractRow)
    expect(plugin.transcript.extractDivider).toBe(clineResultDivider)
    expect(plugin.transcript.notificationEntry).toBe(clineNotificationEntry)
    expect(plugin.transcript.spanRole).toBe(clineSpanRole)
    expect(plugin.transcript.relatedMessages).toBe(clineRelatedMessages)
    expect(plugin.session?.compactionBoundaryFromMessage).toBe(clineCompactionBoundary)
    expect(plugin.controls).toBe(clineControls)
    expect(plugin.configuration).toBe(clineConfiguration)
  })

  // The worker's ValidateAttachment refuses a PDF and a binary file with the same policy.
  it('accepts text and images, and no PDF or binary', () => {
    expect(plugin.configuration?.attachments).toEqual({ text: true, image: true, pdf: false, binary: false })
  })

  it('carries Plan on the permission-mode axis, as the plan toggle', () => {
    expect(plugin.configuration?.triggerModeGroupKey).toBe('permissionMode')
    expect(plugin.configuration?.planMode?.groupKey).toBe('permissionMode')
    expect(plugin.configuration?.planMode?.planValue).toBe(CLINE_PERMISSION_MODE.Plan)
    expect(plugin.configuration?.planMode?.defaultValue).toBe(CLINE_PERMISSION_MODE.Act)
    expect(plugin.configuration?.planMode?.currentMode({ optionValues: { permissionMode: CLINE_PERMISSION_MODE.Plan } })).toBe(CLINE_PERMISSION_MODE.Plan)
  })

  it('states no elicitation and no child input', () => {
    expect(controls.elicitation).toBeUndefined()
    expect(plugin.configuration?.supportsSubagentSend).toBeUndefined()
  })
})

describe('cline controls', () => {
  it('offers the Bypass preset as Auto-approve and no Smart preset', () => {
    expect(controls.permissionPresets).toEqual({ bypass: { sets: { permissionMode: CLINE_PERMISSION_MODE.AutoApprove } } })
  })

  it('reads an approval into a permission with its arguments', () => {
    const control = controls.extractControl?.({ payload: approval('editor', { path: '/work/a.ts', old_text: 'a', new_text: 'b' }) })
    expect(control).toEqual({
      kind: 'permission',
      permission: { title: 'editor', input: { path: '/work/a.ts', old_text: 'a', new_text: 'b' }, options: [] },
    })
  })

  it('states the commands of a command approval', () => {
    const control = controls.extractControl?.({ payload: approval('run_commands', { commands: ['git status', { command: 'go', args: ['test', './...'] }] }) })
    expect(control?.kind === 'permission' ? control.permission.command : undefined).toBe('git status\ngo test ./...')
  })

  // A subagent, a configured agent and a teammate each run their own tools without
  // asking, so the approval that starts one says so.
  it.each([
    ['spawn_agent', { task: 'Look.' }],
    ['subagent_reviewer_1a2b', { prompt: 'Review the change.' }],
    ['team_spawn_teammate', { agentId: 'researcher', rolePrompt: 'You research.' }],
    ['team_run_task', { agentId: 'researcher', task: 'Report a word.' }],
  ])('warns that the agent that %s starts runs its tools without asking', (tool, input) => {
    const control = controls.extractControl?.({ payload: approval(tool, input) })
    expect(control?.kind === 'permission' ? control.permission.reason : undefined).toBe(CLINE_SPAWN_WARNING)
  })

  it('gives no warning for a tool that starts no agent', () => {
    for (const tool of ['editor', 'run_commands', 'team_status', 'github__subagent_search']) {
      const control = controls.extractControl?.({ payload: approval(tool, {}) })
      expect(control?.kind === 'permission' ? control.permission.reason : 'no permission', tool).toBeUndefined()
    }
  })

  it('reads the plan tool\'s approval as the plan approval', () => {
    expect(controls.extractControl?.({ payload: approval('switch_to_act_mode', {}) })).toEqual({ kind: 'plan' })
  })

  it('reads nothing else', () => {
    expect(controls.extractControl?.({ payload: question })).toBeNull()
    expect(controls.extractControl?.({ payload: { type: 'control_request' } })).toBeNull()
  })

  it('reads an approval whose arguments are not JSON with none', () => {
    const payload = approval('editor', {})
    const inner = payload.payload as Record<string, unknown>
    inner.inputJson = 'not json'
    const control = controls.extractControl?.({ payload })
    expect(control?.kind === 'permission' ? control.permission.input : undefined).toEqual({})
  })

  // The composer's send refuses the call, and its words ride the refusal as the reason.
  it('sends the composer\'s text as a refusal', () => {
    expect(controls.buildControlResponse?.(approval('editor', {}), 'Use the other file.', 'r1')).toEqual(buildDenyResponse('r1', 'Use the other file.'))
    expect(controls.buildControlResponse?.(approval('editor', {}), '', 'r1')).toEqual(buildDenyResponse('r1', CONTROL_REJECTED_BY_USER_MESSAGE))
  })
})

describe('cline questions', () => {
  const ask = controls.askUserQuestion!

  it('recognizes a question and nothing else', () => {
    expect(ask.isRequest(question)).toBe(true)
    expect(ask.isRequest(approval('ask_question', {}))).toBe(false)
    const other = structuredClone(question)
    ;(other.payload as Record<string, unknown>).capabilityName = 'custom_tool.switch_to_act_mode'
    expect(ask.isRequest(other)).toBe(false)
  })

  it('reads the question and its options', () => {
    expect(ask.extractQuestions(question)).toEqual([{
      question: 'Which color do you prefer?',
      options: [{ value: 'Red', label: 'Red' }, { value: 'Blue', label: 'Blue' }],
    }])
  })

  it('reads no question from arguments that state none', () => {
    const empty = structuredClone(question)
    ;((empty.payload as Record<string, unknown>).payload as Record<string, unknown>).args = ['  ', ['A']]
    expect(ask.extractQuestions(empty)).toEqual([])
  })

  const request: ControlRequest = { agentId: 'a', requestId: 'capreq_1', payload: question }

  /** The bytes one send carries, decoded. */
  function recorder() {
    const sent: Uint8Array[] = []
    const send = async (bytes: Uint8Array) => {
      sent.push(bytes)
    }
    return { send, last: () => JSON.parse(new TextDecoder().decode(sent.at(-1))) as Record<string, unknown> }
  }

  async function answer(selected: string[], typed: string): Promise<Record<string, unknown>> {
    const { send, last } = recorder()
    await ask.sendAnswer(request, send, ask.extractQuestions(question), createControlAnswerState({ selections: { 0: selected }, customTexts: { 0: typed } }))
    return last()
  }

  // The whole envelope, because the worker reads the behavior and the request id too:
  // an answer with another behavior or id would not reach the question.
  it('answers with the picked option', async () => {
    expect(await answer(['Blue'], '')).toEqual({
      type: 'control_response',
      response: { subtype: 'success', request_id: 'capreq_1', response: { behavior: 'allow', [CLINE_QUESTION_ANSWER.Answer]: 'Blue' } },
    })
  })

  it('answers with the reader\'s own words when they typed any', async () => {
    const response = await answer(['Blue'], '  Green  ')
    expect((response.response as { response: Record<string, unknown> }).response[CLINE_QUESTION_ANSWER.Answer]).toBe('Green')
  })

  it('refuses with the reader\'s words', async () => {
    const { send, last } = recorder()
    await ask.sendReject(request, send, 'Not now.')
    expect(last()).toEqual(buildDenyResponse('capreq_1', 'Not now.'))
  })
})

describe('cline saved answers', () => {
  const display = controls.controlResponseDisplay!

  it('words an approval and a refusal', () => {
    expect(display(saved(approval('editor', {}), { approvalId: 'approval_1', approved: true }))).toEqual({ kind: 'label', text: 'Allow' })
    expect(display(saved(approval('editor', {}), { approvalId: 'approval_1', approved: false, reason: 'Not that one.' }))).toEqual({ kind: 'feedback', message: 'Not that one.' })
    expect(display(saved(approval('editor', {}), { approvalId: 'approval_1', approved: false, reason: CLINE_DECLINE_REASON.Tool }))).toEqual({ kind: 'label', text: 'Deny' })
  })

  it('words the plan approval with the mode it switched to', () => {
    const plan = approval('switch_to_act_mode', {})
    expect(display(saved(plan, { approvalId: 'approval_1', approved: true, permissionMode: CLINE_PERMISSION_MODE.AutoApprove }))).toEqual({ kind: 'label', text: 'Approve (Auto-approve)' })
    expect(display(saved(plan, { approvalId: 'approval_1', approved: true }))).toEqual({ kind: 'label', text: 'Approve' })
    expect(display(saved(plan, { approvalId: 'approval_1', approved: false, reason: 'Split it.' }))).toEqual({ kind: 'feedback', message: 'Split it.' })
  })

  it('words a question\'s answer and its refusal', () => {
    expect(display(saved(question, { requestId: 'capreq_1', ok: true, payload: { result: 'Blue' } }))).toEqual({ kind: 'label', text: 'Blue' })
    expect(display(saved(question, { requestId: 'capreq_1', ok: false, error: CLINE_DECLINE_REASON.Question }))).toEqual({ kind: 'label', text: 'Declined' })
    expect(display(saved(question, { requestId: 'capreq_1', ok: false, error: 'Later.' }))).toEqual({ kind: 'feedback', message: 'Later.' })
  })

  it('reads nothing it does not know', () => {
    expect(display(saved(approval('editor', {}), undefined))).toBeNull()
    expect(display(saved(approval('editor', {}), { approvalId: 'approval_1' }))).toBeNull()
    expect(display(saved(question, { requestId: 'capreq_1' }))).toBeNull()
    expect(display(saved({ type: 'other' }, { approved: true }))).toBeNull()
  })
})
