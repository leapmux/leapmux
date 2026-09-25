import { fireEvent, render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { clineApprovalRequest } from '~/test-support/clineFixtures'
import { ControlRequestActions } from '~/test-support/controlRequestBanner'
import { copilotPermissionRequest } from '~/test-support/copilotFixtures'
import { kimiApprovalRequest } from '~/test-support/kimiFixtures'
import { CONTROL_DECISION_WORDS } from '../persistedControlResponse'
import { createControlAnswerState } from './types'
import '../providers'

describe('permission action parity', () => {
  const providers = [AgentProvider.CLAUDE_CODE, AgentProvider.CODEX, AgentProvider.OPENCODE, AgentProvider.KILO, AgentProvider.CURSOR, AgentProvider.GITHUB_COPILOT, AgentProvider.GOOSE, AgentProvider.GROK_BUILD, AgentProvider.KIMI_CODE, AgentProvider.KIRO, AgentProvider.QWEN_CODE, AgentProvider.REASONIX, AgentProvider.PI, AgentProvider.ZCODE, AgentProvider.OH_MY_PI, AgentProvider.MIMO_CODE, AgentProvider.AMP, AgentProvider.CLINE]

  it.each(providers)('uses shared decision labels and retains the denial value for provider %s', async (provider) => {
    const payload = provider === AgentProvider.CLAUDE_CODE || provider === AgentProvider.ZCODE
      ? { request: { tool_name: 'Bash', input: { command: 'pwd' } } }
      : provider === AgentProvider.CODEX
        ? { method: 'item/commandExecution/requestApproval', params: { command: 'pwd', availableDecisions: ['accept', 'acceptForSession', 'decline', 'cancel'] } }
        : provider === AgentProvider.PI
          ? { type: 'extension_ui_request', method: 'select', title: 'MCP: probe wants to run command\n\nArguments:\n{}', options: ['Allow once', 'Allow for session', 'Deny'] }
          : provider === AgentProvider.GITHUB_COPILOT
            ? copilotPermissionRequest({ kind: 'shell', intention: 'Run command', fullCommandText: 'pwd', commands: [{ identifier: 'pwd', readOnly: true }], canOfferSessionApproval: true })
            : provider === AgentProvider.KIMI_CODE
              ? kimiApprovalRequest('Bash', { kind: 'command', command: 'pwd' })
              : provider === AgentProvider.OH_MY_PI
                ? { type: 'extension_ui_request', id: 'request', method: 'select', title: 'Allow tool: bash\nCommand: pwd', options: ['Approve', 'Deny'] }
                : provider === AgentProvider.MIMO_CODE
                  ? { type: 'permission.asked', properties: { id: 'per_1', sessionID: 'ses_1', permission: 'bash', patterns: ['pwd'], metadata: {}, always: ['pwd *'] }, request: { tool_name: 'bash', tool_use_id: 'call-1' } }
                  : provider === AgentProvider.AMP
                    ? { type: 'leapmux_amp_permission', tool_name: 'shell_command', tool_use_id: 'TU-1', input: { command: 'pwd' } }
                    : provider === AgentProvider.CLINE
                      ? clineApprovalRequest('run_commands', { commands: ['pwd'] })
                      : { method: 'session/request_permission', params: { options: [{ optionId: 'once', name: 'Allow once', kind: 'allow_once' }, { optionId: 'reject', name: 'Reject', kind: 'reject_once' }] } }
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const { getByRole } = render(() => (
      <ControlRequestActions
        agentProvider={provider}
        request={{ agentId: 'agent', requestId: 'request', payload }}
        answerState={createControlAnswerState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))
    // The SAME words a saved decision shows. A change to either side breaks here, which
    // is the point: one answer must read the same before and after the reader gives it.
    expect(getByRole('button', { name: CONTROL_DECISION_WORDS.permission.allow })).toBeVisible()
    fireEvent.click(getByRole('button', { name: CONTROL_DECISION_WORDS.permission.deny }))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    const response = JSON.parse(new TextDecoder().decode(onRespond.mock.calls[0]?.[0]))
    if (provider === AgentProvider.CODEX)
      expect(response.result).toEqual({ decision: 'decline' })
    else if (provider === AgentProvider.PI)
      expect(response.response.response).toEqual({ action: 'decline' })
    else if (provider === AgentProvider.OH_MY_PI)
      expect(response).toEqual({ type: 'extension_ui_response', id: 'request', value: 'Deny' })
    else if (provider === AgentProvider.CLAUDE_CODE || provider === AgentProvider.ZCODE || provider === AgentProvider.GITHUB_COPILOT || provider === AgentProvider.KIMI_CODE || provider === AgentProvider.AMP || provider === AgentProvider.CLINE)
      expect(response.response.response.behavior).toBe('deny')
    else
      expect(response.result).toEqual({ outcome: { outcome: 'selected', optionId: 'reject' } })
  })

  // A plan approval states no option list, so its saved row reads the words its own
  // buttons carry. Those are not the permission pair: a plan is approved, not allowed.
  it.each([AgentProvider.CLAUDE_CODE, AgentProvider.ZCODE, AgentProvider.KIMI_CODE, AgentProvider.MIMO_CODE, AgentProvider.CLINE])('draws the plan decision words for provider %s', (provider) => {
    const payload = provider === AgentProvider.CLINE
      ? clineApprovalRequest('switch_to_act_mode', {})
      : provider === AgentProvider.KIMI_CODE
        ? kimiApprovalRequest('ExitPlanMode', { kind: 'plan_review', plan: 'Do the thing.' })
        : provider === AgentProvider.MIMO_CODE
          ? { type: 'question.asked', properties: { id: 'que_1', sessionID: 'ses_1', questions: [{ key: 'plan_exit', params: { plan: 'plan.md' }, question: 'Approve?' }] }, request: { tool_name: 'plan_exit', tool_use_id: 'call-1' }, plan: 'Do the thing.' }
          : { request: { tool_name: 'ExitPlanMode', input: { plan: 'Do the thing.' } } }
    const { getByRole } = render(() => (
      <ControlRequestActions
        agentProvider={provider}
        request={{ agentId: 'agent', requestId: 'request', payload }}
        answerState={createControlAnswerState()}
        onRespond={vi.fn().mockResolvedValue(undefined)}
        hasEditorContent={false}
        onTriggerSend={vi.fn()}
      />
    ))
    expect(getByRole('button', { name: CONTROL_DECISION_WORDS.plan.allow })).toBeVisible()
    expect(getByRole('button', { name: CONTROL_DECISION_WORDS.plan.deny })).toBeVisible()
  })

  // Grok and Qwen raise a plan approval of their own, and both draw the shared plan
  // surface: Approve sends the neutral allow, whose reply the worker writes, and
  // Reject hands the composer's text to the send path.
  it.each([
    [AgentProvider.GROK_BUILD, { jsonrpc: '2.0', id: 1, method: '_x.ai/exit_plan_mode', params: { toolCallId: 'c', planContent: 'Do the thing.' } }],
    [AgentProvider.QWEN_CODE, { jsonrpc: '2.0', id: 5, method: 'session/request_permission', params: {
      options: [{ optionId: 'proceed_once', name: 'Yes', kind: 'allow_once' }, { optionId: 'cancel', name: 'No', kind: 'reject_once' }],
      toolCall: { toolCallId: 'p', kind: 'switch_mode', rawInput: { plan: 'Do the thing.' }, _meta: { toolName: 'exit_plan_mode' } },
    } }],
  ])('draws the plan decision words for the ACP provider %s', async (provider, payload) => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    const onTriggerSend = vi.fn()
    const { getByRole } = render(() => (
      <ControlRequestActions
        agentProvider={provider}
        request={{ agentId: 'agent', requestId: 'jsonrpc:1', payload }}
        answerState={createControlAnswerState()}
        onRespond={onRespond}
        hasEditorContent={false}
        onTriggerSend={onTriggerSend}
      />
    ))
    fireEvent.click(getByRole('button', { name: CONTROL_DECISION_WORDS.plan.deny }))
    expect(onTriggerSend).toHaveBeenCalledOnce()
    fireEvent.click(getByRole('button', { name: CONTROL_DECISION_WORDS.plan.allow }))
    await vi.waitFor(() => expect(onRespond).toHaveBeenCalledOnce())
    const response = JSON.parse(new TextDecoder().decode(onRespond.mock.calls[0]?.[0]))
    expect(response.response.response.behavior).toBe('allow')
  })
})
