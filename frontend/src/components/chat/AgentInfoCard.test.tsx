import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { formatAgentSessionIdForDisplay, useAgentInfoCard } from './AgentInfoCard'

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
