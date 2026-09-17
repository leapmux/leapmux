import { fireEvent, render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { ControlRequestActions } from '~/test-support/controlRequestBanner'
import { copilotPermissionRequest } from '~/test-support/copilotFixtures'
import { CONTROL_DECISION_WORDS } from '../persistedControlResponse'
import { createControlAnswerState } from './types'
import '../providers'

describe('permission action parity', () => {
  const providers = [AgentProvider.CLAUDE_CODE, AgentProvider.CODEX, AgentProvider.OPENCODE, AgentProvider.KILO, AgentProvider.CURSOR, AgentProvider.GITHUB_COPILOT, AgentProvider.GOOSE, AgentProvider.REASONIX, AgentProvider.PI, AgentProvider.ZCODE]

  it.each(providers)('uses shared decision labels and retains the denial value for provider %s', async (provider) => {
    const payload = provider === AgentProvider.CLAUDE_CODE || provider === AgentProvider.ZCODE
      ? { request: { tool_name: 'Bash', input: { command: 'pwd' } } }
      : provider === AgentProvider.CODEX
        ? { method: 'item/commandExecution/requestApproval', params: { command: 'pwd', availableDecisions: ['accept', 'acceptForSession', 'decline', 'cancel'] } }
        : provider === AgentProvider.PI
          ? { type: 'extension_ui_request', method: 'select', title: 'MCP: probe wants to run command\n\nArguments:\n{}', options: ['Allow once', 'Allow for session', 'Deny'] }
          : provider === AgentProvider.GITHUB_COPILOT
            ? copilotPermissionRequest({ kind: 'shell', intention: 'Run command', fullCommandText: 'pwd', commands: [{ identifier: 'pwd', readOnly: true }], canOfferSessionApproval: true })
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
    else if (provider === AgentProvider.CLAUDE_CODE || provider === AgentProvider.ZCODE || provider === AgentProvider.GITHUB_COPILOT)
      expect(response.response.response.behavior).toBe('deny')
    else
      expect(response.result).toEqual({ outcome: { outcome: 'selected', optionId: 'reject' } })
  })

  // A plan approval states no option list, so its saved row reads the words its own
  // buttons carry. Those are not the permission pair: a plan is approved, not allowed.
  it.each([AgentProvider.CLAUDE_CODE, AgentProvider.ZCODE])('draws the plan decision words for provider %s', (provider) => {
    const payload = provider === AgentProvider.CLAUDE_CODE
      ? { request: { tool_name: 'ExitPlanMode', input: { plan: 'Do the thing.' } } }
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
})
