import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { registerACPProvider } from './registerACPProvider'

describe('registerACPProvider', () => {
  // settingsConfig and defaultPermissionMode are the two ways to declare a provider's
  // settings shape; supplying NEITHER is a registration-time misconfiguration that must
  // fail loudly (before registerProvider) rather than register a provider with no axis.
  it('rejects a registration with neither settingsConfig nor defaultPermissionMode', () => {
    expect(() => registerACPProvider({
      provider: AgentProvider.REASONIX,
      controlActionsFor: () => () => null,
    })).toThrow(/settingsConfig or defaultPermissionMode/)
  })

  // The options a provider with its own methods passes: its elicitation reader, its
  // field for a rejection reason, the dialog flag, and its own turn end. Each must
  // reach the plugin it registers, and the shared defaults must stay for the rest.
  it('carries the optional hooks into the plugin it registers', () => {
    const elicitation = (payload: Record<string, unknown>) => payload.method === 'vendor/elicit' ? { mode: 'form', message: 'm' } : undefined
    registerACPProvider({
      provider: AgentProvider.GROK_BUILD,
      defaultPermissionMode: 'default',
      elicitation,
      permissionRejectReason: (result, reason) => ({ ...result, reason }),
      preservesSelectionNotes: true,
      agentTurnEnd: parent => parent.method === 'vendor/end' ? 'end_turn' : undefined,
    })
    const plugin = providerFor(AgentProvider.GROK_BUILD)!
    expect(plugin.controls?.elicitation?.({ method: 'vendor/elicit' })).toEqual({ mode: 'form', message: 'm' })
    expect(plugin.controls?.preservesSelectionNotes).toBe(true)
    const permission = { params: { toolCall: { toolCallId: 'c' }, options: [{ optionId: 'no', kind: 'reject_once' }] } }
    expect(plugin.controls?.buildControlResponse?.(permission, 'why', 'r')).toEqual({ jsonrpc: '2.0', id: 'r', result: { outcome: { outcome: 'selected', optionId: 'no' }, reason: 'why' } })
    expect(plugin.controls?.controlFeedbackAsFollowUpMessage?.(permission)).toBe(false)
    expect(plugin.transcript.extractDivider({ method: 'vendor/end' })).toEqual({ label: 'Turn ended' })
  })

  // The saved answer of a form reads the request through the same reader that drew
  // the form. A display that read the protocol's own method called the provider's
  // form an ordinary permission, and the row lost what the reader chose.
  it('reads a saved form answer through the provider\'s own elicitation reader', () => {
    const elicitation = (payload: Record<string, unknown>) => payload.method === 'vendor/elicit' ? { mode: 'form' as const, message: 'm' } : undefined
    registerACPProvider({ provider: AgentProvider.KIRO, defaultPermissionMode: 'default', elicitation })
    const plugin = providerFor(AgentProvider.KIRO)!
    const saved = (response: Record<string, unknown>) => plugin.controls?.controlResponseDisplay?.({ requestId: 'r', claimToken: 'c', request: { method: 'vendor/elicit' }, response })
    expect(saved({ result: { action: 'decline' } })).toEqual({ kind: 'label', text: 'Rejected' })
    expect(saved({ result: { action: 'accept' } })).toEqual({ kind: 'label', text: 'Approved' })
    const permission = { requestId: 'r', claimToken: 'c', request: { method: 'session/request_permission', params: { options: [{ optionId: 'no', name: 'No', kind: 'reject_once' }] } }, response: { result: { outcome: { outcome: 'selected', optionId: 'no' } } } }
    expect(plugin.controls?.controlResponseDisplay?.(permission), 'every other request keeps the permission display').toEqual({ kind: 'label', text: 'No' })
  })

  it('keeps the shared defaults when a provider passes none', () => {
    registerACPProvider({ provider: AgentProvider.REASONIX, defaultPermissionMode: 'normal' })
    const plugin = providerFor(AgentProvider.REASONIX)!
    expect(plugin.controls?.preservesSelectionNotes).toBeUndefined()
    expect(plugin.controls?.elicitation?.({ method: 'elicitation/create', params: { message: 'Hi' } })?.message).toBe('Hi')
    const permission = { params: { toolCall: { toolCallId: 'c' }, options: [{ optionId: 'no', kind: 'reject_once' }] } }
    expect(plugin.controls?.controlFeedbackAsFollowUpMessage?.(permission)).toBe(true)
  })
})
