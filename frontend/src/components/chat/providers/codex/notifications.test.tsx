import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'

// Importing the registry side-effect-registers the Codex plugin so the thread
// renderer can dispatch into `notificationThreadEntry`.
await import('./plugin')
const { renderThreadText } = await import('../../messageRenderTestUtils')

const renderText = (messages: unknown[]): string => renderThreadText(messages, AgentProvider.CODEX)

describe('codex single MCP startup status', () => {
  // A standalone Codex notification renders through the same
  // renderNotificationThread path as a consolidated one (a one-element thread).
  it('renders starting status', () => {
    expect(renderText([{
      method: 'mcpServer/startupStatus/updated',
      params: { name: 'codex_apps', status: 'starting', error: null },
    }])).toBe('Starting MCP server: codex_apps')
  })

  it('renders ready status', () => {
    expect(renderText([{
      method: 'mcpServer/startupStatus/updated',
      params: { name: 'codex_apps', status: 'ready', error: null },
    }])).toBe('MCP server ready: codex_apps')
  })

  it('renders failed status with error', () => {
    expect(renderText([{
      method: 'mcpServer/startupStatus/updated',
      params: { name: 'codex_apps', status: 'failed', error: 'boom' },
    }])).toBe('MCP server failed to start: codex_apps (boom)')
  })

  it('renders cancelled status', () => {
    expect(renderText([{
      method: 'mcpServer/startupStatus/updated',
      params: { name: 'codex_apps', status: 'cancelled', error: null },
    }])).toBe('MCP server startup cancelled: codex_apps')
  })

  it('supports nested upstream-style status payloads', () => {
    expect(renderText([{
      method: 'mcpServer/startupStatus/updated',
      params: { name: 'codex_apps', status: { state: 'failed', error: 'timeout' } },
    }])).toBe('MCP server failed to start: codex_apps (timeout)')
  })

  it('falls back for unknown statuses (state carried in the consolidated prefix)', () => {
    // Single notifications now use the consolidated group form, so an unknown
    // state sits in the prefix before the colon rather than after the name.
    expect(renderText([{
      method: 'mcpServer/startupStatus/updated',
      params: { name: 'codex_apps', status: 'warming', error: 'still booting' },
    }])).toBe('MCP server status update (warming): codex_apps (still booting)')
  })

  it('renders a name-less startup as the bare prefix, with no "unknown" placeholder', () => {
    // A startup notification with no server name has nothing to group under the
    // prefix, so it renders the prefix alone rather than "<prefix>: unknown".
    expect(renderText([{
      method: 'mcpServer/startupStatus/updated',
      params: { status: 'ready', error: null },
    }])).toBe('MCP server ready')
  })

  it('renders a name-less failed startup with its error suffix and no placeholder', () => {
    expect(renderText([{
      method: 'mcpServer/startupStatus/updated',
      params: { status: 'failed', error: 'boom' },
    }])).toBe('MCP server failed to start (boom)')
  })

  it('renders a name-less unknown-state startup as the prefix with the state', () => {
    expect(renderText([{
      method: 'mcpServer/startupStatus/updated',
      params: { status: 'warming', error: 'still booting' },
    }])).toBe('MCP server status update (warming) (still booting)')
  })

  it('renders a Claude-shaped notification Codex also emits (previously raw JSON)', () => {
    // Codex classifies context_cleared as a notification but had no standalone
    // renderer for it, so it used to fall through to the raw-JSON bubble. Routed
    // through the shared switch, a standalone Codex notification now renders.
    expect(renderText([{ type: 'context_cleared' }])).toBe('Context cleared')
  })
})

describe('renderNotificationThread (Codex provider): MCP startup grouping', () => {
  it('does not render skills or remote-control metadata entries', () => {
    const text = renderText([
      { method: 'skills/changed', params: {} },
      { method: 'remoteControl/status/changed', params: { status: 'disabled', environmentId: null } },
    ])
    expect(text).toBe('')
  })

  it('ignores skills and remote-control metadata while rendering visible entries', () => {
    const text = renderText([
      { method: 'skills/changed', params: {} },
      { type: 'context_cleared' },
      { method: 'remoteControl/status/changed', params: { status: 'disabled', environmentId: null } },
    ])
    expect(text).toBe('Context cleared')
  })

  it('renders consolidated startup status entries', () => {
    const text = renderText([
      { method: 'mcpServer/startupStatus/updated', params: { name: 'codex_apps', status: 'ready', error: null } },
      { method: 'mcpServer/startupStatus/updated', params: { name: 'other', status: 'failed', error: 'boom' } },
    ])
    expect(text).toContain('MCP server ready: codex_apps')
    expect(text).toContain('MCP server failed to start: other (boom)')
  })

  it('groups multiple servers by startup state', () => {
    const text = renderText([
      { method: 'mcpServer/startupStatus/updated', params: { name: 'server_a', status: 'starting', error: null } },
      { method: 'mcpServer/startupStatus/updated', params: { name: 'server_b', status: 'starting', error: null } },
      { method: 'mcpServer/startupStatus/updated', params: { name: 'server_c', status: 'ready', error: null } },
      { method: 'mcpServer/startupStatus/updated', params: { name: 'server_d', status: 'ready', error: null } },
      { method: 'mcpServer/startupStatus/updated', params: { name: 'server_e', status: 'failed', error: 'boom' } },
      { method: 'mcpServer/startupStatus/updated', params: { name: 'server_f', status: 'failed', error: 'bad gateway' } },
    ])
    expect(text).toContain('Starting MCP server: server_a, server_b')
    expect(text).toContain('MCP server ready: server_c, server_d')
    expect(text).toContain('MCP server failed to start: server_e (boom), server_f (bad gateway)')
  })

  it('preserves ordering around non-startup notifications while grouping startup states', () => {
    const text = renderText([
      { method: 'mcpServer/startupStatus/updated', params: { name: 'server_a', status: 'starting', error: null } },
      { method: 'mcpServer/startupStatus/updated', params: { name: 'server_b', status: 'starting', error: null } },
      { type: 'context_cleared' },
      { method: 'mcpServer/startupStatus/updated', params: { name: 'server_c', status: 'ready', error: null } },
      { method: 'mcpServer/startupStatus/updated', params: { name: 'server_d', status: 'ready', error: null } },
    ])
    const startingIdx = text.indexOf('Starting MCP server: server_a, server_b')
    const clearedIdx = text.indexOf('Context cleared')
    const readyIdx = text.indexOf('MCP server ready: server_c, server_d')
    expect(startingIdx).toBeGreaterThanOrEqual(0)
    expect(clearedIdx).toBeGreaterThan(startingIdx)
    expect(readyIdx).toBeGreaterThan(clearedIdx)
  })
})
