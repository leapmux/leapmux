import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import type { AgentSessionInfo } from '~/stores/agentSession.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { formatAgentSessionIdForDisplay, useAgentInfoCard } from './AgentInfoCard'

// Side-effect imports: register the Claude and Pi plugins so the session-id
// display/copy logic can resolve `sessionIdIsFilePath` through the registry.
import './providers/claude/plugin'
import './providers/pi/plugin'

function InfoCardContent(props: { agent: AgentInfo }) {
  const { infoHoverCardContent } = useAgentInfoCard(props)
  return <div>{infoHoverCardContent()}</div>
}

function agent(provider: AgentProvider, sessionId: string): AgentInfo {
  return {
    agentProvider: provider,
    agentSessionId: sessionId,
  } as AgentInfo
}

describe('formatAgentSessionIdForDisplay', () => {
  it('shortens Pi session file paths to the basename without .jsonl', () => {
    expect(formatAgentSessionIdForDisplay(
      AgentProvider.PI,
      '/Users/me/.pi/agent/sessions/--project--/2026-04-29T10-20-30-000Z_1234.jsonl',
    )).toBe('2026-04-29T10-20-30-000Z_1234')
  })

  it('handles Windows-style Pi session paths', () => {
    expect(formatAgentSessionIdForDisplay(
      AgentProvider.PI,
      'C:\\Users\\me\\.pi\\agent\\sessions\\project\\session-file.jsonl',
    )).toBe('session-file')
  })

  it('keeps non-Pi session IDs unchanged', () => {
    const sessionPath = '/Users/me/.pi/agent/sessions/project/session-file.jsonl'
    expect(formatAgentSessionIdForDisplay(AgentProvider.CLAUDE_CODE, sessionPath)).toBe(sessionPath)
  })
})

describe('agent info card session ID row', () => {
  beforeEach(() => {
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
    })
  })

  it('shows the shortened Pi session file while copying the full path', async () => {
    const fullPath = '/Users/me/.pi/agent/sessions/--project--/2026-04-29T10-20-30-000Z_1234.jsonl'
    render(() => <InfoCardContent agent={agent(AgentProvider.PI, fullPath)} />)

    expect(screen.getByTestId('session-id-value')).toHaveTextContent('2026-04-29T10-20-30-000Z_1234')

    fireEvent.click(screen.getByTestId('session-id-copy'))
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(fullPath)
  })

  it('shows non-Pi session IDs unchanged', () => {
    render(() => <InfoCardContent agent={agent(AgentProvider.CLAUDE_CODE, 'claude-session-123')} />)

    expect(screen.getByTestId('session-id-value')).toHaveTextContent('claude-session-123')
  })
})

describe('agent info card rate-limit rows', () => {
  // Unix seconds in the future, for a deterministically-positive reset countdown.
  const future = (secs: number): number => Math.floor(Date.now() / 1000) + secs

  function InfoCardWithInfo(props: { agent: AgentInfo, agentSessionInfo: AgentSessionInfo }) {
    const { infoHoverCardContent } = useAgentInfoCard(props)
    return <div>{infoHoverCardContent()}</div>
  }

  it('renders a warning tier with label, utilization, and countdown', () => {
    const { container } = render(() => (
      <InfoCardWithInfo
        agent={agent(AgentProvider.CODEX, 's')}
        agentSessionInfo={{
          rateLimits: {
            five_hour: { rateLimitType: 'five_hour', status: 'allowed_warning', utilization: 0.85, resetsAt: future(3600) },
          },
        }}
      />
    ))
    const text = container.textContent ?? ''
    expect(text).toContain('5-Hour Rate Limit')
    expect(text).toContain('Warning')
    expect(text).toContain('85% used')
    expect(text).toContain('resets in')
  })

  it('renders a Claude "rejected" tier as Exceeded without a redundant utilization', () => {
    const { container } = render(() => (
      <InfoCardWithInfo
        agent={agent(AgentProvider.CLAUDE_CODE, 's')}
        agentSessionInfo={{
          rateLimits: {
            seven_day: { rateLimitType: 'seven_day', status: 'rejected', utilization: 1, resetsAt: future(7200) },
          },
        }}
      />
    ))
    const text = container.textContent ?? ''
    expect(text).toContain('7-Day Rate Limit')
    expect(text).toContain('Exceeded')
    expect(text).toContain('resets in')
    expect(text).not.toContain('% used')
  })
})
