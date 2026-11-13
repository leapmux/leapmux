import type { WorkspaceStartPoint } from './workspaceStartPoint'
import type { WorkspaceCreationSubmission } from '~/components/shell/useTabCreation'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { generateSlug } from 'random-word-slugs'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { workerGit } from '~/api/workerRpc'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { ListGitBranchesResponseSchema, ListGitWorktreesResponseSchema } from '~/generated/proto/leapmux/v1/git_pb'
import { GitMode } from '~/hooks/useGitModeState'
import { localStorageClearForTests, localStorageSet, PREFIX_WORKSPACE_GIT_MODE, setStorageAccountForTests } from '~/lib/browserStorage'
import { createRepoGitStore } from '~/stores/repoGit.store'
/// <reference types="vitest/globals" />
import { withPreferences } from '~/test-support/preferencesProvider'
import { NewWorkspaceDialog } from './NewWorkspaceDialog'
import { gitModeStickyKey, readStickyGitMode } from './workspaceStartPoint'

// The vi.mock factories read these values. Hoist them before factory execution to avoid the const temporal dead zone.
const { WORKER_ID, WORKING_DIR, REPO_DIR, NEW_WORKSPACE_ID } = vi.hoisted(() => ({
  WORKER_ID: 'w1',
  WORKING_DIR: '/home/u/proj',
  REPO_DIR: '/home/u/leapmux',
  NEW_WORKSPACE_ID: 'ws-new',
}))

vi.mock('random-word-slugs', async (importOriginal) => {
  const actual = await importOriginal<typeof import('random-word-slugs')>()
  return {
    ...actual,
    generateSlug: vi.fn(actual.generateSlug),
  }
})

vi.mock('~/api/clients', () => ({
  workerClient: {
    listWorkers: vi.fn().mockResolvedValue({
      workers: [
        { id: WORKER_ID, online: true, name: 'worker-1' },
      ],
    }),
  },
  workspaceClient: {
    createWorkspace: vi.fn(),
    deleteWorkspace: vi.fn(),
  },
}))

vi.mock('~/stores/workerInfo.store', () => ({
  workerInfoStore: {
    fetchWorkerInfo: vi.fn().mockResolvedValue(undefined),
    workerInfo: () => null,
    getHomeDir: () => '/home/u',
    getOs: () => undefined,
  },
}))

vi.mock('~/api/workerRpc', () => ({
  workerAgent: {
    openAgent: vi.fn(),
  },
  workerGit: {
    getGitWorkingTreeState: vi.fn(async () => ({ $typeName: 'leapmux.v1.GetGitWorkingTreeStateResponse', isDirty: false })),
    getGitInfo: vi.fn(),
    // A supplied path snapshot mounts GitOptions immediately.
    // It loads each list when the active mode needs it.
    listGitBranches: vi.fn(async () => ({ branches: [] })),
    listGitWorktrees: vi.fn(async () => ({ worktrees: [] })),
  },
  workerFile: {
    listDirectory: vi.fn(),
    statFile: vi.fn(async () => ({
      info: { modTime: '2026-01-01T00:00:00Z' },
    })),
  },
}))

// The real directory tree makes separate listDirectory requests. The submit path needs only its selected working directory.
// This stub supplies that directory through the real onSelect interface.
vi.mock('~/components/tree/DirectoryTree', () => ({
  DirectoryTree: (props: { onSelect: (path: string) => void }) => (
    <button type="button" data-testid="pick-dir" onClick={() => props.onSelect(WORKING_DIR)}>
      pick
    </button>
  ),
}))

const mockCreate = vi.fn<(input: WorkspaceCreationSubmission) => Promise<boolean>>()

beforeAll(() => {
  // jsdom doesn't implement <dialog>; Dialog calls showModal()/close().
  HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
    this.setAttribute('open', '')
  })
  HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) {
    this.removeAttribute('open')
  })
})

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(workerGit.listGitBranches).mockResolvedValue(create(ListGitBranchesResponseSchema, { branches: [], currentBranch: 'main', attachedBranch: 'main' }))
  vi.mocked(workerGit.listGitWorktrees).mockResolvedValue(create(ListGitWorktreesResponseSchema, { worktrees: [] }))
  mockCreate.mockReset()
  mockCreate.mockImplementation(async (input) => {
    input.onCommitted?.({ workspaceId: NEW_WORKSPACE_ID } as never, () => true)
    return true
  })
})

function renderDialog(overrides: Partial<Parameters<typeof NewWorkspaceDialog>[0]> = {}) {
  const props = {
    onCreate: mockCreate,
    onClose: vi.fn(),
    startPoint: { kind: 'directory' } as WorkspaceStartPoint,
    availableProviders: [AgentProvider.CLAUDE_CODE],
    repoGitStore: createRepoGitStore(),
    ...overrides,
  }
  render(withPreferences(() => <NewWorkspaceDialog {...props} />))
  return props
}

/**
 * Select a working directory and click Create after submission becomes available.
 * Submission also requires the worker that the listWorkers mock supplies.
 */
async function submitDialog(): Promise<void> {
  const createButton = await screen.findByRole('button', { name: 'Create' }) as HTMLButtonElement
  // The directory tree mounts once the worker list loads, so wait for it.
  fireEvent.click(await screen.findByTestId('pick-dir'))
  await waitFor(() => expect(createButton.disabled).toBe(false))
  fireEvent.click(createButton)
}

describe('NewWorkspaceDialog', () => {
  it('opens the agent on the new workspace\'s worker', async () => {
    // Channels hold no workspace set. A workspace created after the channel opens must accept OpenAgent without an announcement.
    // The first agent must work without a separate repair request.
    renderDialog()

    await submitDialog()

    await waitFor(() => {
      expect(mockCreate).toHaveBeenCalledOnce()
    })
    expect(mockCreate).toHaveBeenCalledWith(expect.objectContaining({
      workerId: WORKER_ID,
      request: expect.objectContaining({ agentProvider: AgentProvider.CLAUDE_CODE, workingDir: WORKING_DIR }),
    }))
  })

  it('sends the trimmed title to the hub', async () => {
    renderDialog()

    fireEvent.input(await screen.findByLabelText('Title'), {
      target: { value: '  Spaced Out  ' },
    })
    await submitDialog()

    await waitFor(() => {
      expect(mockCreate).toHaveBeenCalledWith(expect.objectContaining({ title: 'Spaced Out' }))
    })
  })

  // Workspace titles must retain their multi-word generator. They have no Agent or Terminal prefix.
  // A single pooled first name would make the label incomplete.
  // The shared field and validation must not replace this generator with the tab-name generator.
  it('pre-fills a multi-word title, not a single pooled name', async () => {
    renderDialog()

    const input = await screen.findByLabelText('Title') as HTMLInputElement
    expect(input.value.split(' ').length).toBeGreaterThan(1)
    expect(input.value).not.toMatch(/^(?:Agent|Terminal) [A-Z][A-Za-z]+$/)
  })

  it('replaces the title through the refresh button', async () => {
    renderDialog()

    const input = await screen.findByLabelText('Title') as HTMLInputElement
    fireEvent.input(input, {
      target: { value: 'Original Title' },
    })
    vi.mocked(generateSlug).mockReturnValueOnce('Replacement Test Title')
    fireEvent.click(screen.getByTestId('title-regenerate'))
    expect(input.value).toBe('Replacement Test Title')
  })

  it('disables submit and creates nothing when the title is emptied', async () => {
    renderDialog()

    const createButton = await screen.findByRole('button', { name: 'Create' }) as HTMLButtonElement
    fireEvent.click(await screen.findByTestId('pick-dir'))
    await waitFor(() => expect(createButton.disabled).toBe(false))

    fireEvent.input(screen.getByLabelText('Title'), {
      target: { value: '  ' },
    })
    await waitFor(() => expect(createButton.disabled).toBe(true))
    expect(screen.getByText('Name must not be empty')).toBeInTheDocument()

    fireEvent.click(createButton)
    expect(mockCreate).not.toHaveBeenCalled()
  })

  // Send the cleaned title so the UI agrees with the hub before a refresh.
  // The hub applies the same rule. Repeated spaces, as well as control characters, can change the stored title.
  it.each([
    ['a repeated space', 'Auth  fix', 'Auth fix'],
    ['a tab', 'Auth\tfix', 'Auth fix'],
    // A text input removes newlines before the handler reads them.
    // Newline normalization remains covered in ~/lib/validate and sidebar rename tests, which set a signal directly.
    ['a no-break space', 'Auth\u00A0fix', 'Auth fix'],
    ['an invisible format character', 'Auth\u200Bfix', 'Authfix'],
    ['a control character', 'Auth\u0000fix', 'Authfix'],
  ])('sends the cleaned title when the input carries %s', async (_label, typed, stored) => {
    renderDialog()

    fireEvent.input(await screen.findByLabelText('Title'), {
      target: { value: typed },
    })
    await submitDialog()

    await waitFor(() => {
      expect(mockCreate).toHaveBeenCalledWith(expect.objectContaining({ title: stored }))
    })
  })

  // Send permitted punctuation unchanged. Client cleanup must not impose a stricter character restriction than the hub.
  it('sends visible punctuation unchanged', async () => {
    renderDialog()

    fireEvent.input(await screen.findByLabelText('Title'), {
      target: { value: '100% of $HOME "quoted"' },
    })
    await submitDialog()

    await waitFor(() => {
      expect(mockCreate).toHaveBeenCalledWith(expect.objectContaining({ title: '100% of $HOME "quoted"' }))
    })
  })

  it('captures the worker request and leaves publication to the coordinator', async () => {
    renderDialog()
    await submitDialog()
    await waitFor(() => expect(mockCreate).toHaveBeenCalledOnce())
    expect(mockCreate.mock.calls[0]?.[0]).toMatchObject({
      workerId: WORKER_ID,
      request: { agentProvider: AgentProvider.CLAUDE_CODE, workingDir: WORKING_DIR },
      onCommitted: expect.any(Function),
      onError: expect.any(Function),
    })
  })

  it('keeps the form open while the coordinator waits for publication', async () => {
    let complete!: (result: boolean) => void
    mockCreate.mockImplementationOnce(() => new Promise((resolve) => {
      complete = resolve
    }))
    const props = renderDialog()
    await submitDialog()
    await waitFor(() => expect(mockCreate).toHaveBeenCalledOnce())
    expect(screen.getByRole('button', { name: 'Creating...' })).toBeDisabled()
    expect(props.onClose).not.toHaveBeenCalled()
    complete(false)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Create' })).toBeEnabled())
    expect(props.onClose).not.toHaveBeenCalled()
  })

  it('reports a creation failure through the captured error callback', async () => {
    mockCreate.mockImplementationOnce(async (input) => {
      input.onError(new Error('worker unavailable'))
      return false
    })
    const props = renderDialog()
    await submitDialog()
    expect(await screen.findByText('worker unavailable')).toBeInTheDocument()
    expect(props.onClose).not.toHaveBeenCalled()
  })

  describe('a repo start point', () => {
    const REPO_START: WorkspaceStartPoint = {
      kind: 'repo',
      workerId: WORKER_ID,
      gitToplevel: REPO_DIR,
      isWorktree: false,
      currentBranch: 'main',
    }

    beforeEach(() => {
      localStorageClearForTests()
      setStorageAccountForTests('u-1')
    })

    it('opens ready to submit, with no directory to pick', async () => {
      // The start point already identifies the worker and repository. The dialog must not request either again.
      renderDialog({ startPoint: REPO_START })

      const createButton = await screen.findByRole('button', { name: 'Create' }) as HTMLButtonElement
      await waitFor(() => expect(createButton.disabled).toBe(false))
      fireEvent.click(createButton)

      await waitFor(() => expect(mockCreate).toHaveBeenCalledOnce())
      expect(mockCreate.mock.calls[0]?.[0].workerId).toBe(WORKER_ID)
      expect(mockCreate.mock.calls[0]?.[0].request)
        .toMatchObject({ workingDir: REPO_DIR })
    })

    // The dialog sends the git mode as ONE git_setup, built from the mode the
    // user picked. The current state is the absent setup: the worker uses the
    // directory as it is.
    it('sends no git setup for the current state', async () => {
      renderDialog({ startPoint: REPO_START })

      const createButton = await screen.findByRole('button', { name: 'Create' }) as HTMLButtonElement
      await waitFor(() => expect(createButton.disabled).toBe(false))
      expect(screen.getByLabelText('Use current state')).toBeChecked()
      fireEvent.click(createButton)

      await waitFor(() => expect(mockCreate).toHaveBeenCalledOnce())
      expect(mockCreate.mock.calls[0]?.[0].request.gitSetup).toBeUndefined()
    })

    it.each([
      [
        'Create new branch',
        'createBranch',
        { name: 'feature-x', baseBranch: 'main' },
      ],
      [
        'Create new worktree',
        'createWorktree',
        { branch: 'feature-x', baseBranch: 'main' },
      ],
    ] as const)('sends the setup of %s with the typed name and the base branch', async (mode, setupCase, value) => {
      renderDialog({ startPoint: REPO_START })
      const createButton = await screen.findByRole('button', { name: 'Create' }) as HTMLButtonElement
      await waitFor(() => expect(createButton.disabled).toBe(false))
      fireEvent.click(await screen.findByLabelText(mode))
      fireEvent.input(await screen.findByPlaceholderText('feature-branch'), {
        target: { value: 'feature-x' },
      })
      await waitFor(() => expect(createButton.disabled).toBe(false))
      fireEvent.click(createButton)

      await waitFor(() => expect(mockCreate).toHaveBeenCalledOnce())
      expect(mockCreate.mock.calls[0]?.[0].request.gitSetup).toEqual({
        mode: { case: setupCase, value },
      })
    })

    it('paints the git options straight away, with no loading spinner', async () => {
      // The supplied snapshot enables skipLoadingFlash. Without it, the dialog shows Loading branch info until the request completes.
      renderDialog({ startPoint: REPO_START })

      expect(await screen.findByLabelText('Use current state')).toBeInTheDocument()
      expect(screen.queryByText(/loading branch info/i)).not.toBeInTheDocument()
    })

    it.each([
      ['invalid branch name', 'Branch name contains invalid characters'],
      ['foo..bar', 'Branch name must not contain ..'],
      ['-bad-start', 'Branch name must not start with'],
    ])('blocks invalid branch input and accepts a corrected value: %s', async (name, message) => {
      renderDialog({ startPoint: REPO_START })
      const submit = await screen.findByRole('button', { name: 'Create' })
      await waitFor(() => expect(submit).toBeEnabled())
      fireEvent.click(await screen.findByLabelText('Create new worktree'))
      const input = await screen.findByPlaceholderText('feature-branch')
      fireEvent.input(input, {
        target: { value: name },
      })
      expect(await screen.findByText(message, { exact: false })).toBeVisible()
      expect(submit).toBeDisabled()
      expect(mockCreate).not.toHaveBeenCalled()
      fireEvent.input(input, {
        target: { value: 'valid-new-branch' },
      })
      await waitFor(() => expect(submit).toBeEnabled())
      expect(screen.queryByText(message, { exact: false })).not.toBeInTheDocument()
    })

    it('replaces the validation message when one invalid branch changes to another', async () => {
      renderDialog({ startPoint: REPO_START })
      fireEvent.click(await screen.findByLabelText('Create new worktree'))
      const input = await screen.findByPlaceholderText('feature-branch')
      const submit = screen.getByRole('button', { name: 'Create' })
      fireEvent.input(input, {
        target: { value: 'invalid branch' },
      })
      expect(await screen.findByText('Branch name contains invalid characters')).toBeVisible()
      fireEvent.input(input, {
        target: { value: 'foo..bar' },
      })
      expect(await screen.findByText('Branch name must not contain ..')).toBeVisible()
      expect(screen.queryByText('Branch name contains invalid characters')).not.toBeInTheDocument()
      expect(submit).toBeDisabled()
      fireEvent.click(submit)
      expect(mockCreate).not.toHaveBeenCalled()
    })

    it.each(['Create new branch', 'Create new worktree'])('refuses an existing branch and accepts a new name in %s', async (mode) => {
      vi.mocked(workerGit.listGitBranches).mockResolvedValue(create(ListGitBranchesResponseSchema, {
        branches: [
          { name: 'claimed-branch' },
        ],
        currentBranch: 'main',
        attachedBranch: 'main',
      }))
      renderDialog({ startPoint: REPO_START })
      fireEvent.click(await screen.findByLabelText(mode))
      const submit = await screen.findByRole('button', { name: 'Create' })
      const input = await screen.findByPlaceholderText('feature-branch')
      fireEvent.input(input, {
        target: { value: 'claimed-branch' },
      })
      expect(await screen.findByText('A branch with this name already exists')).toBeVisible()
      expect(submit).toBeDisabled()
      fireEvent.input(input, {
        target: { value: 'unclaimed-branch' },
      })
      await waitFor(() => expect(submit).toBeEnabled())
      expect(screen.queryByText('A branch with this name already exists')).not.toBeInTheDocument()
    })

    it.each(['Switch to branch', 'Use existing worktree'])('requires a selection in %s and clears that requirement in current mode', async (mode) => {
      vi.mocked(workerGit.listGitBranches).mockResolvedValue(create(ListGitBranchesResponseSchema, {
        branches: [
          { name: 'other' },
        ],
      }))
      vi.mocked(workerGit.listGitWorktrees).mockResolvedValue(create(ListGitWorktreesResponseSchema, {
        worktrees: [
          { path: '/home/u/worktree', branch: 'other' },
        ],
      }))
      renderDialog({ startPoint: REPO_START })
      const submit = await screen.findByRole('button', { name: 'Create' })
      await waitFor(() => expect(submit).toBeEnabled())
      fireEvent.click(await screen.findByLabelText(mode))
      await waitFor(() => expect(submit).toBeDisabled())
      expect(mockCreate).not.toHaveBeenCalled()
      fireEvent.click(screen.getByLabelText('Use current state'))
      await waitFor(() => expect(submit).toBeEnabled())
    })

    it('opens on the mode this repository was last submitted with', async () => {
      localStorageSet(`${PREFIX_WORKSPACE_GIT_MODE}${gitModeStickyKey(WORKER_ID, REPO_DIR)}`, 'create-worktree')
      renderDialog({ startPoint: REPO_START })

      await waitFor(() => expect(screen.getByLabelText('Create new worktree')).toBeChecked())
    })

    it('remembers the mode on a successful submit', async () => {
      renderDialog({ startPoint: REPO_START })

      const createButton = await screen.findByRole('button', { name: 'Create' }) as HTMLButtonElement
      await waitFor(() => expect(createButton.disabled).toBe(false))
      fireEvent.click(await screen.findByLabelText('Create new branch'))
      fireEvent.click(createButton)

      await waitFor(() => {
        expect(readStickyGitMode(gitModeStickyKey(WORKER_ID, REPO_DIR))).toBe(GitMode.CreateBranch)
      })
    })

    it('remembers nothing when the submit fails', async () => {
      // A failed submission must not change the saved mode for the next dialog.
      mockCreate.mockImplementation(async (input) => {
        input.onError(new Error('worker exploded'))
        return false
      })
      renderDialog({ startPoint: REPO_START })

      const createButton = await screen.findByRole('button', { name: 'Create' }) as HTMLButtonElement
      await waitFor(() => expect(createButton.disabled).toBe(false))
      fireEvent.click(await screen.findByLabelText('Create new branch'))
      fireEvent.click(createButton)

      expect(await screen.findByText('worker exploded')).toBeInTheDocument()
      expect(readStickyGitMode(gitModeStickyKey(WORKER_ID, REPO_DIR))).toBeUndefined()
    })

    it('remembers nothing for a directory that is not a repository', async () => {
      // The key identifies a repository root or worktree root, as showGitOptions guarantees.
      // A directory outside a repository has no Git mode to save.
      renderDialog()

      await submitDialog()

      await waitFor(() => expect(mockCreate).toHaveBeenCalledOnce())
      expect(readStickyGitMode(gitModeStickyKey(WORKER_ID, WORKING_DIR))).toBeUndefined()
    })
  })
})
