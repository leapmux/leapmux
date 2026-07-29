/// <reference types="vitest/globals" />
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { workspaceClient } from '~/api/clients'
import * as workerRpc from '~/api/workerRpc'
import { ensureWorkspaceAccess } from '~/api/workspaceAccess'
import { AgentInfoSchema, AgentProvider, AgentStatus, OpenAgentResponseSchema } from '~/generated/leapmux/v1/agent_pb'
import { CreateWorkspaceResponseSchema, DeleteWorkspaceResponseSchema, TabType } from '~/generated/leapmux/v1/workspace_pb'
import { seedTabIntoNewWorkspace } from '~/lib/crdt'
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
}))

vi.mock('~/api/workspaceAccess', () => ({ ensureWorkspaceAccess: vi.fn() }))

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
  workspaceId: NEW_WORKSPACE_ID,
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
  vi.mocked(ensureWorkspaceAccess).mockResolvedValue(true)
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
    ...overrides,
  }
  render(() => <NewWorkspaceDialog {...props} />)
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
  /**
   * The workspace is brand new, so no channel this page holds was seeded with
   * it at `OpenChannel` time. `OpenAgent` on it is refused until
   * `PrepareWorkspaceAccess` has run and been acked -- announcing AFTER the
   * open, or not awaiting it, puts the two in a race the dialog loses roughly
   * every time.
   */
  it('announces the new workspace before opening an agent in it', async () => {
    let announce: (fresh: boolean) => void = () => {}
    vi.mocked(ensureWorkspaceAccess).mockReturnValue(new Promise<boolean>((res) => {
      announce = res
    }))
    renderDialog()

    await submitDialog()

    await waitFor(() => {
      expect(ensureWorkspaceAccess).toHaveBeenCalledWith(WORKER_ID, NEW_WORKSPACE_ID)
    })
    expect(workerRpc.openAgent, 'the agent must wait for the announcement to be acked')
      .not
      .toHaveBeenCalled()

    announce(true)

    await waitFor(() => {
      expect(workerRpc.openAgent).toHaveBeenCalledOnce()
    })
    expect(workerRpc.openAgent).toHaveBeenCalledWith(WORKER_ID, expect.objectContaining({
      workspaceId: NEW_WORKSPACE_ID,
      agentProvider: AgentProvider.CLAUDE_CODE,
      workingDir: WORKING_DIR,
    }))
  })

  it('opens the agent even when the announcer reports nothing new', async () => {
    // `ensure` answers false when the pair was already announced. Elsewhere
    // that answer ends a repair loop, but here it is not a failure and must not
    // be treated as one: it only means some other caller announced this
    // workspace first, which is exactly the state OpenAgent needs.
    vi.mocked(ensureWorkspaceAccess).mockResolvedValue(false)
    const props = renderDialog()

    await submitDialog()

    await waitFor(() => {
      expect(props.onCreated).toHaveBeenCalledWith(NEW_WORKSPACE_ID)
    })
    expect(workerRpc.openAgent).toHaveBeenCalledOnce()
    expect(workspaceClient.deleteWorkspace).not.toHaveBeenCalled()
  })

  it('sends the trimmed title to the hub', async () => {
    renderDialog()

    fireEvent.input(await screen.findByPlaceholderText('New Workspace'), {
      target: { value: '  Spaced Out  ' },
    })
    await submitDialog()

    await waitFor(() => {
      expect(workspaceClient.createWorkspace).toHaveBeenCalledWith({ title: 'Spaced Out' })
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

    it('deletes the workspace when the announcement fails', async () => {
      vi.mocked(ensureWorkspaceAccess).mockRejectedValue(new Error('hub unreachable'))
      const props = renderDialog()

      await submitDialog()

      await waitFor(() => {
        expect(workspaceClient.deleteWorkspace).toHaveBeenCalledWith({ workspaceId: NEW_WORKSPACE_ID })
      })
      // An unannounced workspace is one `OpenAgent` would be refused on, so
      // there is nothing to gain by trying it anyway.
      expect(workerRpc.openAgent).not.toHaveBeenCalled()
      expect(props.onCreated).not.toHaveBeenCalled()
      expect(await screen.findByText('hub unreachable')).toBeInTheDocument()
    })

    it('has nothing to roll back when the workspace itself fails to be created', async () => {
      vi.mocked(workspaceClient.createWorkspace).mockRejectedValue(new Error('quota exceeded'))
      renderDialog()

      await submitDialog()

      expect(await screen.findByText('quota exceeded')).toBeInTheDocument()
      expect(workspaceClient.deleteWorkspace).not.toHaveBeenCalled()
      expect(ensureWorkspaceAccess).not.toHaveBeenCalled()
    })

    it('fails loudly, and without a rollback, when the response carries no workspace id', async () => {
      // A workspace id is the only handle on what was just created. Proceeding
      // with an empty one would announce and open an agent against "", and
      // deleting "" would be a delete request for whatever the hub resolves an
      // empty id to.
      vi.mocked(workspaceClient.createWorkspace).mockResolvedValue(
        create(CreateWorkspaceResponseSchema, { workspaceId: '' }),
      )
      const props = renderDialog()

      await submitDialog()

      expect(await screen.findByText('No workspace ID in response')).toBeInTheDocument()
      expect(ensureWorkspaceAccess).not.toHaveBeenCalled()
      expect(workspaceClient.deleteWorkspace).not.toHaveBeenCalled()
      expect(props.onCreated).not.toHaveBeenCalled()
    })
  })
})
