import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider, ControlResponseState } from '~/generated/proto/leapmux/v1/agent_pb'
import { ControlRequestContent } from '~/test-support/controlRequestBanner'
import { copilotPermissionRequest } from '~/test-support/copilotFixtures'
import { createControlAnswerState } from './types'
import '../providers'

describe('permission content parity', () => {
  const command = 'printf "first line\\n"\nprintf "second line\\n"'
  const input = { command, timeout: 0, quiet: false, note: '' }
  const providers = [AgentProvider.CLAUDE_CODE, AgentProvider.CODEX, AgentProvider.OPENCODE, AgentProvider.KILO, AgentProvider.CURSOR, AgentProvider.GITHUB_COPILOT, AgentProvider.GOOSE, AgentProvider.REASONIX, AgentProvider.ZCODE]

  // Copilot's request is its own native event, and its shell kind states the command
  // in `fullCommandText` beside the identifiers a session-wide approval would use.
  const copilotShellRequest = copilotPermissionRequest({
    kind: 'shell',
    intention: 'Run command',
    fullCommandText: command,
    commands: [{ identifier: 'printf', readOnly: false }],
    canOfferSessionApproval: true,
  })

  it.each([...providers, AgentProvider.PI])('does not request another approval after provider %s receives a response', (provider) => {
    const payload = provider === AgentProvider.PI
      ? { type: 'extension_ui_request', method: 'select', title: `MCP: probe wants to run command\n\nArguments:\n${JSON.stringify(input)}`, options: ['Allow once', 'Allow for session', 'Deny'] }
      : provider === AgentProvider.CLAUDE_CODE || provider === AgentProvider.ZCODE
        ? { request: { tool_name: 'Bash', input } }
        : provider === AgentProvider.CODEX
          ? { method: 'item/commandExecution/requestApproval', params: { command } }
          : provider === AgentProvider.GITHUB_COPILOT
            ? copilotShellRequest
            : { method: 'session/request_permission', params: { toolCall: { toolCallId: 'command', kind: 'execute', title: 'Run command', rawInput: input } } }
    const { getByText, queryByText } = render(() => (
      <ControlRequestContent agentProvider={provider} request={{ agentId: 'agent', requestId: 'request', payload, responseState: ControlResponseState.DELIVERED }} answerState={createControlAnswerState()} />
    ))
    expect(getByText('Permission request', { exact: true })).toBeVisible()
    expect(queryByText('Permission Required', { exact: true })).toBeNull()
  })

  it.each(providers)('renders a command with the shared permission heading for provider %s', (provider) => {
    const payload = provider === AgentProvider.CLAUDE_CODE || provider === AgentProvider.ZCODE
      ? { request: { tool_name: 'Bash', input } }
      : provider === AgentProvider.CODEX
        ? { method: 'item/commandExecution/requestApproval', params: { command, cwd: '/workspace', reason: 'Inspect the command.' } }
        : provider === AgentProvider.GITHUB_COPILOT
          ? copilotShellRequest
          : { method: 'session/request_permission', params: { toolCall: { toolCallId: 'command', kind: 'execute', title: 'Run command', rawInput: input } } }
    const { container, getByText } = render(() => (
      <ControlRequestContent
        agentProvider={provider}
        request={{ agentId: 'agent', requestId: 'request', payload }}
        answerState={createControlAnswerState()}
      />
    ))
    expect(getByText('Permission Required', { exact: true })).toBeVisible()
    const blocks = Array.from(container.querySelectorAll('pre')).map(block => block.textContent)
    expect(blocks).toContain(command)
    if (provider === AgentProvider.CODEX) {
      expect(getByText('Inspect the command.')).toBeVisible()
      expect(container.textContent).toContain('/workspace')
    }
    else if (provider === AgentProvider.GITHUB_COPILOT) {
      // Copilot states no free-form tool input. Its own request fields are the detail.
      const metadata = blocks.find(block => block?.includes('"identifier"'))
      expect(metadata).toContain('"kind": "shell"')
      expect(metadata).toContain('"identifier": "printf"')
    }
    else {
      const metadata = blocks.find(block => block?.includes('"timeout"'))
      expect(JSON.parse(metadata!)).toEqual({ timeout: 0, quiet: false, note: '' })
    }
  })
})
