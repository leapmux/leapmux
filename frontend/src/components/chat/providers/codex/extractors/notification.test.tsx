import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'

// Importing the registry side-effect-registers the Codex plugin so the thread
// renderer can dispatch into `notificationThreadEntry`.
await import('../plugin')
const { renderThreadText } = await import('~/test-support/messageRenderProbes')

const renderText = (messages: unknown[]): string => renderThreadText(messages, AgentProvider.CODEX)

describe('codex single MCP startup status', () => {
  // A standalone Codex notification renders through the same
  // renderNotificationThread path as a consolidated one (a one-element thread).
  it('does not render starting status', () => {
    expect(renderText([{
      method: 'mcpServer/startupStatus/updated',
      params: { name: 'codex_apps', status: 'starting', error: null },
    }])).toBe('')
  })

  it('does not render ready status', () => {
    expect(renderText([{
      method: 'mcpServer/startupStatus/updated',
      params: { name: 'codex_apps', status: 'ready', error: null },
    }])).toBe('')
  })

  it('renders failed status with error', () => {
    expect(renderText([{
      method: 'mcpServer/startupStatus/updated',
      params: { name: 'codex_apps', status: 'failed', error: 'boom' },
    }])).toBe('MCP server failed to start: codex_apps (boom)')
  })

  it('does not render cancelled status', () => {
    expect(renderText([{
      method: 'mcpServer/startupStatus/updated',
      params: { name: 'codex_apps', status: 'cancelled', error: null },
    }])).toBe('')
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

  it('does not render a name-less ready status', () => {
    expect(renderText([{
      method: 'mcpServer/startupStatus/updated',
      params: { status: 'ready', error: null },
    }])).toBe('')
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

  // These values are JavaScript prototype property names. Treat each as an unknown
  // wire state, never as a property lookup that supplies function source text.
  it.each(['toString', 'constructor', 'valueOf', 'hasOwnProperty'])('falls back for a state called %s', (state) => {
    expect(renderText([{
      method: 'mcpServer/startupStatus/updated',
      params: { name: 'codex_apps', status: state, error: null },
    }])).toBe(`MCP server status update (${state}): codex_apps`)
  })

  it('renders a Claude-shaped notification Codex also emits (previously raw JSON)', () => {
    // Codex classifies context_cleared as a notification but had no standalone
    // renderer for it, so it used to fall through to the raw-JSON bubble. Routed
    // through the shared switch, a standalone Codex notification now renders.
    expect(renderText([{ type: 'context_cleared' }])).toBe('Context cleared')
  })
})

describe('codex rate-limit reached-type notifications', () => {
  it('surfaces credit depletion even when no window is over threshold', () => {
    // Windows are well under threshold, so without the reached-type this would
    // render nothing -- the authoritative signal keeps the block visible.
    expect(renderText([{
      method: 'account/rateLimits/updated',
      params: {
        rateLimits: {
          rateLimitReachedType: 'workspace_owner_credits_depleted',
          primary: { usedPercent: 20, windowDurationMins: 300 },
        },
      },
    }])).toBe('Out of credits')
  })

  it('does not double-report when a tier line already conveys the throttle', () => {
    const text = renderText([{
      method: 'account/rateLimits/updated',
      params: {
        rateLimits: {
          rateLimitReachedType: 'rate_limit_reached',
          primary: { usedPercent: 100, windowDurationMins: 300, resetsAt: 4102444800 },
        },
      },
    }])
    expect(text).toContain('5-hour rate limit')
    expect(text).not.toContain('Rate limit reached')
  })
})

describe('codex MCP OAuth notifications', () => {
  it('does not render a successful login', () => {
    expect(renderText([{
      method: 'mcpServer/oauthLogin/completed',
      params: { name: 'docs', success: true },
    }])).toBe('')
  })

  it('renders a failed login', () => {
    expect(renderText([{
      method: 'mcpServer/oauthLogin/completed',
      params: { name: 'docs', success: false, error: 'authorization failed' },
    }])).toBe('MCP OAuth login failed for docs: authorization failed')
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
    expect(text).not.toContain('codex_apps')
    expect(text).toContain('MCP server failed to start: other (boom)')
  })

  it('groups only failed servers', () => {
    const text = renderText([
      { method: 'mcpServer/startupStatus/updated', params: { name: 'server_a', status: 'starting', error: null } },
      { method: 'mcpServer/startupStatus/updated', params: { name: 'server_b', status: 'starting', error: null } },
      { method: 'mcpServer/startupStatus/updated', params: { name: 'server_c', status: 'ready', error: null } },
      { method: 'mcpServer/startupStatus/updated', params: { name: 'server_d', status: 'ready', error: null } },
      { method: 'mcpServer/startupStatus/updated', params: { name: 'server_e', status: 'failed', error: 'boom' } },
      { method: 'mcpServer/startupStatus/updated', params: { name: 'server_f', status: 'failed', error: 'bad gateway' } },
    ])
    expect(text).not.toContain('server_a')
    expect(text).not.toContain('server_c')
    expect(text).toContain('MCP server failed to start: server_e (boom), server_f (bad gateway)')
  })

  it('keeps visible notifications when non-failed startup states disappear', () => {
    const text = renderText([
      { method: 'mcpServer/startupStatus/updated', params: { name: 'server_a', status: 'starting', error: null } },
      { method: 'mcpServer/startupStatus/updated', params: { name: 'server_b', status: 'starting', error: null } },
      { type: 'context_cleared' },
      { method: 'mcpServer/startupStatus/updated', params: { name: 'server_c', status: 'ready', error: null } },
      { method: 'mcpServer/startupStatus/updated', params: { name: 'server_d', status: 'ready', error: null } },
    ])
    expect(text).toBe('Context cleared')
  })
})
