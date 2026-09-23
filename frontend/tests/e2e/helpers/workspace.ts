import type { ChildProcess } from 'node:child_process'
import type { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './api'
import { withCleanup } from './cleanup'
import { createTestDirectory } from './runDirectory'

export interface WorkspaceFixture {
  workspaceId: string
}

interface WorkspaceServer {
  hubUrl: string
  adminToken: string
  workerId: string
  hubProc?: ChildProcess
  serverProc?: ChildProcess
}

interface AgentOpenOverrides {
  model?: string
  optionValues?: Record<string, string>
}

/** Keep workspace creation and disposal identical across agent providers. */
export async function withTestWorkspace(
  server: WorkspaceServer,
  prefix: string,
  use: (workspace: WorkspaceFixture) => Promise<void>,
): Promise<void> {
  const workspaceId = await createWorkspaceViaAPI(server.hubUrl, server.adminToken, `${prefix}-${crypto.randomUUID()}`)
  await withCleanup(() => use({ workspaceId }), async () => {
    const hub = server.hubProc ?? server.serverProc
    // A stopped hub cannot answer cleanup. The run owns and removes its database directory.
    if (!hub || (hub.exitCode === null && hub.signalCode === null))
      await deleteWorkspaceViaAPI(server.hubUrl, server.adminToken, workspaceId)
  })
}

/** Open a real agent in a private working directory and close it after use. */
export async function withAgentWorkspace(
  server: WorkspaceServer,
  options: {
    provider: AgentProvider
    prefix: string
    openOptions?: AgentOpenOverrides
  },
  use: (workspace: WorkspaceFixture) => Promise<void>,
): Promise<void> {
  await withTestWorkspace(server, options.prefix, async (workspace) => {
    const workingDir = createTestDirectory(`${options.prefix}-wd-`)
    const defaults = agentOpenOptions(agentSettings(options.provider))
    await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspace.workspaceId, workingDir, {
      agentProvider: options.provider,
      ...defaults,
      ...options.openOptions,
      optionValues: {
        ...defaults.optionValues,
        ...options.openOptions?.optionValues,
      },
    })
    await use(workspace)
  })
}
