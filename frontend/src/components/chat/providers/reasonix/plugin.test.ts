import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { describeACPProviderBasics } from '../acp/testUtils'
import { providerFor } from '../registry'

import './plugin'

vi.mock('~/api/workerRpc', () => ({
  updateAgentSettings: vi.fn(),
}))

describe('reasonix provider', () => {
  const plugin = providerFor(AgentProvider.REASONIX)!

  // Reasonix is text-only -- the one attachment-capability variant among the ACP stubs.
  describeACPProviderBasics(plugin, { text: true, image: false, pdf: false, binary: false })

  it('uses the advertised mode and tool approval controls', () => {
    expect(plugin.planMode).toBeDefined()
    expect(plugin.triggerModeGroupKey).toBe('permissionMode')
    expect(plugin.permissionPresets?.bypass?.sets).toEqual({ tool_approval: 'yolo' })
  })
})
