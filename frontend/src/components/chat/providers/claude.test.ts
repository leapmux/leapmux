import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { getProviderPlugin } from './registry'

// Side-effect import to register the Claude plugin.
import './claude'

describe('claude classify', () => {
  const plugin = getProviderPlugin(AgentProvider.CLAUDE_CODE)!

  it('exposes attachment capabilities', () => {
    expect(plugin.attachments).toEqual({
      text: true,
      image: true,
      pdf: true,
      binary: false,
    })
  })

  it('classifies result divider', () => {
    const parent = {
      type: 'result',
      subtype: 'success',
      result: 'Done',
      duration_ms: 1234,
      num_turns: 1,
      stop_reason: 'end_turn',
    }
    expect(plugin.classify(parent, null)).toEqual({ kind: 'result_divider' })
  })

  it('classifies error result divider', () => {
    const parent = {
      type: 'result',
      is_error: true,
      errors: ['something went wrong'],
    }
    expect(plugin.classify(parent, null)).toEqual({ kind: 'result_divider' })
  })
})
