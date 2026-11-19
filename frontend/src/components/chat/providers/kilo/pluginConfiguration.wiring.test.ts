import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { openCodePermissionPresets } from '../openCodePermissionPresets'
import { kiloControlResponseSummary } from './controlResponse'
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

  // The Worker reads and answers every control request, so the registration states
  // only Kilo's display of a saved answer and the family presets.
  it('passes the display of a saved answer and the family presets', () => {
    registration.mockClear()
    registerKiloProtocolProvider({ provider: AgentProvider.KILO, defaultPrimaryAgent: 'code', refusals: KILO_REFUSED_TOOL_ERRORS })
    const options = registration.mock.calls[0]?.[0]
    expect(options?.controlResponseDisplay).toBe(kiloControlResponseSummary)
    expect(options?.permissionPresets).toBe(openCodePermissionPresets)
  })
})
