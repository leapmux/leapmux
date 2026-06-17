import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { describeACPStubBasics } from './stubBasics'

import './reasonix'

vi.mock('~/api/workerRpc', () => ({
  updateAgentSettings: vi.fn(),
}))

describe('reasonix provider', () => {
  const plugin = providerFor(AgentProvider.REASONIX)!

  // Reasonix is text-only -- the one attachment-capability variant among the ACP stubs.
  describeACPStubBasics(plugin, { text: true, image: false, pdf: false, binary: false })

  it('has no runtime mode: no plan mode, permission mode, or bypass', () => {
    expect(plugin.planMode).toBeUndefined()
    expect(plugin.bypassPermissionMode).toBeUndefined()
    // modelOnly: no mode axis, so the trigger renders no third (mode) segment.
    expect(plugin.triggerModeGroupKey).toBeUndefined()
  })
})
