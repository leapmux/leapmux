import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { openCodePermissionPresets } from '../openCodePermissionPresets'
import { registerKiloProtocolProvider } from './pluginConfiguration'
import { KILO_REFUSED_TOOL_ERRORS } from './protocol'

const registration = vi.hoisted(() => vi.fn())
vi.mock('../acp/registerACPProvider', () => ({ registerACPProvider: registration }))

describe('registerKiloProtocolProvider', () => {
  it('forwards the provider path reader without parsing or wrapping it', () => {
    registration.mockClear()
    const outputFilePaths = vi.fn(() => ['/native/reported.log'])
    const options = {
      provider: AgentProvider.KILO,
      defaultPrimaryAgent: 'code',
      refusals: KILO_REFUSED_TOOL_ERRORS,
      outputFilePaths,
    }

    registerKiloProtocolProvider(options)

    expect(registration).toHaveBeenCalledOnce()
    expect(registration.mock.calls[0]?.[0]?.outputFilePaths).toBe(outputFilePaths)
    expect(outputFilePaths).not.toHaveBeenCalled()
    expect(registration.mock.calls[0]?.[0]?.settingsConfig).toEqual({ kind: 'optionGroup', optionGroupKey: 'primaryAgent', defaultValue: 'code' })
  })

  it('omits the path reader when the provider supplies none', () => {
    registration.mockClear()
    registerKiloProtocolProvider({ provider: AgentProvider.KILO, defaultPrimaryAgent: 'code', refusals: KILO_REFUSED_TOOL_ERRORS })
    expect(registration.mock.calls[0]?.[0]).not.toHaveProperty('outputFilePaths')
  })

  // The Worker reads and answers every control request, and records the decision that
  // words a saved answer, so the registration states only the family presets.
  it('passes the family presets', () => {
    registration.mockClear()
    registerKiloProtocolProvider({ provider: AgentProvider.KILO, defaultPrimaryAgent: 'code', refusals: KILO_REFUSED_TOOL_ERRORS })
    expect(registration.mock.calls[0]?.[0]?.permissionPresets).toBe(openCodePermissionPresets)
  })
})
