import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { describeACPProviderBasics } from '../acp/testUtils'
import { providerFor } from '../registry'
import './plugin'

const MODE_AUTO = 'auto'
const MODE_SMART_APPROVE = 'smart_approve'

describe('goose provider', () => {
  const plugin = providerFor(AgentProvider.GOOSE)!

  describeACPProviderBasics(AgentProvider.GOOSE, { text: true, image: true, pdf: false, binary: false })

  it('maps smart and bypass permissions to Goose modes', () => {
    expect(plugin?.controls?.permissionPresets).toEqual({
      smart: { sets: { permissionMode: MODE_SMART_APPROVE } },
      bypass: { sets: { permissionMode: MODE_AUTO } },
    })
  })

  it('has no plan mode', () => {
    // Goose's writable axis is the top-level permission mode (no plan toggle);
    // the generic settings panel renders the permissionMode group it reports.
    expect(plugin?.configuration?.planMode).toBeUndefined()
  })

  it('still renders the permissionMode group as the trigger mode segment (no plan mode needed)', () => {
    // The trigger mode segment is decoupled from plan mode: Goose has a mode axis
    // (permissionMode) without a plan toggle, so it still declares triggerModeGroupKey.
    expect(plugin?.configuration?.triggerModeGroupKey).toBe('permissionMode')
  })

  it('states its own reasoning axis for the effort chip', () => {
    expect(plugin?.configuration?.effortGroupKey).toBe('thinking_effort')
  })

  it('derives a control-response label via the default ACP permission path (no question hook)', () => {
    // Goose has no question protocol, so it gets the shared acpControlResponseSummary default.
    expect(plugin?.controls?.controlResponseDisplay!({
      claimToken: 'claim-1',
      requestId: '7',
      request: { method: 'session/request_permission', params: { options: [{ optionId: 'proceed_once', name: 'Allow once' }] } },
      response: { result: { outcome: { optionId: 'proceed_once' } } },
    })).toEqual({ kind: 'label', text: 'Allow once' })
  })
})

describe('native output without a filesystem pointer', () => {
  it('keeps a valid native result and omits the output path hook', () => {
    const frame = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'native-call',
      status: 'completed',
      kind: 'execute',
      title: 'native command',
      rawInput: {
        command: 'printf preview',
      },
      content: [
        {
          type: 'content',
          content: {
            type: 'text',
            text: 'native inline preview',
          },
        },
      ],
      rawOutput: {
        stdout: 'native inline preview',
        stderr: '',
        exit_code: 0,
      },
      _meta: {
        goose: {
          toolCall: {
            extensionName: 'developer',
            toolName: 'shell',
          },
        },
      },
    }
    const call = providerToolCall(AgentProvider.GOOSE, frame, { spanId: 'native-call', spanType: 'shell', agentSessionId: 'native-session', role: 'result' })
    expect(call).not.toBeNull()
    expect(call?.id).toBe('native-call')
    expect(call?.outputFilePaths).toBeUndefined()
    const registered = providerFor(AgentProvider.GOOSE)
    expect(registered).toBeDefined()
    expect(registered?.transcript.outputFilePaths).toBeUndefined()
  })
})
