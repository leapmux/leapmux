import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider, ControlResponseState } from '~/generated/proto/leapmux/v1/agent_pb'
import { clineApprovalRequest } from '~/test-support/clineFixtures'
import { ControlRequestContent } from '~/test-support/controlRequestBanner'
import { copilotPermissionRequest } from '~/test-support/copilotFixtures'
import { kimiApprovalRequest } from '~/test-support/kimiFixtures'
import { createControlAnswerState } from './types'
import '../providers'

describe('permission content parity', () => {
  const command = 'printf "first line\\n"\nprintf "second line\\n"'
  const input = { command, timeout: 0, quiet: false, note: '' }
  const providers = [AgentProvider.CLAUDE_CODE, AgentProvider.CODEX, AgentProvider.OPENCODE, AgentProvider.KILO, AgentProvider.CURSOR, AgentProvider.GITHUB_COPILOT, AgentProvider.GOOSE, AgentProvider.GROK_BUILD, AgentProvider.KIMI_CODE, AgentProvider.KIRO, AgentProvider.QWEN_CODE, AgentProvider.REASONIX, AgentProvider.ZCODE, AgentProvider.OH_MY_PI, AgentProvider.MIMO_CODE, AgentProvider.AMP, AgentProvider.CLINE]

  // Kimi Code's approval is its own event, and its display states the command and its
  // working directory; the server sends no free-form tool input beside it.
  const kimiCommandRequest = kimiApprovalRequest('Bash', { kind: 'command', command, cwd: '/workspace', description: 'Inspect the command.', language: 'bash' })

  // omp states the tool and its command in its approval dialog's title (`tools/approval.ts`).
  const ohMyPiApproval = { type: 'extension_ui_request', id: 'request', method: 'select', title: `Allow tool: bash\nCommand: ${command}`, options: ['Approve', 'Deny'] }

  // MiMo states the command as the request's patterns, and the rest of the call's
  // facts as its metadata.
  const { command: _command, ...mimoMetadata } = input
  const mimoShellRequest = {
    type: 'permission.asked',
    properties: { id: 'per_1', sessionID: 'ses_1', permission: 'bash', patterns: [command], metadata: mimoMetadata },
    request: { tool_name: 'bash', tool_use_id: 'call-1' },
  }

  // Amp's request is the envelope the worker writes for each call Amp's delegate rule
  // hands to the LeapMux helper: the tool and its whole input.
  const ampShellRequest = { type: 'leapmux_amp_permission', tool_name: 'shell_command', tool_use_id: 'TU-1', input }

  // Cline's request is its own approval event, whose arguments state the list of
  // commands the call runs.
  const clineShellRequest = clineApprovalRequest('run_commands', { commands: [command] })

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
      : provider === AgentProvider.OH_MY_PI
        ? ohMyPiApproval
        : provider === AgentProvider.CLAUDE_CODE || provider === AgentProvider.ZCODE
          ? { request: { tool_name: 'Bash', input } }
          : provider === AgentProvider.CODEX
            ? { method: 'item/commandExecution/requestApproval', params: { command } }
            : provider === AgentProvider.GITHUB_COPILOT
              ? copilotShellRequest
              : provider === AgentProvider.KIMI_CODE
                ? kimiCommandRequest
                : provider === AgentProvider.MIMO_CODE
                  ? mimoShellRequest
                  : provider === AgentProvider.AMP
                    ? ampShellRequest
                    : provider === AgentProvider.CLINE
                      ? clineShellRequest
                      : { method: 'session/request_permission', params: { toolCall: { toolCallId: 'command', kind: 'execute', title: 'Run command', rawInput: input } } }
    const { getByText, queryByText } = render(() => (
      <ControlRequestContent agentProvider={provider} request={{ agentId: 'agent', requestId: 'request', payload, responseState: ControlResponseState.DELIVERED }} answerState={createControlAnswerState()} />
    ))
    expect(getByText('Permission request', { exact: true })).toBeVisible()
    expect(queryByText('Permission Required', { exact: true })).toBeNull()
  })

  it.each(providers)('renders a command with the shared permission heading for provider %s', (provider) => {
    const payload = provider === AgentProvider.OH_MY_PI
      ? ohMyPiApproval
      : provider === AgentProvider.CLAUDE_CODE || provider === AgentProvider.ZCODE
        ? { request: { tool_name: 'Bash', input } }
        : provider === AgentProvider.CODEX
          ? { method: 'item/commandExecution/requestApproval', params: { command, cwd: '/workspace', reason: 'Inspect the command.' } }
          : provider === AgentProvider.GITHUB_COPILOT
            ? copilotShellRequest
            : provider === AgentProvider.KIMI_CODE
              ? kimiCommandRequest
              : provider === AgentProvider.MIMO_CODE
                ? mimoShellRequest
                : provider === AgentProvider.AMP
                  ? ampShellRequest
                  : provider === AgentProvider.CLINE
                    ? clineShellRequest
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
    else if (provider === AgentProvider.KIMI_CODE) {
      // The display is the whole statement of the call: its working directory, and the
      // description the model gave the command.
      expect(container.textContent).toContain('/workspace')
      expect(container.textContent).toContain('Inspect the command.')
    }
    else if (provider === AgentProvider.OH_MY_PI) {
      // omp states the tool and the command in its dialog's title, and no other input.
      expect(getByText('bash')).toBeVisible()
    }
    else if (provider === AgentProvider.CLINE) {
      // Cline states the commands as a list, which the arguments show beside the command.
      const metadata = blocks.find(block => block?.includes('"commands"'))
      expect(JSON.parse(metadata!)).toEqual({ commands: [command] })
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
