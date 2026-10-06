import type { Page } from '@playwright/test'
import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ServerInfo } from '../fixtures'
import type { NativeWorker } from './nativeWorker'
import type { ProviderAgent, WorkspaceFixture } from './workspace'
import { rmSync } from 'node:fs'
import { currentNativeAgent } from './nativeScenario'
import { withNativeWorker } from './nativeWorker'
import { createTestDirectory, isFileNameComponent } from './runDirectory'
import { loginViaToken, openWorkspace } from './ui'
import { newProviderWorkingDir, withTestWorkspace } from './workspace'

/** The suite server that a private Worker registers with. */
export type PrivateWorkerHub = Pick<ServerInfo, 'hubUrl' | 'adminToken' | 'workerId' | 'agentEnv'>

/** What the private files of a workspace give its Worker, and what the fixture keeps from them. */
export interface PrivateWorkerSetup<Setup> {
  /**
   * The agent environment of the Worker, in place of the suite's own one. A Worker that needs a private mock
   * environment, such as one from `createMockAgentEnvironment`, states it. The default is the suite's environment.
   */
  agentEnv?: Record<string, string>
  /** Variables that the Worker environment adds to the agent environment. */
  env?: NodeJS.ProcessEnv
  /** What the fixture keeps from its private files, such as a receipt path. The workspace hands it back unchanged. */
  setup: Setup
}

/** How one private-Worker workspace starts. */
export interface PrivateNativeWorkspaceOptions<Server extends PrivateWorkerHub, Setup> {
  /**
   * The prefix of the run directory, of the Worker data directory, of the workspace, and of the working directory.
   * It must be one file-name component.
   */
  prefix: string
  /** The Worker name, which the Worker output states. */
  workerName: string
  /** How an agent of the provider opens. The working directory follows the rule of its provider. */
  providerAgent: ProviderAgent
  /**
   * Write the private files of the Worker into `runDirectory`, which the workspace created for it, before the Worker
   * starts. The run directory goes away after the Worker exited, or at once when no Worker starts.
   */
  prepare: (runDirectory: string) => Promise<PrivateWorkerSetup<Setup>> | PrivateWorkerSetup<Setup>
  /** Open the agent of the workspace on the private Worker, in `workingDir`, and return its ID. */
  openAgent: (server: NativeWorker<Server>['server'], workspaceId: string, workingDir: string) => Promise<string>
}

/** One workspace on a private Worker, with the one agent that it opened and that the page shows. */
export interface PrivateNativeWorkspace<Server extends PrivateWorkerHub, Setup> extends WorkspaceFixture {
  /** The server of the private Worker: the suite server with the Worker ID and the agent environment of the Worker. */
  server: NativeWorker<Server>['server']
  agentId: string
  workingDir: string
  /** The opened agent as the Worker reports it, after the Worker reports it active. */
  agent: AgentInfo
  /** The directory of the private files. */
  runDirectory: string
  /** What `prepare` kept. */
  setup: Setup
}

/**
 * Run `use` with one workspace on a private Worker. The chain is the same for each provider that needs a Worker of its
 * own:
 *
 * 1. Create a run directory, and write the private files of the Worker there (`prepare`).
 * 2. Register a private Worker with the suite Hub (`withNativeWorker`). Its cleanup requires that the Worker process
 *    exited, and it removes the run directory only after that.
 * 3. Create a workspace on the private Worker, and open the agent in a new working directory of its provider.
 * 4. Sign the page in, show the workspace, and require that the Worker reports the shown agent as the opened one, and
 *    as active.
 */
export async function withPrivateNativeWorkspace<Server extends PrivateWorkerHub, Setup>(
  page: Page,
  leapmuxServer: Server,
  options: PrivateNativeWorkspaceOptions<Server, Setup>,
  use: (workspace: PrivateNativeWorkspace<Server, Setup>) => Promise<void>,
): Promise<void> {
  if (!isFileNameComponent(options.prefix))
    throw new Error(`The private workspace prefix must be one file-name component, not ${JSON.stringify(options.prefix)}.`)
  const runDirectory = createTestDirectory(`${options.prefix}-private-`)
  let prepared: PrivateWorkerSetup<Setup>
  try {
    prepared = await options.prepare(runDirectory)
  }
  catch (error) {
    // No Worker exists yet, so no process can still read the partial files.
    rmSync(runDirectory, { recursive: true, force: true })
    throw error
  }
  const hub: Server = prepared.agentEnv === undefined ? leapmuxServer : { ...leapmuxServer, agentEnv: prepared.agentEnv }
  await withNativeWorker(hub, {
    dataDirPrefix: `${options.prefix}-worker`,
    workerName: options.workerName,
    ...(prepared.env === undefined ? {} : { env: prepared.env }),
    privateDirectories: [runDirectory],
  }, async ({ server }) => {
    await withTestWorkspace(server, options.prefix, async (workspace) => {
      const workingDir = newProviderWorkingDir(options.providerAgent, `${options.prefix}-wd-`)
      const agentId = await options.openAgent(server, workspace.workspaceId, workingDir)
      await loginViaToken(page, server.adminToken)
      await openWorkspace(page, workspace.workspaceId)
      const agent = await currentNativeAgent({ page, leapmuxServer: server })
      if (agent.id !== agentId)
        throw new Error(`The page shows agent ${agent.id}, not the agent ${agentId} that the private workspace opened.`)
      await use({ workspaceId: workspace.workspaceId, server, agentId, workingDir, agent, runDirectory, setup: prepared.setup })
    })
  })
}
