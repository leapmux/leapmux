import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { registerOpenCodeProtocolProvider } from './registerOpenCodeProtocolProvider'

const registration = vi.hoisted(() => vi.fn())
vi.mock('./acp/registerACPProvider', () => ({ registerACPProvider: registration }))

describe('registerOpenCodeProtocolProvider', () => {
  it('forwards the provider path reader without parsing or wrapping it', () => {
    registration.mockClear()
    const outputFilePaths = vi.fn(() => ['/native/reported.log'])
    const options = { provider: AgentProvider.OPENCODE, defaultPrimaryAgent: 'build', outputFilePaths }

    registerOpenCodeProtocolProvider(options)

    expect(registration).toHaveBeenCalledOnce()
    expect(registration.mock.calls[0]?.[0]?.outputFilePaths).toBe(outputFilePaths)
    expect(outputFilePaths).not.toHaveBeenCalled()
    expect(registration.mock.calls[0]?.[0]?.settingsConfig).toEqual({ kind: 'optionGroup', optionGroupKey: 'primaryAgent', defaultValue: 'build' })
  })

  it('omits the path reader when the provider supplies none', () => {
    registration.mockClear()
    registerOpenCodeProtocolProvider({ provider: AgentProvider.KILO, defaultPrimaryAgent: 'code' })
    expect(registration.mock.calls[0]?.[0]).not.toHaveProperty('outputFilePaths')
  })
})
