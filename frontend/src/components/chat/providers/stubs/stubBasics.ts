import type { AttachmentCapabilities, Provider } from '../registry'
import { expect, it } from 'vitest'
import { input } from '../testUtils'

/**
 * Asserts the behaviours every minimal ACP "stub" provider (Copilot, Goose, Reasonix, Cursor, Kilo)
 * shares: its attachment capabilities, agent_message_chunk -> assistant_text classification,
 * config_option_update hiding, and the ACP session/cancel interrupt request (wired unconditionally
 * by registerACPProvider). Call it inside the provider's own describe() block and pass the
 * provider's expected attachment capabilities -- the one case that genuinely differs (Reasonix is
 * text-only). A provider with a richer variant of one of these keeps that EXTRA case inline
 * alongside the helper (Kilo also asserts a populated config_option_update is hidden).
 */
export function describeACPStubBasics(plugin: Provider, attachments: AttachmentCapabilities): void {
  it('exposes attachment capabilities', () => {
    expect(plugin.attachments).toEqual(attachments)
  })

  it('classifies agent_message_chunk as assistant_text', () => {
    const parent = {
      sessionUpdate: 'agent_message_chunk',
      content: { type: 'text', text: 'Hello' },
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'assistant_text' })
  })

  it('hides config_option_update', () => {
    const parent = {
      sessionUpdate: 'config_option_update',
      configOptions: [],
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
  })

  it('builds an ACP cancel request for interrupt', () => {
    expect(plugin.buildInterruptContent?.('session-1')).toBe(JSON.stringify({
      jsonrpc: '2.0',
      method: 'session/cancel',
      params: { sessionId: 'session-1' },
    }))
  })
}
