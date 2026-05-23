import type { GitModeIntent } from '~/hooks/useGitModeState'
import { describe, expect, it } from 'vitest'
import {
  isAgentCreateDisabled,
  isChangeBranchSubmitDisabled,
  isGitModeInvalid,
  isTerminalCreateDisabled,
  isWorkspaceCreateDisabled,
} from '~/components/shell/dialogValidation'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { GitMode } from '~/hooks/useGitModeState'

const validIntent: GitModeIntent = { mode: GitMode.Current }

const validBase = {
  submitting: false,
  workspaceId: 'ws-1',
  workerId: 'worker-1',
  workingDir: '/home/user/project',
  noProviders: false,
  git: validIntent,
}

describe('isWorkspaceCreateDisabled', () => {
  const valid = { ...validBase, titleError: null, sessionIdError: null }

  it('returns false when all fields are valid', () => {
    expect(isWorkspaceCreateDisabled(valid)).toBe(false)
  })

  it('returns true when submitting', () => {
    expect(isWorkspaceCreateDisabled({ ...valid, submitting: true })).toBe(true)
  })

  it('returns true when workerId is empty (no workers online)', () => {
    expect(isWorkspaceCreateDisabled({ ...valid, workerId: '' })).toBe(true)
  })

  it('returns true when workingDir is empty', () => {
    expect(isWorkspaceCreateDisabled({ ...valid, workingDir: '' })).toBe(true)
  })

  it('returns true when workingDir is whitespace-only', () => {
    expect(isWorkspaceCreateDisabled({ ...valid, workingDir: '   ' })).toBe(true)
  })

  it('returns true when title has an error', () => {
    expect(isWorkspaceCreateDisabled({ ...valid, titleError: 'Name must not be empty' })).toBe(true)
  })

  it('returns true when no providers are available', () => {
    expect(isWorkspaceCreateDisabled({ ...valid, noProviders: true })).toBe(true)
  })

  it('returns true when worktree branch has an error in create-worktree mode', () => {
    expect(isWorkspaceCreateDisabled({
      ...valid,
      git: { mode: GitMode.CreateWorktree, worktreeBranch: 'feat', worktreeBranchError: 'Invalid branch', worktreeBaseBranch: 'main' },
    })).toBe(true)
  })

  it('current mode never blocks submit regardless of stale per-mode signals', () => {
    // GitModeIntent's tagged union means a `current` intent literally
    // cannot carry worktree fields, so a stale worktree error from a
    // prior mode toggle cannot leak into the submit gate.
    expect(isWorkspaceCreateDisabled({ ...valid, git: { mode: GitMode.Current } })).toBe(false)
  })

  it('returns true when sessionIdError is set even when everything else is valid', () => {
    // Tighten the matrix: a non-null sessionIdError must veto submit.
    // (Original test set only covered titleError, leaving this gap.)
    expect(isWorkspaceCreateDisabled({
      ...valid,
      sessionIdError: 'invalid session id',
    })).toBe(true)
  })

  it('returns true when switch-branch mode has no branch selected', () => {
    expect(isWorkspaceCreateDisabled({
      ...valid,
      git: { mode: GitMode.SwitchBranch, checkoutBranch: '', checkoutBranchError: null },
    })).toBe(true)
  })

  it('returns false when switch-branch mode has a branch selected', () => {
    expect(isWorkspaceCreateDisabled({
      ...valid,
      git: { mode: GitMode.SwitchBranch, checkoutBranch: 'main', checkoutBranchError: null },
    })).toBe(false)
  })

  it('returns true when use-worktree mode has no path selected', () => {
    expect(isWorkspaceCreateDisabled({
      ...valid,
      git: { mode: GitMode.UseWorktree, useWorktreePath: '' },
    })).toBe(true)
  })

  it('returns false when use-worktree mode has a path selected', () => {
    expect(isWorkspaceCreateDisabled({
      ...valid,
      git: { mode: GitMode.UseWorktree, useWorktreePath: '/path/to/wt' },
    })).toBe(false)
  })
})

describe('isAgentCreateDisabled', () => {
  const valid = { ...validBase, sessionIdError: null }

  it('returns false when all fields are valid', () => {
    expect(isAgentCreateDisabled(valid)).toBe(false)
  })

  it('returns true when workspaceId is empty', () => {
    expect(isAgentCreateDisabled({ ...valid, workspaceId: '' })).toBe(true)
  })

  it('returns true when no providers are available', () => {
    expect(isAgentCreateDisabled({ ...valid, noProviders: true })).toBe(true)
  })

  it('returns true when submitting', () => {
    expect(isAgentCreateDisabled({ ...valid, submitting: true })).toBe(true)
  })

  it('returns true when workerId is empty (no workers online)', () => {
    expect(isAgentCreateDisabled({ ...valid, workerId: '' })).toBe(true)
  })

  it('returns true when workingDir is empty', () => {
    expect(isAgentCreateDisabled({ ...valid, workingDir: '' })).toBe(true)
  })

  it('returns true when workingDir is whitespace-only', () => {
    expect(isAgentCreateDisabled({ ...valid, workingDir: '  ' })).toBe(true)
  })

  it('returns true when worktree branch has an error in create-worktree mode', () => {
    expect(isAgentCreateDisabled({
      ...valid,
      git: { mode: GitMode.CreateWorktree, worktreeBranch: 'feat', worktreeBranchError: 'Invalid branch', worktreeBaseBranch: 'main' },
    })).toBe(true)
  })

  it('current mode never blocks submit', () => {
    expect(isAgentCreateDisabled({ ...valid, git: { mode: GitMode.Current } })).toBe(false)
  })

  it('treats a missing git intent as valid (dialog without git options)', () => {
    expect(isAgentCreateDisabled({ ...valid, git: undefined })).toBe(false)
  })

  it('returns true when sessionIdError is set', () => {
    expect(isAgentCreateDisabled({
      ...valid,
      sessionIdError: 'invalid session id',
    })).toBe(true)
  })
})

describe('isGitModeInvalid', () => {
  it('treats undefined as valid', () => {
    expect(isGitModeInvalid(undefined)).toBe(false)
  })

  describe('current mode', () => {
    it('is always valid', () => {
      expect(isGitModeInvalid({ mode: GitMode.Current })).toBe(false)
    })
  })

  describe('switch-branch mode', () => {
    it('invalid when no branch is selected', () => {
      expect(isGitModeInvalid({ mode: GitMode.SwitchBranch, checkoutBranch: '', checkoutBranchError: null })).toBe(true)
    })

    it('valid when a branch is selected', () => {
      expect(isGitModeInvalid({ mode: GitMode.SwitchBranch, checkoutBranch: 'main', checkoutBranchError: null })).toBe(false)
    })
  })

  describe('create-branch mode', () => {
    it('invalid when branch name is empty', () => {
      expect(isGitModeInvalid({
        mode: GitMode.CreateBranch,
        createBranch: '',
        createBranchError: null,
        createBranchBase: 'main',
      })).toBe(true)
    })

    it('invalid when branch name has an error', () => {
      expect(isGitModeInvalid({
        mode: GitMode.CreateBranch,
        createBranch: 'feat',
        createBranchError: 'taken',
        createBranchBase: 'main',
      })).toBe(true)
    })

    it('valid when base branch is empty (detached HEAD / unborn HEAD; server falls back to HEAD)', () => {
      // The base picker stays empty when `info().currentBranch` is empty
      // (detached HEAD, fresh `git init` with no commits). Locking the
      // submit gate on createBranchBase would lock those users out of
      // creating a branch even though createBranchInDir's `git checkout
      // -b <name>` (no base arg) is the natural default. Regression for
      // the bug where the gate trapped detached-HEAD callers.
      expect(isGitModeInvalid({
        mode: GitMode.CreateBranch,
        createBranch: 'feat',
        createBranchError: null,
        createBranchBase: '',
      })).toBe(false)
    })

    it('valid when name + base are both populated and error-free', () => {
      expect(isGitModeInvalid({
        mode: GitMode.CreateBranch,
        createBranch: 'feat',
        createBranchError: null,
        createBranchBase: 'main',
      })).toBe(false)
    })
  })

  describe('create-worktree mode', () => {
    it('invalid when worktree branch name is empty', () => {
      expect(isGitModeInvalid({
        mode: GitMode.CreateWorktree,
        worktreeBranch: '',
        worktreeBranchError: null,
        worktreeBaseBranch: 'main',
      })).toBe(true)
    })

    it('invalid when worktree branch has an error', () => {
      expect(isGitModeInvalid({
        mode: GitMode.CreateWorktree,
        worktreeBranch: 'feat',
        worktreeBranchError: 'taken',
        worktreeBaseBranch: 'main',
      })).toBe(true)
    })

    it('valid when base branch is empty (detached HEAD / unborn HEAD; server falls back to HEAD)', () => {
      // Mirrors the CreateBranch case: an empty base must NOT lock
      // submit out when `currentBranch` was empty at dialog-open time.
      expect(isGitModeInvalid({
        mode: GitMode.CreateWorktree,
        worktreeBranch: 'feat',
        worktreeBranchError: null,
        worktreeBaseBranch: '',
      })).toBe(false)
    })

    it('valid when name + base are both populated and error-free', () => {
      expect(isGitModeInvalid({
        mode: GitMode.CreateWorktree,
        worktreeBranch: 'feat',
        worktreeBranchError: null,
        worktreeBaseBranch: 'main',
      })).toBe(false)
    })
  })

  describe('use-worktree mode', () => {
    it('invalid when no path is selected', () => {
      expect(isGitModeInvalid({ mode: GitMode.UseWorktree, useWorktreePath: '' })).toBe(true)
    })

    it('valid when a path is selected', () => {
      expect(isGitModeInvalid({ mode: GitMode.UseWorktree, useWorktreePath: '/wt' })).toBe(false)
    })
  })
})

describe('isTerminalCreateDisabled', () => {
  const { noProviders: _, ...terminalBase } = validBase
  const valid = { ...terminalBase, shell: '/bin/bash' }

  it('returns false when all fields are valid', () => {
    expect(isTerminalCreateDisabled(valid)).toBe(false)
  })

  it('returns true when workspaceId is empty', () => {
    expect(isTerminalCreateDisabled({ ...valid, workspaceId: '' })).toBe(true)
  })

  it('returns true when submitting', () => {
    expect(isTerminalCreateDisabled({ ...valid, submitting: true })).toBe(true)
  })

  it('returns true when workerId is empty (no workers online)', () => {
    expect(isTerminalCreateDisabled({ ...valid, workerId: '' })).toBe(true)
  })

  it('returns true when workingDir is empty', () => {
    expect(isTerminalCreateDisabled({ ...valid, workingDir: '' })).toBe(true)
  })

  it('returns true when workingDir is whitespace-only', () => {
    expect(isTerminalCreateDisabled({ ...valid, workingDir: '\t' })).toBe(true)
  })

  it('returns true when shell is empty', () => {
    expect(isTerminalCreateDisabled({ ...valid, shell: '' })).toBe(true)
  })

  it('returns true when worktree branch has an error in create-worktree mode', () => {
    expect(isTerminalCreateDisabled({
      ...valid,
      git: { mode: GitMode.CreateWorktree, worktreeBranch: 'feat', worktreeBranchError: 'err', worktreeBaseBranch: 'main' },
    })).toBe(true)
  })

  it('current mode never blocks submit', () => {
    expect(isTerminalCreateDisabled({ ...valid, git: { mode: GitMode.Current } })).toBe(false)
  })

  it('treats a missing git intent as valid (dialog without git options)', () => {
    expect(isTerminalCreateDisabled({ ...valid, git: undefined })).toBe(false)
  })
})

describe('isChangeBranchSubmitDisabled', () => {
  const switchBranchValid = {
    submitting: false,
    git: { mode: GitMode.SwitchBranch, checkoutBranch: 'main', checkoutBranchError: null } as GitModeIntent,
    worktreeTabType: TabType.AGENT as TabType.AGENT | TabType.TERMINAL,
    noProviders: false,
    shell: '',
  }

  it('returns false when switch-branch mode is valid', () => {
    expect(isChangeBranchSubmitDisabled(switchBranchValid)).toBe(false)
  })

  it('returns true when submitting', () => {
    expect(isChangeBranchSubmitDisabled({ ...switchBranchValid, submitting: true })).toBe(true)
  })

  it('returns true when mode is not one of the three the dialog offers (defensive)', () => {
    // The dialog never renders a `current`/`use-worktree` option, but
    // state.gitMode may briefly hold the default before GitOptions emits.
    expect(isChangeBranchSubmitDisabled({
      ...switchBranchValid,
      git: { mode: GitMode.Current },
    })).toBe(true)
    expect(isChangeBranchSubmitDisabled({
      ...switchBranchValid,
      git: { mode: GitMode.UseWorktree, useWorktreePath: '/wt' },
    })).toBe(true)
  })

  it('returns true when switch-branch has no branch selected', () => {
    expect(isChangeBranchSubmitDisabled({
      ...switchBranchValid,
      git: { mode: GitMode.SwitchBranch, checkoutBranch: '', checkoutBranchError: null },
    })).toBe(true)
  })

  it('returns true when create-branch is missing fields', () => {
    expect(isChangeBranchSubmitDisabled({
      ...switchBranchValid,
      git: { mode: GitMode.CreateBranch, createBranch: '', createBranchError: null, createBranchBase: 'main' },
    })).toBe(true)
  })

  it('returns false when create-branch is fully valid (branch + base, no error)', () => {
    // Create-branch in this dialog never asks for a tab type — unlike
    // create-worktree — so noProviders/shell are irrelevant once the
    // git fields are valid.
    expect(isChangeBranchSubmitDisabled({
      ...switchBranchValid,
      git: { mode: GitMode.CreateBranch, createBranch: 'feat', createBranchError: null, createBranchBase: 'main' },
      noProviders: true,
      shell: '',
    })).toBe(false)
  })

  describe('create-worktree mode', () => {
    const worktreeValidGit: GitModeIntent = {
      mode: GitMode.CreateWorktree,
      worktreeBranch: 'feat',
      worktreeBranchError: null,
      worktreeBaseBranch: 'main',
    }

    it('agent tab type: returns false when providers exist', () => {
      expect(isChangeBranchSubmitDisabled({
        ...switchBranchValid,
        git: worktreeValidGit,
        worktreeTabType: TabType.AGENT,
        noProviders: false,
      })).toBe(false)
    })

    it('agent tab type: returns true when noProviders', () => {
      expect(isChangeBranchSubmitDisabled({
        ...switchBranchValid,
        git: worktreeValidGit,
        worktreeTabType: TabType.AGENT,
        noProviders: true,
      })).toBe(true)
    })

    it('terminal tab type: returns false when shell is set', () => {
      expect(isChangeBranchSubmitDisabled({
        ...switchBranchValid,
        git: worktreeValidGit,
        worktreeTabType: TabType.TERMINAL,
        shell: '/bin/bash',
      })).toBe(false)
    })

    it('terminal tab type: returns true when shell is empty', () => {
      expect(isChangeBranchSubmitDisabled({
        ...switchBranchValid,
        git: worktreeValidGit,
        worktreeTabType: TabType.TERMINAL,
        shell: '',
      })).toBe(true)
    })

    it('returns true when the underlying git mode is invalid (missing branch name)', () => {
      // Empty worktreeBaseBranch is intentionally NOT invalid (detached
      // HEAD / unborn HEAD seed it that way). Use an empty branch name
      // for the "underlying mode invalid" case instead so the assertion
      // pins the legitimate invalid path.
      expect(isChangeBranchSubmitDisabled({
        ...switchBranchValid,
        git: {
          mode: GitMode.CreateWorktree,
          worktreeBranch: '',
          worktreeBranchError: null,
          worktreeBaseBranch: '',
        },
        worktreeTabType: TabType.AGENT,
      })).toBe(true)
    })
  })

  describe('switch-branch no-op detection (via intent.checkoutBranchError)', () => {
    // A user clicking SwitchBranch with the destination set to the
    // branch they're already on used to result in a silent "success" —
    // the worker runs `git checkout <current>` which is a no-op and
    // the UI looked like the operation worked despite changing nothing.
    // The no-op detection lives in GitOptions (where the branches
    // list is available for the local-vs-remote distinction); the
    // resulting message is propagated through `checkoutBranchError`
    // and the dialog validation just reads that field — mirroring the
    // existing CreateBranch / CreateWorktree error-passthrough pattern.
    it('disables Apply when checkoutBranchError is a non-empty string', () => {
      expect(isChangeBranchSubmitDisabled({
        ...switchBranchValid,
        git: {
          mode: GitMode.SwitchBranch,
          checkoutBranch: 'main',
          checkoutBranchError: 'Working directory is already on this branch.',
        },
      })).toBe(true)
    })

    it('keeps Apply enabled when checkoutBranchError is null (the explicit "no error" sentinel)', () => {
      // Belt-and-braces: a future refactor that swapped null → '' would
      // also pass `!!error` so this is light coverage of the boolean
      // coercion the validator uses.
      expect(isChangeBranchSubmitDisabled({
        ...switchBranchValid,
        git: { mode: GitMode.SwitchBranch, checkoutBranch: 'main', checkoutBranchError: null },
      })).toBe(false)
    })

    it('does not consult checkoutBranchError on CreateBranch / CreateWorktree (mode-specific gate)', () => {
      // CreateBranch carries its OWN createBranchError; the
      // SwitchBranch-shaped error must not bleed across variants.
      // (Type-system already prevents this by tag, but the runtime
      // gate also reads `state.git.mode === SwitchBranch` first.)
      expect(isChangeBranchSubmitDisabled({
        ...switchBranchValid,
        git: {
          mode: GitMode.CreateBranch,
          createBranch: 'feat',
          createBranchError: null,
          createBranchBase: 'main',
        },
      })).toBe(false)
    })
  })
})
