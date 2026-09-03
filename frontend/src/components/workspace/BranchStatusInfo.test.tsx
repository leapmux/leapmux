import type { AffectedTabs, BranchSnapshot } from './BranchStatusInfo'
import type { BranchGitState } from '~/generated/proto/leapmux/v1/git_pb'
import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { BranchStatusInfo, hasPushableWork } from './BranchStatusInfo'

function gitState(overrides: Partial<BranchGitState> = {}): BranchGitState {
  return {
    $typeName: 'leapmux.v1.BranchGitState',
    diffAdded: 0,
    diffDeleted: 0,
    diffUntracked: 0,
    hasUncommittedChanges: false,
    unpushedCommitCount: 0,
    upstreamExists: true,
    remoteBranchMissing: false,
    originExists: true,
    canPush: true,
    ...overrides,
  } as BranchGitState
}

function branch(overrides: Partial<BranchSnapshot> = {}): BranchSnapshot {
  return {
    isWorktree: false,
    branchName: 'feature',
    directory: '/Users/dev/repos/leapmux',
    homeDir: '/Users/dev',
    gitState: gitState(),
    ...overrides,
  }
}

function affectedTabs(overrides: Partial<AffectedTabs> = {}): AffectedTabs {
  return {
    agents: 0,
    terminals: 0,
    files: 0,
    images: 0,
    willStop: true,
    ...overrides,
  }
}

describe('branchStatusInfo', () => {
  it('names the main working tree a branch and gives its directory', () => {
    render(() => <BranchStatusInfo branch={branch()} affectedTabs={affectedTabs()} />)

    expect(screen.getByText('Branch')).toBeInTheDocument()
    expect(screen.queryByText('Worktree branch')).toBeNull()
    // The directory shows for a plain branch too. Without it a user with two
    // clones of the same repo cannot tell which one the dialog acts on.
    expect(screen.getByTestId('working-tree-directory').textContent).toBe('~/repos/leapmux')
  })

  it('names a linked worktree and gives its directory', () => {
    render(() => (
      <BranchStatusInfo
        branch={branch({ isWorktree: true, directory: '/Users/dev/repos/leapmux-worktrees/wt' })}
        affectedTabs={affectedTabs()}
      />
    ))

    // "Worktree branch", not "Worktree": the value beside it is the branch
    // name, and a bare kind read as though it were the directory.
    expect(screen.getByText('Worktree branch')).toBeInTheDocument()
    expect(screen.queryByText('Branch')).toBeNull()
    expect(screen.getByTestId('working-tree-directory').textContent)
      .toBe('~/repos/leapmux-worktrees/wt')
  })

  // The path used to render raw here while every other surface tilde-compressed
  // it, so the same directory read two ways in two places.
  it('leaves the directory absolute when the worker home dir is unknown', () => {
    render(() => (
      <BranchStatusInfo
        branch={branch({ homeDir: undefined })}
        affectedTabs={affectedTabs()}
      />
    ))

    expect(screen.getByTestId('working-tree-directory').textContent)
      .toBe('/Users/dev/repos/leapmux')
  })

  it('shows clean message when nothing to lose', () => {
    render(() => <BranchStatusInfo branch={branch()} affectedTabs={affectedTabs()} />)
    expect(screen.getByText(/No uncommitted changes or unpushed commits/)).toBeInTheDocument()
  })

  it('shows unpushed commit count', () => {
    render(() => (
      <BranchStatusInfo
        branch={branch({ gitState: gitState({ unpushedCommitCount: 3 }) })}
        affectedTabs={affectedTabs()}
      />
    ))
    expect(screen.getByText(/3 commits/)).toBeInTheDocument()
    expect(screen.getByText(/not pushed/)).toBeInTheDocument()
  })

  it('shows the "not pushed to remote" line when remoteBranchMissing is true', () => {
    render(() => (
      <BranchStatusInfo
        branch={branch({ gitState: gitState({ remoteBranchMissing: true, upstreamExists: false }) })}
        affectedTabs={affectedTabs()}
      />
    ))
    expect(screen.getByText('Branch not pushed to remote.')).toBeInTheDocument()
  })

  it('shows the "not pushed to remote" line when upstream is missing but canPush is true', () => {
    render(() => (
      <BranchStatusInfo
        branch={branch({ gitState: gitState({ upstreamExists: false, canPush: true }) })}
        affectedTabs={affectedTabs()}
      />
    ))
    expect(screen.getByText('Branch not pushed to remote.')).toBeInTheDocument()
  })

  it('hides the "not pushed to remote" line when upstream is missing and canPush is false', () => {
    render(() => (
      <BranchStatusInfo
        branch={branch({ gitState: gitState({ upstreamExists: false, canPush: false }) })}
        affectedTabs={affectedTabs()}
      />
    ))
    expect(screen.queryByText('Branch not pushed to remote.')).toBeNull()
  })

  it('renders the diff badge when there are uncommitted changes', () => {
    render(() => (
      <BranchStatusInfo
        branch={branch({
          gitState: gitState({
            hasUncommittedChanges: true,
            diffAdded: 4,
            diffDeleted: 2,
            diffUntracked: 1,
          }),
        })}
        affectedTabs={affectedTabs()}
      />
    ))
    expect(screen.getByText(/Uncommitted changes:/)).toBeInTheDocument()
  })

  it('hides the clean message once there are unpushed commits', () => {
    render(() => (
      <BranchStatusInfo
        branch={branch({ gitState: gitState({ unpushedCommitCount: 1 }) })}
        affectedTabs={affectedTabs()}
      />
    ))
    expect(screen.queryByText(/No uncommitted changes or unpushed commits/)).toBeNull()
  })

  it('formats affected tabs with both kinds present', () => {
    render(() => (
      <BranchStatusInfo
        branch={branch()}
        affectedTabs={affectedTabs({ agents: 2, terminals: 1 })}
      />
    ))
    expect(screen.getByText('2 agents and 1 terminal will be stopped.')).toBeInTheDocument()
  })

  it('omits a side when its count is zero', () => {
    render(() => (
      <BranchStatusInfo
        branch={branch()}
        affectedTabs={affectedTabs({ agents: 0, terminals: 1 })}
      />
    ))
    expect(screen.getByText('1 terminal will be stopped.')).toBeInTheDocument()
  })

  it('uses "will keep running" copy when affectedTabs.willStop is false', () => {
    render(() => (
      <BranchStatusInfo
        branch={branch()}
        affectedTabs={affectedTabs({ agents: 1, terminals: 0, willStop: false })}
      />
    ))
    expect(screen.getByText('1 agent will keep running.')).toBeInTheDocument()
  })

  it('hides the affected-tabs line when all counts are zero', () => {
    render(() => <BranchStatusInfo branch={branch()} affectedTabs={affectedTabs()} />)
    expect(screen.queryByText(/will be stopped/)).toBeNull()
    expect(screen.queryByText(/will keep running/)).toBeNull()
    expect(screen.queryByText(/will be closed/)).toBeNull()
  })

  it('renders the FILE-only affected-tabs line without a "will be stopped" verb', () => {
    // FILE tabs hold no running process, so the dialog must say
    // "<n file(s)> will be closed" instead of misleadingly stamping
    // them with the stopped/running verb meant for agents/terminals.
    render(() => (
      <BranchStatusInfo
        branch={branch()}
        affectedTabs={affectedTabs({ agents: 0, terminals: 0, files: 2, willStop: true })}
      />
    ))
    expect(screen.getByText('2 files will be closed.')).toBeInTheDocument()
    expect(screen.queryByText(/will be stopped/)).toBeNull()
    expect(screen.queryByText(/will keep running/)).toBeNull()
  })

  it('appends FILE count to the process line when agents/terminals are also affected', () => {
    render(() => (
      <BranchStatusInfo
        branch={branch()}
        affectedTabs={affectedTabs({ agents: 1, terminals: 0, files: 3, willStop: true })}
      />
    ))
    expect(screen.getByText('1 agent will be stopped, 3 files will be closed.')).toBeInTheDocument()
  })

  it('hides the clean message when gitState is undefined (fast-path skipped)', () => {
    render(() => (
      <BranchStatusInfo
        branch={branch({ gitState: undefined })}
        affectedTabs={affectedTabs()}
      />
    ))
    expect(screen.queryByText(/No uncommitted changes or unpushed commits/)).toBeNull()
    expect(screen.queryByText(/Uncommitted changes:/)).toBeNull()
  })
})

describe('hasPushableWork', () => {
  it('returns false for an undefined gitState', () => {
    expect(hasPushableWork(undefined)).toBe(false)
  })

  it('returns false when canPush is false, regardless of pending work', () => {
    // Capability check fails first — no point offering the button.
    expect(hasPushableWork(gitState({ canPush: false, hasUncommittedChanges: true }))).toBe(false)
    expect(hasPushableWork(gitState({ canPush: false, unpushedCommitCount: 3 }))).toBe(false)
    expect(hasPushableWork(gitState({ canPush: false, remoteBranchMissing: true }))).toBe(false)
    expect(hasPushableWork(gitState({ canPush: false, upstreamExists: false }))).toBe(false)
  })

  it('returns false for a clean tree against an existing upstream', () => {
    // Mirrors the "No uncommitted changes or unpushed commits." condition
    // in BranchStatusInfo — when that line shows, the button is a no-op.
    expect(hasPushableWork(gitState())).toBe(false)
  })

  it('returns true when there are uncommitted changes', () => {
    expect(hasPushableWork(gitState({ hasUncommittedChanges: true }))).toBe(true)
  })

  it('returns true when there are unpushed commits', () => {
    expect(hasPushableWork(gitState({ unpushedCommitCount: 1 }))).toBe(true)
  })

  it('returns true when the remote branch is missing', () => {
    expect(hasPushableWork(gitState({ remoteBranchMissing: true }))).toBe(true)
  })

  it('returns true when no upstream is set yet (first push)', () => {
    expect(hasPushableWork(gitState({ upstreamExists: false }))).toBe(true)
  })
})
