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
  describeACPProviderBasics(AgentProvider.REASONIX, { text: true, image: false, pdf: false, binary: false })

  it('uses the advertised mode and tool approval controls', () => {
    expect(plugin?.configuration?.planMode).toBeDefined()
    expect(plugin?.configuration?.triggerModeGroupKey).toBe('permissionMode')
    expect(plugin?.controls?.permissionPresets?.bypass?.sets).toEqual({ tool_approval: 'yolo' })
  })

  it('reads the native MCP form request', () => {
    const schema = { type: 'object', properties: { count: { type: 'integer', minimum: 0 } } }
    expect(plugin?.controls?.elicitation?.({
      method: '_reasonix.io/mcp/request_interaction',
      params: { sessionId: 'session-1', promptId: 'prompt-1', server: 'form_probe', mode: 'form', message: 'Choose a count.', requestedSchema: schema },
    })).toEqual({ mode: 'form', message: 'Choose a count.', server: 'form_probe', schema, url: '', title: '', description: '' })
  })
})
