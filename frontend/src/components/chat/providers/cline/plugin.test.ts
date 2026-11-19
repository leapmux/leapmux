import { describe, expect, it } from 'vitest'
import { CLINE_PERMISSION_MODE } from '~/generated/contracts/cline-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { providerFor } from '../registry'
import { classifyClineMessage } from './classification'
import { clineCompactionBoundary, clineCompactionStart, clineNotificationEntry } from './extractors/notification'
import { clineResultDivider } from './extractors/resultDivider'
import { clineExtractRow } from './extractors/row'
import { clineConfiguration } from './pluginConfiguration'
import { clineControls } from './pluginControls'
import { clineNeedsRequest, clineSpanRole } from './spanRole'
import './plugin'

const plugin = providerFor(AgentProvider.CLINE)!
const controls = plugin.controls!

describe('cline plugin', () => {
  // A registration that is present but holds the readers of another provider passes a
  // presence check, so the test compares each hook with Cline's own function.
  it('registers the Cline readers for the CLINE provider', () => {
    expect(Object.keys(plugin.transcript).sort()).toEqual([
      'classify',
      'compactionStart',
      'extractDivider',
      'extractRow',
      'needsRequest',
      'notificationEntry',
      'spanRole',
    ])
    expect(plugin.transcript.compactionStart).toBe(clineCompactionStart)
    expect(plugin.transcript.classify).toBe(classifyClineMessage)
    expect(plugin.transcript.extractRow).toBe(clineExtractRow)
    expect(plugin.transcript.extractDivider).toBe(clineResultDivider)
    expect(plugin.transcript.notificationEntry).toBe(clineNotificationEntry)
    expect(plugin.transcript.spanRole).toBe(clineSpanRole)
    expect(plugin.transcript.needsRequest).toBe(clineNeedsRequest)
    expect(plugin.session?.compactionBoundaryFromMessage).toBe(clineCompactionBoundary)
    expect(plugin.controls).toBe(clineControls)
    expect(plugin.configuration).toBe(clineConfiguration)
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

  it('carries Plan on the permission-mode axis, as the plan toggle', () => {
    expect(plugin.configuration?.triggerModeGroupKey).toBe('permissionMode')
    expect(plugin.configuration?.planMode?.groupKey).toBe('permissionMode')
    expect(plugin.configuration?.planMode?.planValue).toBe(CLINE_PERMISSION_MODE.Plan)
    expect(plugin.configuration?.planMode?.defaultValue).toBe(CLINE_PERMISSION_MODE.Act)
    expect(plugin.configuration?.planMode?.currentMode({
      optionValues: { permissionMode: CLINE_PERMISSION_MODE.Plan },
    })).toBe(CLINE_PERMISSION_MODE.Plan)
  })

  it('states no child input', () => {
    expect(plugin.configuration?.supportsSubagentSend).toBeUndefined()
  })
})

describe('cline controls', () => {
  // The Worker reads each request, builds each answer, and records the decision that
  // words a saved answer, so the browser keeps only the presets.
  it('registers only the presets', () => {
    expect(Object.keys(controls)).toEqual(['permissionPresets'])
  })

  it('offers the Bypass preset as Auto-approve and no Smart preset', () => {
    expect(controls.permissionPresets).toEqual({
      bypass: {
        sets: { permissionMode: CLINE_PERMISSION_MODE.AutoApprove },
      },
    })
  })
})

describe('native output without a filesystem pointer', () => {
  it('keeps a valid native result and omits the output path hook', () => {
    const frame = {
      version: 'v1',
      event: 'tool.finished',
      sessionId: 'native-session',
      payload: {
        toolCallId: 'native-call',
        toolName: 'run_commands',
        output: [
          {
            query: 'printf preview',
            result: 'native inline preview',
            success: true,
          },
        ],
      },
    }
    const call = providerToolCall(AgentProvider.CLINE, frame, {
      spanId: 'native-call',
      spanType: 'run_commands',
      agentSessionId: 'native-session',
      role: 'result',
    })
    expect(call).not.toBeNull()
    expect(call?.id).toBe('native-call')
    expect(call?.outputFilePaths).toBeUndefined()
    const registered = providerFor(AgentProvider.CLINE)
    expect(registered).toBeDefined()
    expect(registered?.transcript.outputFilePaths).toBeUndefined()
  })
})
