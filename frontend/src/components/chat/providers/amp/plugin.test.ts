import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { AMP_OPTION, AMP_PERMISSION_MODE } from '~/generated/contracts/amp-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { buildAllowResponse, buildDenyResponse, CONTROL_REJECTED_BY_USER_MESSAGE } from '~/utils/controlResponse'
import { providerFor } from '../registry'
import { classifyAmpMessage } from './classification'
import { ampResultDivider } from './extractors/resultDivider'
import { ampExtractRow } from './extractors/row'
import { ampControls } from './pluginControls'
import { ampRelatedMessages, ampSpanRole } from './spanRole'
import './plugin'

const plugin = providerFor(AgentProvider.AMP)!
const controls = plugin.controls!

const permission = { type: 'leapmux_amp_permission', tool_name: 'shell_command', tool_use_id: 'TU-1', input: { command: 'ls' } }

function saved(response: Record<string, unknown> | undefined): PersistedControlResponse {
  return { requestId: 'amp-permission-0a0b0c0d-1', claimToken: '', request: permission, response }
}

describe('amp plugin', () => {
  // A registration that is present but holds the readers of another provider passes a
  // presence check, so the test compares each hook with Amp's own function.
  it('registers the Amp readers for the AMP provider', () => {
    expect(Object.keys(plugin.transcript).sort()).toEqual(['classify', 'extractDivider', 'extractRow', 'relatedMessages', 'spanRole'])
    expect(plugin.transcript.classify).toBe(classifyAmpMessage)
    expect(plugin.transcript.extractRow).toBe(ampExtractRow)
    expect(plugin.transcript.extractDivider).toBe(ampResultDivider)
    expect(plugin.transcript.spanRole).toBe(ampSpanRole)
    expect(plugin.transcript.relatedMessages).toBe(ampRelatedMessages)
    expect(plugin.controls).toBe(ampControls)
    // Amp writes no notification and states no compaction of its own.
    expect(plugin.session).toBeUndefined()
  })

  // The worker's ValidateAttachment refuses a PDF and a binary file with the same policy.
  it('accepts text and images, and no PDF or binary', () => {
    expect(plugin.configuration?.attachments).toEqual({ text: true, image: true, pdf: false, binary: false })
  })

  it('labels the settings trigger with the agent mode', () => {
    expect(plugin.configuration?.triggerModeGroupKey).toBe(AMP_OPTION.AgentMode)
  })

  it('states no plan mode, no effort axis and no child input', () => {
    expect(plugin.configuration?.planMode).toBeUndefined()
    expect(plugin.configuration?.effortGroupKey).toBeUndefined()
    expect(plugin.configuration?.supportsSubagentSend).toBeUndefined()
    expect(plugin.controls?.askUserQuestion).toBeUndefined()
    expect(plugin.controls?.elicitation).toBeUndefined()
  })
})

describe('amp controls', () => {
  it('offers the Bypass preset as Allow All and no Smart preset', () => {
    expect(controls.permissionPresets).toEqual({ bypass: { sets: { permissionMode: AMP_PERMISSION_MODE.AllowAll } } })
  })

  it('reads the permission envelope into the permission row', () => {
    expect(controls.extractControl?.({ payload: permission })?.kind).toBe('permission')
  })

  // The composer's send refuses the call, and its words ride the refusal as the reason.
  it('sends the composer\'s text as a refusal', () => {
    expect(controls.buildControlResponse?.(permission, 'Use the clean target.', 'r1')).toEqual(buildDenyResponse('r1', 'Use the clean target.'))
    expect(controls.buildControlResponse?.(permission, '', 'r1')).toEqual(buildDenyResponse('r1', CONTROL_REJECTED_BY_USER_MESSAGE))
  })

  it('displays a saved answer from the neutral envelope', () => {
    const display = controls.controlResponseDisplay!
    expect(display(saved(buildAllowResponse('r1', {})))).toEqual({ kind: 'label', text: 'Allow' })
    expect(display(saved(buildDenyResponse('r1', 'Not that one.')))).toEqual({ kind: 'feedback', message: 'Not that one.' })
    expect(display(saved(buildDenyResponse('r1')))).toEqual({ kind: 'label', text: 'Deny' })
    expect(display(saved(undefined))).toBeNull()
    expect(display(saved({ jsonrpc: '2.0', id: 1, result: {} }))).toBeNull()
  })
})
