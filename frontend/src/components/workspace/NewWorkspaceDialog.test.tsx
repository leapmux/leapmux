import type { WorkspaceStartPoint } from './workspaceStartPoint'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { generateSlug } from 'random-word-slugs'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { workspaceClient } from '~/api/clients'
import * as workerRpc from '~/api/workerRpc'
import { AgentInfoSchema, AgentProvider, AgentStatus, OpenAgentResponseSchema } from '~/generated/proto/leapmux/v1/agent_pb'
import { ListGitBranchesResponseSchema, ListGitWorktreesResponseSchema } from '~/generated/proto/leapmux/v1/git_pb'
import { CreateWorkspaceResponseSchema, DeleteWorkspaceResponseSchema, TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { GitMode } from '~/hooks/useGitModeState'
import { localStorageClearForTests, localStorageSet, PREFIX_WORKSPACE_GIT_MODE, setStorageAccountForTests } from '~/lib/browserStorage'
import { seedTabIntoNewWorkspace } from '~/lib/crdt'
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
  return { ...actual, generateSlug: vi.fn(actual.generateSlug) }
})

vi.mock('~/api/clients', () => ({
  workerClient: {
    listWorkers: vi.fn().mockResolvedValue({
      workers: [{ id: WORKER_ID, online: true, name: 'worker-1' }],
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
  openAgent: vi.fn(),
  getGitInfo: vi.fn(),
  // GitOptions requests both lists on mount. A supplied path snapshot makes it mount immediately.
  listGitBranches: vi.fn(async () => ({ branches: [] })),
  listGitWorktrees: vi.fn(async () => ({ worktrees: [] })),
  listDirectory: vi.fn(),
  statFile: vi.fn(async () => ({ info: { modTime: '2026-01-01T00:00:00Z' } })),
}))

// Mock seedTabIntoNewWorkspace through the same module that the dialog imports.
// Keep its other exports because the import graph also requires operation builders and the bridge.
vi.mock('~/lib/crdt', async importOriginal => ({
  ...(await importOriginal<typeof import('~/lib/crdt')>()),
  seedTabIntoNewWorkspace: vi.fn(),
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

/** What the worker reports back for the agent the dialog opens. */
const AGENT = create(AgentInfoSchema, {
  id: 'agent-1',
  workerId: WORKER_ID,
  title: 'Agent Mimi',
  workingDir: WORKING_DIR,
  agentProvider: AgentProvider.CLAUDE_CODE,
  status: AgentStatus.ACTIVE,
})

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
  vi.mocked(workerRpc.listGitBranches).mockResolvedValue(create(ListGitBranchesResponseSchema, { branches: [], currentBranch: 'main' }))
  vi.mocked(workerRpc.listGitWorktrees).mockResolvedValue(create(ListGitWorktreesResponseSchema, { worktrees: [] }))
  vi.mocked(workspaceClient.createWorkspace).mockResolvedValue(
    create(CreateWorkspaceResponseSchema, { workspaceId: NEW_WORKSPACE_ID }),
  )
  vi.mocked(workspaceClient.deleteWorkspace).mockResolvedValue(create(DeleteWorkspaceResponseSchema, {}))
  vi.mocked(workerRpc.openAgent).mockResolvedValue(
    create(OpenAgentResponseSchema, { agent: AGENT }),
  )
  vi.mocked(seedTabIntoNewWorkspace).mockResolvedValue({ rootNodeId: 'root-1', position: 'n' })
})

function renderDialog(overrides: Partial<Parameters<typeof NewWorkspaceDialog>[0]> = {}) {
  const props = {
    onCreated: vi.fn(),
    onClose: vi.fn(),
    startPoint: { kind: 'directory' } as WorkspaceStartPoint,
    availableProviders: [AgentProvider.CLAUDE_CODE],
    metadata: { patch: vi.fn() } as unknown as TabMetadataStore,
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
  fireEvent.click(screen.getByTestId('pick-dir'))
  await waitFor(() => expect(createButton.disabled).toBe(false))
  fireEvent.click(createButton)
}

describe('newWorkspaceDialog', () => {
  it('opens the agent on the new workspace\'s worker', async () => {
    // Channels hold no workspace set. A workspace created after the channel opens must accept OpenAgent without an announcement.
    // The first agent must work without a separate repair request.
    renderDialog()

    await submitDialog()

    await waitFor(() => {
      expect(workerRpc.openAgent).toHaveBeenCalledOnce()
    })
    expect(workerRpc.openAgent).toHaveBeenCalledWith(WORKER_ID, expect.objectContaining({
      agentProvider: AgentProvider.CLAUDE_CODE,
      workingDir: WORKING_DIR,
    }))
  })

  it('sends the trimmed title to the hub', async () => {
    renderDialog()

    fireEvent.input(await screen.findByLabelText('Title'), {
      target: { value: '  Spaced Out  ' },
    })
    await submitDialog()

    await waitFor(() => {
      expect(workspaceClient.createWorkspace).toHaveBeenCalledWith({ title: 'Spaced Out' })
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
    fireEvent.input(input, { target: { value: 'Original Title' } })
    vi.mocked(generateSlug).mockReturnValueOnce('Replacement Test Title')
    fireEvent.click(screen.getByTestId('title-regenerate'))
    expect(input.value).toBe('Replacement Test Title')
  })

  it('disables submit and creates nothing when the title is emptied', async () => {
    renderDialog()

    const createButton = await screen.findByRole('button', { name: 'Create' }) as HTMLButtonElement
    fireEvent.click(screen.getByTestId('pick-dir'))
    await waitFor(() => expect(createButton.disabled).toBe(false))

    fireEvent.input(screen.getByLabelText('Title'), { target: { value: '  ' } })
    await waitFor(() => expect(createButton.disabled).toBe(true))
    expect(screen.getByText('Name must not be empty')).toBeInTheDocument()

    fireEvent.click(createButton)
    expect(workspaceClient.createWorkspace).not.toHaveBeenCalled()
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
      expect(workspaceClient.createWorkspace).toHaveBeenCalledWith({ title: stored })
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
      expect(workspaceClient.createWorkspace).toHaveBeenCalledWith({ title: '100% of $HOME "quoted"' })
    })
  })

  it('places the agent in the CRDT and seeds its metadata as hydrated', async () => {
    const props = renderDialog()

    await submitDialog()

    await waitFor(() => {
      expect(props.onCreated).toHaveBeenCalledWith(NEW_WORKSPACE_ID)
    })
    expect(seedTabIntoNewWorkspace).toHaveBeenCalledWith({
      workspaceId: NEW_WORKSPACE_ID,
      tabType: TabType.AGENT,
      tabId: 'agent-1',
      workerId: WORKER_ID,
    })
    // hydrated: true marks the OpenAgent response as the worker state for this tab.
    // Without it, useTabHydrators requests ListAgents for an agent that this client just created.
    // That response lacks the live handler suppression for settings that still await a response.
    expect(props.metadata.patch).toHaveBeenCalledWith('agent-1', expect.objectContaining({
      title: 'Agent Mimi',
      workerId: WORKER_ID,
      agentProvider: AgentProvider.CLAUDE_CODE,
      hydrated: true,
    }))
  })

  // Both facts belong to `openedAgentTabFields`, not to this call site; its
  // doc comment states why each one holds.
  //   - `hydrated: true` reaches the row.
  //   - The row gets no `gitToplevel`, because the response carries no status.
  it('marks the new agent hydrated and gives it no repo identity', async () => {
    const props = renderDialog()

    await submitDialog()

    await waitFor(() => {
      expect(props.onCreated).toHaveBeenCalledWith(NEW_WORKSPACE_ID)
    })
    expect(props.metadata.patch).toHaveBeenCalledWith('agent-1', expect.objectContaining({
      hydrated: true,
    }))
    expect(props.metadata.patch).toHaveBeenCalledWith('agent-1', expect.not.objectContaining({
      gitToplevel: expect.anything(),
    }))
    expect(
      Object.keys(props.repoGitStore.repos()),
      'no repo identity on the row, so none in the store either',
    ).toEqual([])
  })

  /**
   * Write metadata before placement, as openTabInFocusedTile requires.
   * Placement creates the tab in the projection synchronously. Metadata written after an await leaves a temporary tab without a title or provider.
   * The sidebar caches its groups across metadata-only changes.
   * That temporary state can retain the generic Agent label and icon until an unrelated tab forces a rebuild.
   */
  it('seeds the metadata before placing the tab', async () => {
    const order: string[] = []
    const props = renderDialog()
    vi.mocked(props.metadata.patch).mockImplementation(() => {
      order.push('metadata')
    })
    vi.mocked(seedTabIntoNewWorkspace).mockImplementation(async () => {
      order.push('placement')
      return { rootNodeId: 'root-1', position: 'n' }
    })

    await submitDialog()

    await waitFor(() => {
      expect(props.onCreated).toHaveBeenCalledWith(NEW_WORKSPACE_ID)
    })
    expect(order).toEqual(['metadata', 'placement'])
  })

  it('reports the workspace even when the worker returns no agent', async () => {
    vi.mocked(workerRpc.openAgent).mockResolvedValue(create(OpenAgentResponseSchema, {}))
    const props = renderDialog()

    await submitDialog()

    await waitFor(() => {
      expect(props.onCreated).toHaveBeenCalledWith(NEW_WORKSPACE_ID)
    })
    // The response supplies no tab to place, but the workspace exists and must remain.
    expect(seedTabIntoNewWorkspace).not.toHaveBeenCalled()
    expect(props.metadata.patch).not.toHaveBeenCalled()
    expect(workspaceClient.deleteWorkspace).not.toHaveBeenCalled()
  })

  /**
   * Roll back the workspace after every failure that follows a committed CreateWorkspace request.
   * Otherwise, each retry leaves an empty workspace behind.
   */
  describe('rollback', () => {
    it('deletes the workspace when the agent fails to open', async () => {
      vi.mocked(workerRpc.openAgent).mockRejectedValue(new Error('worker exploded'))
      const props = renderDialog()

      await submitDialog()

      await waitFor(() => {
        expect(workspaceClient.deleteWorkspace).toHaveBeenCalledWith({ workspaceId: NEW_WORKSPACE_ID })
      })
      expect(props.onCreated).not.toHaveBeenCalled()
      expect(await screen.findByText('worker exploded')).toBeInTheDocument()
    })

    it('has nothing to roll back when the workspace itself fails to be created', async () => {
      vi.mocked(workspaceClient.createWorkspace).mockRejectedValue(new Error('quota exceeded'))
      renderDialog()

      await submitDialog()

      expect(await screen.findByText('quota exceeded')).toBeInTheDocument()
      expect(workspaceClient.deleteWorkspace).not.toHaveBeenCalled()
      expect(workerRpc.openAgent).not.toHaveBeenCalled()
    })

    it('fails loudly, and without a rollback, when the response carries no workspace id', async () => {
      // The workspace ID identifies the newly created workspace.
      // An empty ID would place the agent in an empty workspace key and send a delete request with that same invalid ID.
      vi.mocked(workspaceClient.createWorkspace).mockResolvedValue(
        create(CreateWorkspaceResponseSchema, { workspaceId: '' }),
      )
      const props = renderDialog()

      await submitDialog()

      expect(await screen.findByText('No workspace ID in response')).toBeInTheDocument()
      expect(workerRpc.openAgent).not.toHaveBeenCalled()
      expect(workspaceClient.deleteWorkspace).not.toHaveBeenCalled()
      expect(props.onCreated).not.toHaveBeenCalled()
    })
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

      await waitFor(() => expect(workerRpc.openAgent).toHaveBeenCalledOnce())
      expect(vi.mocked(workerRpc.openAgent).mock.calls[0][1])
        .toMatchObject({ workerId: WORKER_ID, workingDir: REPO_DIR })
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
      fireEvent.input(input, { target: { value: name } })
      expect(await screen.findByText(message, { exact: false })).toBeVisible()
      expect(submit).toBeDisabled()
      expect(workerRpc.openAgent).not.toHaveBeenCalled()
      fireEvent.input(input, { target: { value: 'valid-new-branch' } })
      await waitFor(() => expect(submit).toBeEnabled())
      expect(screen.queryByText(message, { exact: false })).not.toBeInTheDocument()
    })

    it('replaces the validation message when one invalid branch changes to another', async () => {
      renderDialog({ startPoint: REPO_START })
      fireEvent.click(await screen.findByLabelText('Create new worktree'))
      const input = await screen.findByPlaceholderText('feature-branch')
      const submit = screen.getByRole('button', { name: 'Create' })
      fireEvent.input(input, { target: { value: 'invalid branch' } })
      expect(await screen.findByText('Branch name contains invalid characters')).toBeVisible()
      fireEvent.input(input, { target: { value: 'foo..bar' } })
      expect(await screen.findByText('Branch name must not contain ..')).toBeVisible()
      expect(screen.queryByText('Branch name contains invalid characters')).not.toBeInTheDocument()
      expect(submit).toBeDisabled()
      fireEvent.click(submit)
      expect(workspaceClient.createWorkspace).not.toHaveBeenCalled()
    })

    it.each(['Create new branch', 'Create new worktree'])('refuses an existing branch and accepts a new name in %s', async (mode) => {
      vi.mocked(workerRpc.listGitBranches).mockResolvedValue(create(ListGitBranchesResponseSchema, {
        branches: [{ name: 'claimed-branch' }],
        currentBranch: 'main',
      }))
      renderDialog({ startPoint: REPO_START })
      fireEvent.click(await screen.findByLabelText(mode))
      const submit = await screen.findByRole('button', { name: 'Create' })
      const input = await screen.findByPlaceholderText('feature-branch')
      fireEvent.input(input, { target: { value: 'claimed-branch' } })
      expect(await screen.findByText('A branch with this name already exists')).toBeVisible()
      expect(submit).toBeDisabled()
      fireEvent.input(input, { target: { value: 'unclaimed-branch' } })
      await waitFor(() => expect(submit).toBeEnabled())
      expect(screen.queryByText('A branch with this name already exists')).not.toBeInTheDocument()
    })

    it.each(['Switch to branch', 'Use existing worktree'])('requires a selection in %s and clears that requirement in current mode', async (mode) => {
      vi.mocked(workerRpc.listGitBranches).mockResolvedValue(create(ListGitBranchesResponseSchema, { branches: [{ name: 'other' }] }))
      vi.mocked(workerRpc.listGitWorktrees).mockResolvedValue(create(ListGitWorktreesResponseSchema, {
        worktrees: [{ path: '/home/u/worktree', branch: 'other' }],
      }))
      renderDialog({ startPoint: REPO_START })
      const submit = await screen.findByRole('button', { name: 'Create' })
      await waitFor(() => expect(submit).toBeEnabled())
      fireEvent.click(await screen.findByLabelText(mode))
      await waitFor(() => expect(submit).toBeDisabled())
      expect(workerRpc.openAgent).not.toHaveBeenCalled()
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
      vi.mocked(workerRpc.openAgent).mockRejectedValue(new Error('worker exploded'))
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

      await waitFor(() => expect(workerRpc.openAgent).toHaveBeenCalledOnce())
      expect(readStickyGitMode(gitModeStickyKey(WORKER_ID, WORKING_DIR))).toBeUndefined()
    })
  })
})
