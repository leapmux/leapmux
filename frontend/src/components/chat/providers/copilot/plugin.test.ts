import type { ControlAnswerState } from '../../controls/types'
import { describe, expect, it, vi } from 'vitest'
import { COPILOT_EVENT, COPILOT_MODE, COPILOT_OPTION, COPILOT_PERMISSION_MODE, COPILOT_TOOL } from '~/generated/contracts/copilot-protocol'
import { WORKSPACE_TRUST_CONTROL_TYPE, WORKSPACE_TRUST_DECISION } from '~/generated/contracts/workspace-trust'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { copilotToolStart } from '~/test-support/copilotFixtures'
import { providerQuotableText } from '~/test-support/toolCallFixture'
import { createControlAnswerState } from '../../controls/types'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import { WORKSPACE_TRUST_OPTION } from '../workspaceTrust'

import './plugin'

function frame(type: string, data: Record<string, unknown> = {}): Record<string, unknown> {
  return { jsonrpc: '2.0', method: 'session.event', params: { sessionId: 'session-1', event: { id: 'event-1', type, data } } }
}

describe('copilot provider', () => {
  const plugin = providerFor(AgentProvider.GITHUB_COPILOT)!

  it('accepts text, image and PDF attachments, and refuses a binary file', () => {
    expect(plugin?.configuration?.attachments).toEqual({ text: true, image: true, pdf: true, audio: false, binary: false })
  })

  // The tool result already states the completion, so the Worker drops this announcement.
  // A stored copy has no browser-only hide rule.
  it('leaves a stored task-complete announcement unknown', () => {
    expect(plugin?.transcript.classify(input(frame(COPILOT_EVENT.SessionTaskComplete, {
      summary: 'Listed the exact callable tool names as requested.',
      success: true,
    })))).toEqual({ kind: 'unknown' })
  })

  // A row LeapMux itself wrote carries a `type` and no Copilot event, so the event
  // dispatch finds nothing of its own in it.
  it('classifies each plain notification type as a notification', () => {
    const notifications = [
      { type: 'interrupted' },
      { type: 'settings_changed', changes: { model: { old: 'a', new: 'b' } } },
      { type: 'context_cleared' },
      { type: 'agent_error', error: 'failed' },
      { type: 'plan_updated', plan_title: 'Plan' },
    ]
    for (const notification of notifications)
      expect(plugin?.transcript.classify(input(notification)).kind).toBe('notification')
  })

  it('reads the plan toggle from the session-mode axis, not the permission mode', () => {
    expect(plugin?.configuration?.planMode).toMatchObject({
      groupKey: COPILOT_OPTION.SessionMode,
      planValue: COPILOT_MODE.Plan,
      defaultValue: COPILOT_MODE.Interactive,
    })
    expect(plugin?.configuration?.planMode?.currentMode({ optionValues: { [COPILOT_OPTION.SessionMode]: COPILOT_MODE.Plan } })).toBe(COPILOT_MODE.Plan)
    expect(plugin?.configuration?.planMode?.currentMode({ optionValues: { [COPILOT_OPTION.SessionMode]: '' } })).toBe(COPILOT_MODE.Interactive)
    expect(plugin?.configuration?.triggerModeGroupKey).toBe(COPILOT_OPTION.SessionMode)
  })

  // The two axes are independent: a preset moves the permission mode and leaves the
  // session mode where the user put it.
  it('maps the permission presets onto the native permission modes', () => {
    expect(plugin?.controls?.permissionPresets).toEqual({
      smart: { sets: { permissionMode: COPILOT_PERMISSION_MODE.Assisted } },
      bypass: { sets: { permissionMode: COPILOT_PERMISSION_MODE.AllowAll } },
    })
  })

  it('classifies the events that own a surface', () => {
    const classify = (row: Record<string, unknown>) => plugin?.transcript.classify!(input(row, null, AgentProvider.GITHUB_COPILOT)).kind
    expect(classify(frame(COPILOT_EVENT.AssistantMessage, { content: 'Hello' }))).toBe('assistant_text')
    expect(classify(frame(COPILOT_EVENT.AssistantReasoning, { content: 'Thinking' }))).toBe('assistant_thinking')
    expect(classify(frame(COPILOT_EVENT.ToolStarted, { toolCallId: 'call', toolName: 'view' }))).toBe('tool_use')
    expect(classify(frame(COPILOT_EVENT.ToolCompleted, { toolCallId: 'call', success: true }))).toBe('tool_result')
    expect(classify(frame(COPILOT_EVENT.SessionIdle))).toBe('result_divider')
    expect(classify(frame(COPILOT_EVENT.SessionError, { message: 'It broke' }))).toBe('notification')
  })

  // An empty message says nothing, and a lifecycle event repeats what the session
  // response already returned.
  it('leaves stored consumed events unknown', () => {
    const classify = (row: Record<string, unknown>) => plugin?.transcript.classify!(input(row, null, AgentProvider.GITHUB_COPILOT)).kind
    for (const type of [
      COPILOT_EVENT.SessionStart,
      COPILOT_EVENT.SessionResume,
      COPILOT_EVENT.AssistantTurnStart,
      COPILOT_EVENT.AssistantTurnEnd,
      COPILOT_EVENT.AssistantMessageDelta,
      COPILOT_EVENT.SubagentStarted,
      // The Worker consumes the subagent settings before storage.
      COPILOT_EVENT.SubagentConfigured,
      COPILOT_EVENT.UserMessage,
      COPILOT_EVENT.SessionModeChanged,
    ])
      expect(classify(frame(type)), type).toBe('unknown')
    expect(classify(frame(COPILOT_EVENT.AssistantMessage, { content: '   ' }))).toBe('hidden')
    expect(classify(frame(COPILOT_EVENT.PermissionCompleted))).toBe('hidden')
  })

  // Both sides read the same contract table for these trace families.
  it('hides the runtime trace a stored transcript may still hold', () => {
    const classify = (row: Record<string, unknown>) => plugin?.transcript.classify!(input(row, null, AgentProvider.GITHUB_COPILOT)).kind
    for (const type of [
      'model.turn_started',
      'model.model_call_started',
      'model.captured_assignment_context',
      'model.model_call_success',
      'model.message',
      'model.response',
      'model.turn_ended',
      'model.messages_snapshot',
      'hook.start',
      'hook.end',
    ])
      expect(classify(frame(type, { kind: type })), type).toBe('hidden')
  })

  // The Worker consumes model failures. The shared trace rule also covers stored copies.
  it('hides a stored model failure through the shared trace rule', () => {
    const classify = (row: Record<string, unknown>) => plugin?.transcript.classify!(input(row, null, AgentProvider.GITHUB_COPILOT)).kind
    expect(classify(frame(COPILOT_EVENT.ModelCallFailure, { errorMessage: 'the model refused the request' }))).toBe('hidden')
  })

  it('classifies a LeapMux user row rather than stringifying it', () => {
    const classify = (row: Record<string, unknown>) => plugin?.transcript.classify!(input(row, null, AgentProvider.GITHUB_COPILOT)).kind
    expect(classify({ content: 'Typed message' })).toBe('user_content')
    expect(classify({ content: 'Implement the plan.', planExecution: true })).toBe('plan_execution')
    expect(classify({ content: 'Internal', hidden: true })).toBe('user_content')
  })

  it('labels the turn end and states an interruption', () => {
    expect(plugin?.transcript.extractDivider!(frame(COPILOT_EVENT.SessionIdle))).toEqual({ label: 'Turn ended' })
    expect(plugin?.transcript.extractDivider!(frame(COPILOT_EVENT.SessionIdle, { aborted: true }))).toEqual({ label: 'Turn interrupted' })
  })

  it('quotes the assistant text a row carries', () => {
    const row = frame(COPILOT_EVENT.AssistantMessage, { content: 'Quotable' })
    expect(providerQuotableText(AgentProvider.GITHUB_COPILOT, row, { category: { kind: 'assistant_text' } })).toBe('Quotable')
    expect(providerQuotableText(AgentProvider.GITHUB_COPILOT, { content: 'Typed' }, { category: { kind: 'user_content' } })).toBe('Typed')
  })
})

describe('a copilot tool row the turn interrupted', () => {
  const plugin = providerFor(AgentProvider.GITHUB_COPILOT)!
  // The runtime sends no completion for a call its turn cut short, so the worker
  // stores the START frame again as the closing row and states the outcome in the
  // completion column. That copy is the call's result.
  const start = copilotToolStart('tool-1', COPILOT_TOOL.Bash, { command: 'bun test' })

  it('reads the retained start frame as the call result', () => {
    expect(plugin?.transcript.spanRole!({ ...input(start), completion: MessageCompletion.INTERRUPTED })).toBe('result')
    expect(plugin?.transcript.spanRole!(input(start))).toBe('request')
  })

  it('classifies the retained copy as a result and the first copy as a request', () => {
    expect(plugin?.transcript.classify({ ...input(start), completion: MessageCompletion.INTERRUPTED }))
      .toEqual({ kind: 'tool_result' })
    expect(plugin?.transcript.classify(input(start))).toMatchObject({ kind: 'tool_use' })
  })
})

// A result states no tool name and no arguments of its own, so it always wants its
// request. A request wants its result only when the body comes from there: an agent
// call, or a call whose arguments are empty.
describe('copilot relatedMessages', () => {
  const plugin = providerFor(AgentProvider.GITHUB_COPILOT)!

  it('a result wants its request', () => {
    expect(plugin?.transcript.relatedMessages!(input(frame(COPILOT_EVENT.ToolCompleted, { toolCallId: 'call', success: true, result: { content: 'ok' } })))).toEqual(['request'])
  })

  it('a request wants its result only when the body comes from there', () => {
    expect(plugin?.transcript.relatedMessages!(input(frame(COPILOT_EVENT.ToolStarted, { toolCallId: 'call', toolName: COPILOT_TOOL.Bash, arguments: {} })))).toEqual(['result'])
    expect(plugin?.transcript.relatedMessages!(input(frame(COPILOT_EVENT.ToolStarted, { toolCallId: 'call', toolName: COPILOT_TOOL.Bash, arguments: { command: 'ls' } })))).toEqual([])
    expect(plugin?.transcript.relatedMessages!(input(frame(COPILOT_EVENT.ToolStarted, { toolCallId: 'call', toolName: COPILOT_TOOL.Task, arguments: { prompt: 'x' } })))).toEqual(['result'])
  })
})

// The runtime distinguishes a typed answer from a selected choice, and it accepts an
// explicit empty answer. Both facts travel inside the neutral control-response
// envelope, which carries a scope or an answer that neither shared builder can hold.
describe('copilot question answers', () => {
  const plugin = providerFor(AgentProvider.GITHUB_COPILOT)!
  const request = { requestId: 'req-1', agentId: 'agent-1', payload: frame(COPILOT_EVENT.UserInputRequested, { question: 'Which one?', choices: ['A', 'B'] }) }
  const questions = [{ question: 'Which one?', options: [{ label: 'A' }, { label: 'B' }] }]

  async function sent(state: ControlAnswerState) {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    await plugin?.controls?.askUserQuestion!.sendAnswer(request, onRespond, questions, state)
    expect(onRespond).toHaveBeenCalledOnce()
    return JSON.parse(new TextDecoder().decode(onRespond.mock.calls[0]?.[0] as Uint8Array))
  }

  it.each([
    ['a selected choice', { selections: { 0: ['B'] } }, { behavior: 'allow', answer: 'B', wasFreeform: false }],
    ['a typed answer', { customTexts: { 0: '  neither  ' } }, { behavior: 'allow', answer: 'neither', wasFreeform: true }],
    ['an explicit empty answer', {}, { behavior: 'allow', answer: '', wasFreeform: true }],
  ])('sends %s inside the control-response envelope', async (_label, seed, answer) => {
    expect(await sent(createControlAnswerState(seed))).toEqual({
      type: 'control_response',
      response: { subtype: 'success', request_id: 'req-1', response: answer },
    })
  })
})

// The Worker asks Copilot's folder-trust question itself, in LeapMux's neutral shape.
describe('copilotControls workspace trust', () => {
  const plugin = providerFor(AgentProvider.GITHUB_COPILOT)!
  const question = { type: WORKSPACE_TRUST_CONTROL_TYPE.Question, path: '/work/project', configuration: ['MCP servers'] }

  it('draws the question with the name of the agent', () => {
    expect(plugin.controls?.extractControl?.({ payload: question })).toEqual({
      kind: 'permission',
      permission: {
        title: 'Trust the folder /work/project',
        reason: 'GitHub Copilot loads the MCP servers of a project only from a folder that you trust, and it keeps your answer for the folder.',
        options: [
          { optionId: 'workspace-trust', kind: 'allow_once', name: 'Trust this folder' },
          { optionId: 'workspace-reject', kind: 'reject_once', name: 'Do not trust' },
        ],
      },
    })
  })

  it('still draws a native permission request', () => {
    const permission = frame(COPILOT_EVENT.PermissionRequested, { requestId: 'r-1', permissionRequest: { kind: 'read', path: '/work/project/a.go' } })
    expect(plugin.controls?.extractControl?.({ payload: permission })).toMatchObject({ kind: 'permission', permission: { options: expect.arrayContaining([expect.objectContaining({ name: 'Allow once' })]) } })
  })

  it('sends Trust as an allow decision, and a native option through its own envelope', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    await plugin.controls?.sendPermissionOption?.(onRespond, 'workspace-trust:session-1', WORKSPACE_TRUST_OPTION.Trust)
    expect(JSON.parse(new TextDecoder().decode(onRespond.mock.calls[0]?.[0] as Uint8Array))).toEqual({
      type: 'control_response',
      response: { subtype: 'success', request_id: 'workspace-trust:session-1', response: { behavior: 'allow' } },
    })
    await plugin.controls?.sendPermissionOption?.(onRespond, 'copilot-request', 'session')
    expect(JSON.parse(new TextDecoder().decode(onRespond.mock.calls[1]?.[0] as Uint8Array))).toEqual({
      type: 'control_response',
      response: { subtype: 'success', request_id: 'copilot-request', response: { behavior: 'allow', scope: 'session' } },
    })
  })

  it('reads the saved answer', () => {
    expect(plugin.controls?.controlResponseDisplay?.({
      requestId: 'workspace-trust:session-1',
      claimToken: '',
      request: question,
      response: { type: 'control_response', response: { request_id: 'workspace-trust:session-1', response: { decision: WORKSPACE_TRUST_DECISION.Reject } } },
    })).toEqual({ kind: 'label', text: 'Did not trust this folder' })
  })
})
