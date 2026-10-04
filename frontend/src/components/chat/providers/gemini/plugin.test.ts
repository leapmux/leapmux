import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { describeACPProviderBasics } from '../acp/testUtils'
import { providerFor } from '../registry'

import './plugin'

describe('gemini provider', () => {
  describeACPProviderBasics(AgentProvider.GEMINI_CLI, { text: true, image: true, pdf: true, binary: true })

  it('exposes native modes and a bypass permission preset', () => {
    const plugin = providerFor(AgentProvider.GEMINI_CLI)
    expect(plugin?.configuration?.triggerModeGroupKey).toBe('permissionMode')
    expect(plugin?.configuration?.planMode).toBeDefined()
    expect(plugin?.controls?.permissionPresets).toEqual({ bypass: { sets: { permissionMode: 'yolo' } } })
    expect(plugin?.configuration?.effortGroupKey).toBeUndefined()
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
        output: 'native inline preview',
        exitCode: 0,
      },
    }
    const call = providerToolCall(AgentProvider.GEMINI_CLI, frame, { spanId: 'native-call', spanType: 'run_shell_command', agentSessionId: 'native-session', role: 'result' })
    expect(call).not.toBeNull()
    expect(call?.id).toBe('native-call')
    expect(call?.outputFilePaths).toBeUndefined()
    const registered = providerFor(AgentProvider.GEMINI_CLI)
    expect(registered).toBeDefined()
    expect(registered?.transcript.outputFilePaths).toBeUndefined()
  })
})
