import { describe, expect, it } from 'vitest'
import { AMP_OPTION, AMP_PERMISSION_MODE } from '~/generated/contracts/amp-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { providerFor } from '../registry'
import { classifyAmpMessage } from './classification'
import { ampResultDivider } from './extractors/resultDivider'
import { ampExtractRow } from './extractors/row'
import { ampControls } from './pluginControls'
import { ampNeedsRequest, ampSpanRole } from './spanRole'
import './plugin'

const plugin = providerFor(AgentProvider.AMP)!
const controls = plugin.controls!

describe('amp plugin', () => {
  // A registration that is present but holds the readers of another provider passes a
  // presence check, so the test compares each hook with Amp's own function.
  it('registers the Amp readers for the AMP provider', () => {
    expect(Object.keys(plugin.transcript).sort()).toEqual([
      'classify',
      'extractDivider',
      'extractRow',
      'needsRequest',
      'spanRole',
    ])
    expect(plugin.transcript.classify).toBe(classifyAmpMessage)
    expect(plugin.transcript.extractRow).toBe(ampExtractRow)
    expect(plugin.transcript.extractDivider).toBe(ampResultDivider)
    expect(plugin.transcript.spanRole).toBe(ampSpanRole)
    expect(plugin.transcript.needsRequest).toBe(ampNeedsRequest)
    expect(plugin.controls).toBe(ampControls)
    // Amp writes no notification and states no compaction of its own.
    expect(plugin.session).toBeUndefined()
  })

  // The worker's ValidateAttachment refuses a PDF and a binary file with the same policy.
  it('accepts text and images, and no PDF or binary', () => {
    expect(plugin.configuration?.attachments).toEqual({
      text: true,
      image: true,
      pdf: false,
      audio: false,
      binary: false,
    })
  })

  it('labels the settings trigger with the agent mode', () => {
    expect(plugin.configuration?.triggerModeGroupKey).toBe(AMP_OPTION.AgentMode)
  })

  it('states no plan mode, no effort axis and no child input', () => {
    expect(plugin.configuration?.planMode).toBeUndefined()
    expect(plugin.configuration?.effortGroupKey).toBeUndefined()
    expect(plugin.configuration?.supportsSubagentSend).toBeUndefined()
  })
})

describe('amp controls', () => {
  // The Worker reads each request, builds each answer, and records the decision that
  // words a saved answer, so the browser keeps only the presets.
  it('registers only the presets', () => {
    expect(Object.keys(controls)).toEqual(['permissionPresets'])
  })

  it('offers the Bypass preset as Allow All and no Smart preset', () => {
    expect(controls.permissionPresets).toEqual({
      bypass: {
        sets: { permissionMode: AMP_PERMISSION_MODE.AllowAll },
      },
    })
  })
})

describe('native output without a filesystem pointer', () => {
  it('keeps a valid native result and omits the output path hook', () => {
    const frame = {
      type: 'user',
      session_id: 'native-session',
      message: {
        role: 'user',
        content: [
          {
            type: 'tool_result',
            tool_use_id: 'native-call',
            content: '{"output":"native inline preview","exitCode":0}',
            is_error: false,
          },
        ],
      },
    }
    const call = providerToolCall(AgentProvider.AMP, frame, {
      spanId: 'native-call',
      spanType: 'shell_command',
      agentSessionId: 'native-session',
      role: 'result',
    })
    expect(call).not.toBeNull()
    expect(call?.id).toBe('native-call')
    expect(call?.outputFilePaths).toBeUndefined()
    const registered = providerFor(AgentProvider.AMP)
    expect(registered).toBeDefined()
    expect(registered?.transcript.outputFilePaths).toBeUndefined()
  })
})
