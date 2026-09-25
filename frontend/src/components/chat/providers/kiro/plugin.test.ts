import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { describeACPProviderBasics } from '../acp/testUtils'
import { providerFor } from '../registry'
import { input } from '../testUtils'

import './plugin'

vi.mock('~/api/workerRpc', () => ({
  updateAgentSettings: vi.fn(),
}))

/** Kiro's end of a turn it started by itself, as the worker stores it. */
function turnEnd(stopReason: string) {
  return { sessionUpdate: 'session_info_update', _meta: { kiro: { turnEnd: { stopReason }, kind: 'turn_end', stopReason, messageId: 'm' } } }
}

describe('kiro provider', () => {
  const plugin = providerFor(AgentProvider.KIRO)!

  // The same policy as the worker's ValidateAttachment.
  describeACPProviderBasics(AgentProvider.KIRO, { text: true, image: true, pdf: true, binary: false })

  it('carries plan mode on the permission-mode axis', () => {
    expect(plugin.configuration?.planMode).toMatchObject({ groupKey: 'permissionMode', planValue: 'plan', defaultValue: 'vibe' })
    expect(plugin.configuration?.triggerModeGroupKey).toBe('permissionMode')
  })

  // Kiro's default mode is `vibe`, so an agent that reports no mode yet is not in plan mode.
  it('reads the current mode from the permission-mode option, and the default mode when it is absent', () => {
    const planMode = plugin.configuration?.planMode
    expect(planMode?.currentMode({ optionValues: { permissionMode: 'plan' } })).toBe('plan')
    expect(planMode?.currentMode({ optionValues: {} })).toBe('vibe')
    expect(planMode?.currentMode({})).toBe('vibe')
  })

  it('states its own effort axis for the effort chip', () => {
    expect(plugin.configuration?.effortGroupKey).toBe('effortLevel')
  })

  // Kiro never reports its policy preset, so LeapMux holds it as an option of its
  // own. Kiro has no preset between its own rules and every call, so only Bypass maps.
  it('maps bypass to the allow-all policy preset, and offers no smart preset', () => {
    expect(plugin.controls?.permissionPresets).toEqual({
      bypass: { sets: { policyPreset: 'allow-all' } },
    })
  })

  it('reads its own MCP elicitation method', () => {
    expect(plugin.controls?.elicitation?.({ method: '_kiro/mcp/elicitation', params: { elicitation: { mode: 'form', message: 'Pick one', requestedSchema: { type: 'object' } } } })).toMatchObject({
      mode: 'form',
      message: 'Pick one',
      schema: { type: 'object' },
    })
  })

  it('shows a saved MCP form answer through the shared form display', () => {
    const request = { jsonrpc: '2.0', id: 1, method: '_kiro/mcp/elicitation', params: { elicitation: { mode: 'form', message: 'Choose', requestedSchema: { type: 'object', properties: { size: { type: 'string', title: 'Size' } } } } } }
    const display = plugin.controls?.controlResponseDisplay?.({ requestId: 'jsonrpc:1', claimToken: 'c', request, response: { jsonrpc: '2.0', id: 1, result: { action: 'accept', content: { size: 'Large' } } } })
    expect(display).toEqual({ kind: 'label', text: 'Approved\nSize: Large' })
  })

  it('recognizes its question dialog', () => {
    expect(plugin.controls?.askUserQuestion?.isRequest({ method: '_kiro/userInput' })).toBe(true)
    expect(plugin.controls?.askUserQuestion?.isRequest({ method: 'session/request_permission' })).toBe(false)
  })

  it('carries a rejection reason in Kiro\'s own field rather than as a message', () => {
    const permission = { method: 'session/request_permission', params: { toolCall: { toolCallId: 'c' }, options: [{ optionId: 'accept', kind: 'allow_once' }, { optionId: 'reject', kind: 'reject_once' }] } }
    expect(plugin.controls?.controlFeedbackAsFollowUpMessage?.(permission)).toBe(false)
    expect(plugin.controls?.buildControlResponse?.(permission, 'Use rg', 'jsonrpc:1')).toEqual({
      jsonrpc: '2.0',
      id: 'jsonrpc:1',
      result: { outcome: { outcome: 'selected', optionId: 'reject' }, _meta: { kiro: { rejectionReason: 'Use rg' } } },
    })
  })

  describe('the end of a turn Kiro started', () => {
    it('draws a divider from the stop reason of turn_end', () => {
      const parent = turnEnd('end_turn')
      expect(plugin.transcript.classify(input(parent, null, AgentProvider.KIRO))).toEqual({ kind: 'result_divider' })
      expect(plugin.transcript.extractDivider(parent)).toEqual({ label: 'Turn ended' })
    })

    it('states a reason that is not the ordinary end', () => {
      expect(plugin.transcript.extractDivider(turnEnd('max_tokens'))?.label).toContain('max_tokens')
      expect(plugin.transcript.extractDivider(turnEnd('cancelled'))).toEqual({ label: 'Turn interrupted' })
    })

    it('still reads the prompt response of a turn LeapMux started', () => {
      const parent = { stopReason: 'end_turn' }
      expect(plugin.transcript.classify(input(parent, null, AgentProvider.KIRO))).toEqual({ kind: 'result_divider' })
      expect(plugin.transcript.extractDivider(parent)).toEqual({ label: 'Turn ended' })
    })

    it('hides every other session_info_update', () => {
      const parent = { sessionUpdate: 'session_info_update', _meta: { kiro: { kind: 'turn_start' } } }
      expect(plugin.transcript.classify(input(parent, null, AgentProvider.KIRO))).toEqual({ kind: 'hidden' })
    })
  })
})
