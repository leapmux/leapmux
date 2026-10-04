import type { ServerInfo } from '../fixtures'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { WorkspaceFixture } from '../helpers/workspace'
import type { LettaMcpServer } from './mcpConfiguration'
import { rmSync } from 'node:fs'
import { expect } from '@playwright/test'
import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { LETTA_MODE } from '../../../src/generated/contracts/letta-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { findBinary } from '../helpers/binaryOnPath'
import { withCleanup } from '../helpers/cleanup'
import { createMockAgentEnvironment } from '../helpers/mockAgentEnvironment'
import { currentNativeAgent, nativeAgentById } from '../helpers/nativeScenario'
import { withNativeWorker } from '../helpers/nativeWorker'
import { isAlive } from '../helpers/processTree'
import { createTestDirectory } from '../helpers/runDirectory'
import { loginViaToken, openWorkspace, tabById, waitForSettingsHydrated } from '../helpers/ui'
import { withTestWorkspace } from '../helpers/workspace'
import { closeAgentViaAPI } from '../helpers/worktree'
import { lettaTest } from '../letta-fixtures'
import { configureLettaMcp } from './mcpConfiguration'

export interface PrivateMcpLettaWorkspace extends WorkspaceFixture {
  server: ServerInfo
  runDirectory: string
  home: string
  backendDirectory: string
  nodeExecutable: string
  workingDir: string
}

/** Open the actual initial or resumed agent for the private MCP fixture. */
export function openMcpLettaAgent(
  server: Pick<ServerInfo, 'hubUrl' | 'adminToken' | 'workerId'>,
  workspaceId: string,
  workingDir: string,
  agentSessionId?: string,
): Promise<string> {
  const defaults = agentOpenOptions(agentSettings(AgentProvider.LETTA))
  return openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, workingDir, {
    agentProvider: AgentProvider.LETTA,
    ...defaults,
    optionValues: { ...defaults.optionValues, [OPTION_ID_PERMISSION_MODE]: LETTA_MODE.Unrestricted },
    ...(agentSessionId !== undefined ? { agentSessionId } : {}),
  })
}

/** Keep native MCP settings and the native backend inside one private Worker environment. */
export const mcpLettaTest = lettaTest.extend<{ privateMcpLettaWorkspace: PrivateMcpLettaWorkspace }>({
  privateMcpLettaWorkspace: async ({ page, leapmuxServer }, use) => {
    const runDirectory = createTestDirectory('letta-mcp-private-')
    let workerAttempted = false
    let workerStopped = false
    await withCleanup(async () => {
      const environment = await createMockAgentEnvironment(runDirectory, leapmuxServer.mockModelUrl)
      const nodeExecutable = findBinary('node', environment.env)
      const backendDirectory = environment.env.LETTA_LOCAL_BACKEND_DIR
      if (!nodeExecutable || !backendDirectory)
        throw new Error('The private Letta MCP fixture requires Node and a local backend directory.')
      workerAttempted = true
      await withNativeWorker({ ...leapmuxServer, agentEnv: environment.env }, {
        dataDirPrefix: 'letta-mcp-worker',
        workerName: 'Letta MCP test',
        afterStop: (worker) => {
          if (worker.pid && isAlive(worker.pid))
            throw new Error('The private Letta MCP Worker did not physically exit.')
          workerStopped = true
        },
      }, async ({ server }) => {
        await withTestWorkspace(server, 'letta-mcp-private', async (workspace) => {
          const workingDir = createTestDirectory('letta-mcp-native-wd-')
          const agentId = await openMcpLettaAgent(server, workspace.workspaceId, workingDir)
          await loginViaToken(page, server.adminToken)
          await openWorkspace(page, workspace.workspaceId)
          const applied = await currentNativeAgent({ page, leapmuxServer: server })
          expect(applied.id).toBe(agentId)
          expect(applied.agentProvider).toBe(AgentProvider.LETTA)
          expect(applied.agentSessionId).not.toBe('')
          expect(applied.optionGroups.find(group => group.id === OPTION_ID_PERMISSION_MODE)?.currentValue).toBe(LETTA_MODE.Unrestricted)
          await use({ ...workspace, server, runDirectory, home: environment.homeDir, backendDirectory, nodeExecutable, workingDir })
        })
      })
    }, async () => {
      if (!workerAttempted || workerStopped)
        rmSync(runDirectory, { recursive: true, force: true })
    })
  },
})

/** Release the resumed agent and restore its native MCP registration. */
export async function cleanupRegisteredLettaMcp(options: {
  agentId: string
  close: (agentId: string) => Promise<void>
  restore: () => void
}): Promise<void> {
  await withCleanup(async () => {
    if (options.agentId)
      await options.close(options.agentId)
  }, async () => options.restore())
}

/** Reopen the same native conversation after its MCP settings change. */
export async function withRegisteredLettaMcp(
  context: ManagedNativeScenarioContext,
  workspace: PrivateMcpLettaWorkspace,
  servers: readonly LettaMcpServer[],
  use: (identity: { agentId: string, conversationId: string }) => Promise<void>,
): Promise<void> {
  const before = await currentNativeAgent(context)
  expect(before.agentProvider).toBe(AgentProvider.LETTA)
  expect(before.agentSessionId).not.toBe('')
  const server = workspace.server
  const close = async (agentId: string) => {
    const closed = await closeAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, agentId)
    expect(closed.failureMessage).toBe('')
    await expect.poll(() => nativeAgentById(context, agentId)).toBeNull()
  }
  await close(before.id)
  const configuration = configureLettaMcp({ ...workspace, conversationId: before.agentSessionId, servers })
  let reopened = ''
  await withCleanup(async () => {
    reopened = await openMcpLettaAgent(server, context.workspaceId, before.workingDir, before.agentSessionId)
    expect(reopened).not.toBe(before.id)
    await tabById(context.page, reopened).click()
    await waitForSettingsHydrated(context.page)
    const applied = await currentNativeAgent(context)
    expect(applied.agentSessionId).toBe(before.agentSessionId)
    expect(applied.optionGroups.find(group => group.id === OPTION_ID_PERMISSION_MODE)?.currentValue).toBe(LETTA_MODE.Unrestricted)
    await use(configuration.identity)
  }, () => cleanupRegisteredLettaMcp({ agentId: reopened, close, restore: configuration.restore }))
}
