import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import './plugin'

describe('dirac provider', () => {
  it('maps Bypass to native yolo without changing the model or Plan mode', () => {
    const plugin = providerFor(AgentProvider.DIRAC)
    expect(plugin?.controls?.permissionPresets?.bypass).toEqual({ sets: { yolo: 'true' } })
    expect(plugin?.controls?.permissionPresets?.smart).toBeUndefined()
    expect(plugin?.configuration?.planMode).toMatchObject({ groupKey: 'permissionMode', planValue: 'plan', defaultValue: 'act' })
  })
})
