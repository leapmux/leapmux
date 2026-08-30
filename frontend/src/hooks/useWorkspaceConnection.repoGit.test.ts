import type { UseWatchEventsStreamsOpts } from '~/hooks/useWatchEventsStreams'
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { TerminalStatus } from '~/generated/proto/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { useWorkspaceConnection } from '~/hooks/useWorkspaceConnection'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { emitAddTab } from '~/stores/tabOps'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'

vi.mock('~/api/workerRpc', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/workerRpc')>()
  return {
    ...actual,
    // The tail-reconcile effect asks for the newest page of every agent tab.
    // Answered with an empty page so the hook's own promise chains settle.
    listAgentMessages: vi.fn().mockResolvedValue({ messages: [], hasMore: false }),
    channelManager: {
      getOrOpenChannel: vi.fn().mockResolvedValue('ch-1'),
      hasOpenChannelForWorker: vi.fn().mockReturnValue(true),
      fatalCloseInfo: vi.fn(() => null),
    },
  }
})

vi.mock('~/components/common/Toast', () => ({
  showWarnToast: vi.fn(),
  showInfoToast: vi.fn(),
  showWarnToastUnlessDisconnected: vi.fn(),
}))

/**
 * The real stream hook dials a channel. Replaced with a capture so a test can
 * report the offline / online transitions itself -- which is the whole input
 * to the sweep effect under test.
 */
const streams = vi.hoisted(() => ({ opts: undefined as unknown }))

vi.mock('~/hooks/useWatchEventsStreams', () => ({
  useWatchEventsStreams: (opts: unknown) => {
    streams.opts = opts
    return { abortSignalFor: () => undefined }
  },
}))

// The capture is module state, so it outlives a test. Reset it, or a mount that
// installs nothing leaves the guard below reading the PREVIOUS test's callbacks
// on a disposed root, and the assertions pass against the wrong hook.
beforeEach(() => {
  streams.opts = undefined
})

/** The captured stream callbacks, typed. */
function streamOpts(): UseWatchEventsStreamsOpts {
  if (!streams.opts)
    throw new Error('useWorkspaceConnection did not open its watch streams')
  return streams.opts as UseWatchEventsStreamsOpts
}

const WS = 'ws-repo-git-offline'
const WORKER = 'w-1'
const REPO = '/home/dev/repo'

let nextPosition = 0

/**
 * One agent tab and one terminal tab on the same worker, over a real CRDT
 * bridge, with the worker's repo already known to the git store.
 *
 * The agent tab is deliberately left UNSELECTED. A background tab is the whole
 * case: the active tab is refreshed by AppShell whenever its context changes,
 * so it repairs itself and can never show the defect.
 */
function mountConnection() {
  const harness = installTestBridge({ workspaceId: WS })
  const { view, metadata, selection } = createTestTabStores(WS)
  const repoGitStore = createRepoGitStore()
  const chatStore = createChatStore()

  const place = (type: TabType, id: string) => {
    nextPosition += 1
    emitAddTab({ type, id, tileId: harness.rootTileId, position: `p${nextPosition}`, workerId: WORKER })
  }
  place(TabType.AGENT, 'a1')
  place(TabType.TERMINAL, 't1')
  // `workerId` is deliberately absent here: it lives on the projection, and
  // the `emitAddTab` above carries it there. TabMetadata has no such field.
  metadata.patch('a1', { workingDir: REPO, gitToplevel: REPO, agentStatus: AgentStatus.ACTIVE })
  metadata.patch('t1', { workingDir: REPO, gitToplevel: REPO, terminalStatus: TerminalStatus.READY })

  repoGitStore.upsert(repoKey(WORKER, REPO), {
    workerId: WORKER,
    toplevel: REPO,
    branch: 'feature/sidebar',
    originUrl: 'git@example.com:org/repo.git',
    gitStatusSeen: true,
  })

  let dispose!: () => void
  createRoot((d) => {
    dispose = d
    useWorkspaceConnection({
      chatStore,
      view,
      metadata,
      selection,
      controlStore: createControlStore(),
      agentSessionStore: createAgentSessionStore(),
      repoGitStore,
      settingsLoading: createLoadingSignal(),
      getActiveWorkspaceId: () => WS,
    })
  })
  return { dispose, repoGitStore, chatStore, view, metadata }
}

/**
 * What a dropped worker link may and may not take with it.
 *
 * `useWatchEventsStreams` reports a worker offline for any transport-shaped
 * failure -- a closed socket, a sleeping laptop, a restarted hub. The sweep
 * that follows used to call `repoGitStore.clearForWorker`, which deleted the
 * branch, the origin URL and the diff stats for every repo on that worker.
 *
 * Three rules kept a BACKGROUND tab from recovering. `useTabHydrators` re-asks
 * only for a tab that is not `hydrated`, and an agent tab stays `hydrated` for
 * the rest of the page. The worker sends a git status at that agent's own turn
 * end. The catch-up replay after a reconnect carries one only for an agent
 * promoted to FULL.
 *
 * Meanwhile `Tab.gitToplevel` survives the outage untouched, and
 * `WorkspaceTabTree.repoKeyAndLabel` groups a tab by that field alone. So the
 * sidebar kept the repo group and moved every tab under it to the no-branch
 * bucket, until the user clicked the tab or the Files refresh button.
 *
 * The entry is last-known repo state, not a liveness claim. The store already
 * keeps it across one transient bad probe ("keeping last-good repo state" in
 * `RepoGitStore.refresh`), and `RepoGitState.nonRepoProbeIgnored` limits that
 * to one. Permanent removal has its own caller: `useWorkerSection` clears on
 * deregistration.
 */
describe('useWorkspaceConnection worker-offline sweep', () => {
  it('keeps a background tab\'s branch and origin when the worker link drops', () => {
    const { dispose, repoGitStore } = mountConnection()
    try {
      streamOpts().onWorkerOnline(WORKER, false)

      const repo = repoGitStore.get(repoKey(WORKER, REPO))
      expect(repo?.branch, 'a transport blip is not news about the working tree').toBe('feature/sidebar')
      expect(repo?.originUrl).toBe('git@example.com:org/repo.git')
    }
    finally {
      dispose()
    }
  })

  it('still knows the branch after the link returns, with no tab click', () => {
    const { dispose, repoGitStore } = mountConnection()
    try {
      streamOpts().onWorkerOnline(WORKER, false)
      streamOpts().onWorkerOnline(WORKER, true)

      expect(
        repoGitStore.get(repoKey(WORKER, REPO))?.branch,
        'a backgrounded agent tab gets no catch-up git status, so a drop here is permanent',
      ).toBe('feature/sidebar')
    }
    finally {
      dispose()
    }
  })

  it('leaves a repo on a still-connected worker alone', () => {
    const { dispose, repoGitStore } = mountConnection()
    try {
      repoGitStore.upsert(repoKey('w-2', REPO), { workerId: 'w-2', toplevel: REPO, branch: 'main' })
      streamOpts().onWorkerOnline(WORKER, false)

      expect(repoGitStore.get(repoKey('w-2', REPO))?.branch).toBe('main')
    }
    finally {
      dispose()
    }
  })

  it('still disconnects the worker\'s running terminals', () => {
    const { dispose, view } = mountConnection()
    try {
      streamOpts().onWorkerOnline(WORKER, false)

      expect(view.getTerminalTab('t1')?.status).toBe(TerminalStatus.DISCONNECTED)
    }
    finally {
      dispose()
    }
  })

  it('still drops its ACTIVE agents to INACTIVE and clears their streaming text', () => {
    const { dispose, view, chatStore } = mountConnection()
    try {
      chatStore.streamingText.set('a1', 'half a sentence')
      streamOpts().onWorkerOnline(WORKER, false)

      expect(view.getAgentTab('a1')?.agentStatus).toBe(AgentStatus.INACTIVE)
      expect(chatStore.streamingText.get('a1'), 'the process that would finish it is gone').toBe('')
    }
    finally {
      dispose()
    }
  })
})
