import type { DeepseekHarnessEnvironmentOptions } from '../helpers/deepseekHarnessEnvironment'
import type { McpProbeServer } from '../helpers/mcpProbeServer'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { createDeepseekHarnessEnvironment } from '../helpers/deepseekHarnessEnvironment'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { withNativeWorker } from '../helpers/nativeWorker'
import { createTestDirectory } from '../helpers/runDirectory'
import { tabById } from '../helpers/ui'

/** One private native MCP session: its server, its working directory, and its native preset. */
export interface DeepseekHarnessMcpSetup {
  /** The MCP server that the native profile registers under its own name. */
  server: McpProbeServer
  /** The working directory of the native agent. */
  workingDir: string
  /** The native default preset. An absent value keeps the native standard default. */
  agentPreset?: DeepseekHarnessEnvironmentOptions['agentPreset']
}

/** Give one native MCP session its own profile and Worker. */
export async function withDeepseekHarnessMcp(
  context: ManagedNativeScenarioContext,
  setup: DeepseekHarnessMcpSetup,
  use: (context: ManagedNativeScenarioContext) => Promise<void>,
): Promise<void> {
  const modelURL = context.leapmuxServer.mockModelUrl
  if (!modelURL)
    throw new Error('The DeepSeek Harness MCP scenario requires the private mock URL.')
  const { server } = setup
  const env = createDeepseekHarnessEnvironment({
    runDirectory: createTestDirectory('deepseek-mcp-profile-'),
    modelURL,
    modelKey: MODEL_KEY,
    mcpServers: [{ name: server.name, command: server.command, args: server.args }],
    ...(setup.agentPreset !== undefined ? { agentPreset: setup.agentPreset } : {}),
  })
  await withNativeWorker(context.leapmuxServer, { dataDirPrefix: 'deepseek-mcp-worker', workerName: 'DeepSeek MCP worker', env }, async (worker) => {
    const privateContext = { ...context, leapmuxServer: worker.server }
    const id = await openAgentViaAPI(worker.server.hubUrl, worker.server.adminToken, worker.workerId, context.workspaceId, setup.workingDir, agentOpenOptions(context.provider, {
      optionValues: { permissionMode: 'act', permissions: 'danger-full-access' },
    }))
    await tabById(context.page, id).click()
    await currentNativeAgent(privateContext)
    await use(privateContext)
  })
}
