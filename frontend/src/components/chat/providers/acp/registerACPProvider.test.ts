import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerACPProvider } from './registerACPProvider'

describe('registerACPProvider', () => {
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
