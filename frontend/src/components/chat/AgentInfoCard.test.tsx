import type { AgentInfo } from '~/generated/proto/leapmux/v1/agent_pb'
import type { AgentSessionInfo } from '~/stores/agentSession.store'
import type { RepoGitView } from '~/stores/repoGit'
import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { formatAgentSessionIdForDisplay, useAgentInfoCard } from './AgentInfoCard'

// Side-effect imports: register the Claude and Pi plugins so the session-id
// display/copy logic can resolve `sessionIdIsFilePath` through the registry.
import './providers/claude/plugin'
import './providers/pi/plugin'

function InfoCardContent(props: { agent?: AgentInfo, agentSessionInfo?: AgentSessionInfo, branchName?: string, gitView?: RepoGitView, homeDir?: string }) {
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

// The Directory and Plan File rows abbreviate the worker's home directory to
// `~` for reading, and copy the absolute path for pasting. Both halves need
// pinning, and the display half especially: tildify returns its input unchanged
// when homeDir is absent, so losing the home directory degrades to a full path
// that still looks perfectly plausible on screen.
describe('agent info card path rows', () => {
  const HOME = '/Users/me'

  function withPaths(fields: Partial<AgentInfo>): AgentInfo {
    return { agentProvider: AgentProvider.CLAUDE_CODE, agentSessionId: 'sid', ...fields } as AgentInfo
  }

  beforeEach(() => {
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
    })
  })

  // The home directory arrives as a PROP, not on the agent. `agentTabToInfo`
  // builds the AgentInfo from a Tab row, which carries none, so `agent.homeDir`
  // is always the empty string in the app -- a test that seeded it there proved
  // the abbreviation and hid the fact that no path on this card ever shortened.
  it('abbreviates the working directory but copies the absolute path', () => {
    const workingDir = `${HOME}/projects/app`
    render(() => <InfoCardContent agent={withPaths({ workingDir })} homeDir={HOME} />)

    expect(screen.getByTestId('info-row-directory')).toHaveTextContent('~/projects/app')
    expect(screen.getByTestId('info-row-directory')).not.toHaveTextContent(HOME)

    fireEvent.click(screen.getByRole('button', { name: 'Copy directory path' }))
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(workingDir)
  })

  // The label states WHOSE directory it is. `WorkingTreeRows` prints a
  // `Directory` row for the working-tree ROOT, and the `[+]` menu puts the two
  // one click apart -- an agent opened in a subdirectory makes them differ.
  it('names the row after the agent working directory, not the checkout root', () => {
    render(() => <InfoCardContent agent={withPaths({ workingDir: `${HOME}/projects/app` })} homeDir={HOME} />)

    expect(screen.getByTestId('info-row-directory')).toHaveTextContent('Working dir')
    expect(screen.queryByText('Directory')).toBeNull()
  })

  it('abbreviates the plan file path but copies the absolute path', () => {
    const planFilePath = `${HOME}/projects/app/PLAN.md`
    render(() => (
      <InfoCardContent
        agent={withPaths({ workingDir: `${HOME}/projects/app` })}
        homeDir={HOME}
        agentSessionInfo={{ planFilePath }}
      />
    ))

    expect(screen.getByTestId('info-row-plan-file')).toHaveTextContent('~/projects/app/PLAN.md')
    expect(screen.getByTestId('info-row-plan-file')).not.toHaveTextContent(HOME)

    fireEvent.click(screen.getByRole('button', { name: 'Copy plan file path' }))
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(planFilePath)
  })

  // Not a repeat of tildify's own cases (paths.test.ts covers the abbreviation
  // itself). This is the wiring: the card must hand the worker's home directory
  // to tildify rather than call it with nothing, and a worker that reported no
  // home directory must still show a usable path. Omitting the prop is exactly
  // the "worker system info not fetched yet" case.
  it('shows the absolute path when the worker reported no home directory', () => {
    const workingDir = `${HOME}/projects/app`
    render(() => <InfoCardContent agent={withPaths({ workingDir })} />)

    expect(screen.getByTestId('info-row-directory')).toHaveTextContent(workingDir)

    fireEvent.click(screen.getByRole('button', { name: 'Copy directory path' }))
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(workingDir)
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

describe('agent info card branch row', () => {
  function withGit(fields: Partial<AgentInfo>): AgentInfo {
    return { agentProvider: AgentProvider.CLAUDE_CODE, agentSessionId: 'sid', ...fields } as AgentInfo
  }

  it('prefers the flat branch name over a stale nested gitStatus branch', () => {
    render(() => (
      <InfoCardContent
        agent={withGit({})}
        branchName="renamed"
        gitView={{ modified: true } as RepoGitView}
      />
    ))

    expect(screen.getByText('renamed')).toBeInTheDocument()
    expect(screen.queryByText(/^main$/)).toBeNull()
    expect(screen.getByText('Modified')).toBeInTheDocument()
  })

  it('hides the branch row when the flat branch is explicitly empty', () => {
    render(() => (
      <InfoCardContent
        agent={withGit({})}
        branchName=""
        gitView={{ modified: true } as RepoGitView}
      />
    ))

    expect(screen.queryByText(/^main$/)).toBeNull()
    expect(screen.getByText('Modified')).toBeInTheDocument()
  })

  it('shows branchName from repoGitView when provided', () => {
    render(() => (
      <InfoCardContent
        agent={withGit({})}
        branchName="main"
      />
    ))

    expect(screen.getByText('main')).toBeInTheDocument()
  })

  // The card is where a user checks which checkout the agent runs in, and a
  // worktree deletes as a whole directory while a main-repo branch does not.
  it('labels the row Worktree and marks it with the worktree glyph', () => {
    const { container } = render(() => (
      <InfoCardContent
        agent={withGit({})}
        branchName="feature"
        gitView={{ isWorktree: true } as RepoGitView}
      />
    ))

    const row = screen.getByTestId('info-row-working-tree')
    expect(within(row).getByText('Worktree')).toBeInTheDocument()
    expect(container.querySelector('[data-testid="worktree-icon"]')).not.toBeNull()
  })

  it('labels the row Branch and marks it with the branch glyph', () => {
    const { container } = render(() => (
      <InfoCardContent
        agent={withGit({})}
        branchName="main"
        gitView={{ isWorktree: false } as RepoGitView}
      />
    ))

    const row = screen.getByTestId('info-row-working-tree')
    expect(within(row).getByText('Branch')).toBeInTheDocument()
    expect(container.querySelector('[data-testid="branch-icon"]')).not.toBeNull()
  })

  // A branch stamped onto a tab before its first status push has no git view.
  // "Branch" is the safe reading: it claims nothing about a directory.
  it('falls back to Branch when the checkout kind is not known yet', () => {
    render(() => <InfoCardContent agent={withGit({})} branchName="main" />)

    expect(within(screen.getByTestId('info-row-working-tree')).getByText('Branch')).toBeInTheDocument()
  })

  it('still copies the branch name from the renamed row', () => {
    render(() => (
      <InfoCardContent
        agent={withGit({})}
        branchName="feature"
        gitView={{ isWorktree: true } as RepoGitView}
      />
    ))

    expect(screen.getByRole('button', { name: 'Copy branch name' })).toBeInTheDocument()
  })
})

describe('agent info card context row', () => {
  const usage: AgentSessionInfo['contextUsage'] = {
    inputTokens: 1000,
    cacheCreationInputTokens: 0,
    cacheReadInputTokens: 0,
  }

  it('renders the context row from the reported usage', () => {
    render(() => (
      <InfoCardContent
        agent={agent(AgentProvider.CLAUDE_CODE, 'sid')}
        agentSessionInfo={{ contextUsage: usage }}
      />
    ))
    expect(screen.getByText(/Context/)).toBeInTheDocument()
  })

  it('renders nothing for the context row when no usage is reported', () => {
    render(() => (
      <InfoCardContent agent={agent(AgentProvider.CLAUDE_CODE, 'sid')} agentSessionInfo={{}} />
    ))
    expect(screen.queryByText(/Context/)).toBeNull()
  })

  // Regression: the row's body used to deref `agentSessionInfo!.contextUsage!`
  // behind a plain <Show>, re-reading the very value the guard had already
  // resolved. `agentSessionInfo` is re-resolved per read in the shell
  // (`agentSessionStore.getInfo(focusedAgentId())`), so a focus switch can hand
  // the guard one agent's info and the body the next agent's -- and the body
  // then read `usage.contextWindow` off undefined and tripped the error
  // boundary. The body must consume the value the guard admitted, never re-read
  // it. The getter below makes that re-read observable: it answers once.
  it('does not throw when the guarded usage is gone by the time the body reads it', () => {
    let reads = 0
    const sessionInfo: AgentSessionInfo = {
      get contextUsage() {
        reads += 1
        return reads === 1 ? usage : undefined
      },
    }
    expect(() => render(() => (
      <InfoCardContent agent={agent(AgentProvider.CLAUDE_CODE, 'sid')} agentSessionInfo={sessionInfo} />
    ))).not.toThrow()
    // The guard consumed one read; a body that re-read would have taken more.
    expect(reads).toBe(1)
  })
})
