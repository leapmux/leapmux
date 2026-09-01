import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { workspaceClient } from '~/api/clients'
import * as workerRpc from '~/api/workerRpc'
import { AgentInfoSchema, AgentProvider, AgentStatus, OpenAgentResponseSchema } from '~/generated/proto/leapmux/v1/agent_pb'
import { CreateWorkspaceResponseSchema, DeleteWorkspaceResponseSchema, TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { seedTabIntoNewWorkspace } from '~/lib/crdt'
import { createRepoGitStore } from '~/stores/repoGit.store'
/// <reference types="vitest/globals" />
import { withPreferences } from '~/test-support/preferencesProvider'
import { NewWorkspaceDialog } from './NewWorkspaceDialog'

// Hoisted alongside the `vi.mock` factories below, which read them. A plain
// `const` would be in its temporal dead zone when the factories run.
const { WORKER_ID, WORKING_DIR, NEW_WORKSPACE_ID } = vi.hoisted(() => ({
  WORKER_ID: 'w1',
  WORKING_DIR: '/home/u/proj',
  NEW_WORKSPACE_ID: 'ws-new',
}))

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
  listDirectory: vi.fn(),
  statFile: vi.fn(async () => ({ info: { modTime: '2026-01-01T00:00:00Z' } })),
}))

// Partial mock: the dialog imports `seedTabIntoNewWorkspace` through the
// barrel, and the barrel's other exports (op builders, the bridge) are pulled
// in by the same import graph.
vi.mock('~/lib/crdt', async importOriginal => ({
  ...(await importOriginal<typeof import('~/lib/crdt')>()),
  seedTabIntoNewWorkspace: vi.fn(),
}))

// The real tree issues its own `listDirectory` round-trips and renders nothing
// this dialog's submit path depends on. Stub it down to the one thing the
// dialog reads back from it -- the selected working directory, which gates
// submit.
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
    availableProviders: [AgentProvider.CLAUDE_CODE],
    metadata: { patch: vi.fn() } as unknown as TabMetadataStore,
    repoGitStore: createRepoGitStore(),
    ...overrides,
  }
  render(withPreferences(() => <NewWorkspaceDialog {...props} />))
  return props
}

/**
 * Drive the dialog to a submittable state and click Create. Submit is gated on
 * a selected worker (arrives with the `listWorkers` mock) AND a non-empty
 * working directory, which only the directory tree can supply.
 */
async function submitDialog(): Promise<void> {
  const createButton = await screen.findByRole('button', { name: 'Create' }) as HTMLButtonElement
  fireEvent.click(screen.getByTestId('pick-dir'))
  await waitFor(() => expect(createButton.disabled).toBe(false))
  fireEvent.click(createButton)
}

describe('newWorkspaceDialog', () => {
  it('opens the agent on the new workspace\'s worker', async () => {
    // No announcement step. A channel carries no workspace set any more, so a
    // workspace created after it opened needs nothing done to it before
    // OpenAgent will be served -- which is what makes the first agent in a
    // freshly-created workspace work without an out-of-band repair.
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

  // The workspace title keeps its WORD-SLUG generator: a workspace carries no
  // "Agent "/"Terminal " prefix, so the tab pool's lone first name would read
  // as an unfinished label. Asserted here because the field now shares its
  // component and validation with the two tab dialogs, and the generator is
  // the one thing that must NOT have been unified with them.
  it('pre-fills a multi-word title, not a single pooled name', async () => {
    renderDialog()

    const input = await screen.findByLabelText('Title') as HTMLInputElement
    expect(input.value.split(' ').length).toBeGreaterThan(1)
    expect(input.value).not.toMatch(/^(?:Agent|Terminal) [A-Z][A-Za-z]+$/)
  })

  it('re-rolls the title from the refresh button', async () => {
    renderDialog()

    const input = await screen.findByLabelText('Title') as HTMLInputElement
    const first = input.value
    // The slug space is large, but a repeat is still possible; click until it
    // moves rather than asserting one click differs.
    for (let i = 0; i < 50 && input.value === first; i++)
      fireEvent.click(screen.getByTestId('title-regenerate'))
    expect(input.value).not.toBe(first)
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

  // The dialog sends the CLEANED title, not the raw one. The hub applies the
  // same rule to whatever arrives, so a raw send showed one title in the UI
  // while the hub stored another until the next refresh overwrote it. The gap
  // widened when the rule started to FOLD: a plain double space is a far more
  // common typo than a control character was.
  it.each([
    ['a repeated space', 'Auth  fix', 'Auth fix'],
    ['a tab', 'Auth\tfix', 'Auth fix'],
    // No newline case here: an `<input type="text">` value cannot hold one,
    // so the DOM removes it before the handler reads it. The fold is covered
    // where it is reachable -- `~/lib/validate` and the sidebar rename, which
    // sets the value through a signal rather than a DOM input.
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

  // The punctuation the rule now KEEPS must reach the hub untouched, so the
  // clean does not become a second, stricter character ban on this side.
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
    // `hydrated: true` marks the OpenAgent reply as the worker's answer for
    // this tab. Without it `useTabHydrators` fires a ListAgents round-trip for
    // an agent this client just created, and that reply lands with none of the
    // live handler's in-flight-settings suppression.
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
   * Metadata BEFORE placement, the order `openTabInFocusedTile` documents and
   * every other open path follows. Placement is what makes the tab exist for
   * the projection and it applies synchronously, so patching afterwards renders
   * the tab untitled and provider-less for at least the microtask the `await`
   * in between costs. The sidebar tree caches its grouping across
   * metadata-only changes, so that window is enough to leave the row on the
   * bare "Agent" label and the generic bot icon until an unrelated tab forces
   * a rebuild.
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
    // Nothing to place and nothing to describe -- but the workspace exists, so
    // it must not be rolled back either.
    expect(seedTabIntoNewWorkspace).not.toHaveBeenCalled()
    expect(props.metadata.patch).not.toHaveBeenCalled()
    expect(workspaceClient.deleteWorkspace).not.toHaveBeenCalled()
  })

  /**
   * Every failure after `CreateWorkspace` has committed must take the
   * workspace with it. Without the rollback the user sees an error, retries,
   * and leaves an empty workspace behind on each attempt.
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
      // A workspace id is the only handle on what was just created. Proceeding
      // with an empty one would seed the agent's tab into "", and deleting ""
      // would be a delete request for whatever the hub resolves an empty id to.
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
})
