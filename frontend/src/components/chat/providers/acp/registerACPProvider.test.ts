import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { registerACPProvider } from './registerACPProvider'

describe('registerACPProvider', () => {
  // planModeFromConfig has no mode to toggle for a modelOnly provider, so passing
  // planValue is a registration-time misconfiguration that must fail loudly rather
  // than silently wire up a broken plan toggle. The throw fires before
  // registerProvider, so no provider is registered.
  it('rejects planValue for a modelOnly provider', () => {
    expect(() => registerACPProvider({
      provider: AgentProvider.REASONIX,
      settingsConfig: { kind: 'modelOnly' },
      ControlContent: () => null,
      ControlActions: () => null,
      planValue: 'plan',
    })).toThrow(/modelOnly/)
  })

  // settingsConfig and defaultPermissionMode are the two ways to declare a provider's
  // settings shape; supplying NEITHER is a registration-time misconfiguration that must
  // fail loudly (before registerProvider) rather than register a provider with no axis.
  it('rejects a registration with neither settingsConfig nor defaultPermissionMode', () => {
    expect(() => registerACPProvider({
      provider: AgentProvider.REASONIX,
      ControlContent: () => null,
      ControlActions: () => null,
    })).toThrow(/settingsConfig or defaultPermissionMode/)
  })
})
