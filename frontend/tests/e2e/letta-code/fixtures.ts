import type { ServerInfo } from '../fixtures'
import type { McpProbeServer } from '../helpers/mcpProbeServer'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { PrivateWorkerSetup } from '../helpers/privateNativeWorkspace'
import type { WorkspaceFixture } from '../helpers/workspace'
import type { LettaMcpServer } from './mcpConfiguration'
import { expect } from '@playwright/test'
import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { LETTA_MODE } from '../../../src/generated/contracts/letta-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { requireBinary } from '../helpers/binaryOnPath'
import { withCleanup } from '../helpers/cleanup'
import { createMockAgentEnvironment } from '../helpers/mockAgentEnvironment'
import { currentNativeAgent, nativeOptionValue } from '../helpers/nativeScenario'
import { withPrivateNativeWorkspace } from '../helpers/privateNativeWorkspace'
import { tabById, waitForSettingsHydrated } from '../helpers/ui'
import { closeNativeAgentAndWait } from '../helpers/workerTabs'
import { lettaTest } from '../letta-fixtures'
import { configureLettaMcp } from './mcpConfiguration'
import { LETTA_AGENT } from './scenarios'

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
  return openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, workingDir, {
    ...agentOpenOptions(AgentProvider.LETTA, { optionValues: { [OPTION_ID_PERMISSION_MODE]: LETTA_MODE.Unrestricted } }),
    ...(agentSessionId !== undefined ? { agentSessionId } : {}),
  })
}

/** What the private files of the Letta MCP Worker hold. */
interface LettaMcpFiles {
  home: string
  backendDirectory: string
  nodeExecutable: string
}

/** Write a private mock agent environment with a local Letta backend into `runDirectory`, for the Letta MCP Worker. */
export async function prepareMcpLetta(runDirectory: string, mockModelUrl: string): Promise<PrivateWorkerSetup<LettaMcpFiles>> {
  const environment = await createMockAgentEnvironment(runDirectory, mockModelUrl)
  const nodeExecutable = requireBinary('node', 'The private Letta MCP fixture requires the Node executable', environment.env)
  const backendDirectory = environment.env.LETTA_LOCAL_BACKEND_DIR
  if (!backendDirectory)
    throw new Error('The private Letta MCP fixture requires a local backend directory.')
  return { agentEnv: environment.env, setup: { home: environment.homeDir, backendDirectory, nodeExecutable } }
}

/** Keep native MCP settings and the native backend inside one private Worker environment. */
export const mcpLettaTest = lettaTest.extend<{ privateMcpLettaWorkspace: PrivateMcpLettaWorkspace }>({
  privateMcpLettaWorkspace: async ({ page, leapmuxServer }, use) => {
    await withPrivateNativeWorkspace(page, leapmuxServer, {
      prefix: 'letta-mcp',
      workerName: 'Letta MCP test',
      providerAgent: LETTA_AGENT,
      prepare: runDirectory => prepareMcpLetta(runDirectory, leapmuxServer.mockModelUrl),
      openAgent: openMcpLettaAgent,
    }, async ({ workspaceId, server, workingDir, agent, runDirectory, setup }) => {
      expect(agent.agentProvider).toBe(AgentProvider.LETTA)
      expect(agent.agentSessionId).not.toBe('')
      expect(nativeOptionValue(agent, OPTION_ID_PERMISSION_MODE)).toBe(LETTA_MODE.Unrestricted)
      await use({ workspaceId, server, runDirectory, workingDir, ...setup })
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

/**
 * Reopen the same native conversation after its MCP settings register `probes`.
 * Each server starts through the Node executable of the private workspace, under the name that the server states.
 */
export async function withRegisteredLettaMcp(
  context: ManagedNativeScenarioContext,
  workspace: PrivateMcpLettaWorkspace,
  probes: readonly McpProbeServer[],
  use: (identity: { agentId: string, conversationId: string }) => Promise<void>,
): Promise<void> {
  const servers = probes.map((probe): LettaMcpServer => ({ name: probe.name, transport: 'stdio', command: workspace.nodeExecutable, args: [...probe.args] }))
  const before = await currentNativeAgent(context)
  expect(before.agentProvider).toBe(AgentProvider.LETTA)
  expect(before.agentSessionId).not.toBe('')
  const server = workspace.server
  // The private Worker holds the agent, so the close and its wait both go there.
  const close = (agentId: string) => closeNativeAgentAndWait({ leapmuxServer: server }, agentId)
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
    expect(nativeOptionValue(applied, OPTION_ID_PERMISSION_MODE)).toBe(LETTA_MODE.Unrestricted)
    await use(configuration.identity)
  }, () => cleanupRegisteredLettaMcp({ agentId: reopened, close, restore: configuration.restore }))
}
