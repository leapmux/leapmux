import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { describeACPProviderBasics } from '../acp/testUtils'
import { providerFor } from '../registry'
import { input } from '../testUtils'

import './plugin'

vi.mock('~/api/workerRpc', () => ({
  updateAgentSettings: vi.fn(),
}))

/** A turn end that Grok states for a turn it started by itself. */
function turnCompleted(update: Record<string, unknown>) {
  return { jsonrpc: '2.0', method: '_x.ai/session_notification', params: { sessionId: 's', update: { sessionUpdate: 'turn_completed', ...update } } }
}

describe('grok provider', () => {
  const plugin = providerFor(AgentProvider.GROK_BUILD)!

  describeACPProviderBasics(AgentProvider.GROK_BUILD, { text: true, image: true, pdf: true, binary: true })

  it('carries plan mode on the permission-mode axis', () => {
    expect(plugin.configuration?.planMode).toMatchObject({ groupKey: 'permissionMode', planValue: 'plan', defaultValue: 'default' })
    expect(plugin.configuration?.triggerModeGroupKey).toBe('permissionMode')
  })

  // An agent that reports no mode yet is in Grok's default mode, not in plan mode.
  it('reads the current mode from the permission-mode option, and the default mode when it is absent', () => {
    const planMode = plugin.configuration?.planMode
    expect(planMode?.currentMode({ optionValues: { permissionMode: 'plan' } })).toBe('plan')
    expect(planMode?.currentMode({ optionValues: { permissionMode: 'ask' } })).toBe('ask')
    expect(planMode?.currentMode({ optionValues: {} })).toBe('default')
    expect(planMode?.currentMode({})).toBe('default')
  })

  it('states its own reasoning axis for the effort chip', () => {
    expect(plugin.configuration?.effortGroupKey).toBe('reasoning_effort')
  })

  // Grok never reports its own approval mode, so LeapMux holds it as an option of
  // its own, and each preset sets that option.
  it('maps smart and bypass permissions to the approval mode', () => {
    expect(plugin.controls?.permissionPresets).toEqual({
      smart: { sets: { approvalMode: 'auto' } },
      bypass: { sets: { approvalMode: 'always-approve' } },
    })
  })

  // Grok's reply carries the chosen options and the reader's notes for the same
  // question, so the dialog keeps both.
  it('keeps a typed note beside a chosen option', () => {
    expect(plugin.controls?.preservesSelectionNotes).toBe(true)
  })

  it('reads its own MCP elicitation method', () => {
    expect(plugin.controls?.elicitation?.({ method: '_x.ai/mcp/elicit', params: { serverName: 'docs', message: 'Pick one', mode: 'form', requestedSchema: { type: 'object' } } })).toEqual({
      mode: 'form',
      message: 'Pick one',
      server: 'docs',
      schema: { type: 'object' },
      url: '',
      title: '',
      description: '',
    })
  })

  it('recognizes its question dialog', () => {
    expect(plugin.controls?.askUserQuestion?.isRequest({ method: '_x.ai/ask_user_question' })).toBe(true)
    expect(plugin.controls?.askUserQuestion?.isRequest({ method: 'session/request_permission' })).toBe(false)
  })

  describe('the end of a turn Grok started', () => {
    it('draws a divider from the stop reason of turn_completed', () => {
      const parent = turnCompleted({ stop_reason: 'end_turn', prompt_id: 'p' })
      expect(plugin.transcript.classify(input(parent, null, AgentProvider.GROK_BUILD))).toEqual({ kind: 'result_divider' })
      expect(plugin.transcript.extractDivider(parent)).toEqual({ label: 'Turn ended' })
    })

    it('states a reason that is not the ordinary end', () => {
      expect(plugin.transcript.extractDivider(turnCompleted({ stop_reason: 'max_tokens' }))?.label).toContain('max_tokens')
      expect(plugin.transcript.extractDivider(turnCompleted({ stop_reason: 'cancelled' }))).toEqual({ label: 'Turn interrupted' })
    })

    it('still reads the prompt response of a turn LeapMux started', () => {
      const parent = { stopReason: 'end_turn' }
      expect(plugin.transcript.classify(input(parent, null, AgentProvider.GROK_BUILD))).toEqual({ kind: 'result_divider' })
      expect(plugin.transcript.extractDivider(parent)).toEqual({ label: 'Turn ended' })
    })
  })
})
