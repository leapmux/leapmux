import { describe, expect, it } from 'vitest'
import { ANTIGRAVITY_MODE } from '~/generated/contracts/antigravity-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import './plugin'

const plugin = providerFor(AgentProvider.ANTIGRAVITY_CLI)!

describe('antigravity plugin metadata', () => {
  it('registers every capability', () => {
    expect(plugin.transcript.classify).toBeDefined()
    expect(plugin.transcript.extractRow).toBeDefined()
    expect(plugin.transcript.extractDivider).toBeDefined()
    // The Worker reads each request, builds each answer, and records the decision that
    // words a saved answer, so the browser keeps only the presets.
    expect(Object.keys(plugin.controls ?? {})).toEqual(['permissionPresets'])
    expect(plugin.configuration?.attachments).toEqual({
      text: true,
      image: true,
      pdf: true,
      audio: true,
      binary: true,
    })
    expect(plugin.configuration?.supportsSubagentSend).toBe(true)
  })

  it('maps only the bypass preset', () => {
    expect(plugin.controls?.permissionPresets).toEqual({
      bypass: {
        sets: { permissionMode: ANTIGRAVITY_MODE.Bypass },
      },
    })
    expect(plugin.controls?.permissionPresets?.smart).toBeUndefined()
  })
})
