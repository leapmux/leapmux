import type { DeepseekHarnessEnvironmentOptions } from '../helpers/deepseekHarnessEnvironment'
import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import process from 'node:process'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { createDeepseekHarnessEnvironment } from '../helpers/deepseekHarnessEnvironment'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { withNativeWorker } from '../helpers/nativeWorker'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { tabById } from '../helpers/ui'

/** One private native MCP session: its server, its working directory, and its native preset. */
export interface DeepseekHarnessMcpSetup {
  /** The MCP server name that the native profile registers. */
  name: string
  /** The existing MCP server script that Node runs. */
  script: string
  /** The working directory of the native agent. */
  workingDir: string
  /** The native default preset. An absent value keeps the native standard default. */
  agentPreset?: DeepseekHarnessEnvironmentOptions['agentPreset']
}

/** Give one native MCP session its own profile and Worker. */
export async function withDeepseekHarnessMcp(
  context: ManagedNativeScenarioContext,
  server: DeepseekHarnessMcpSetup,
  use: (context: ManagedNativeScenarioContext) => Promise<void>,
): Promise<void> {
  const modelURL = context.leapmuxServer.mockModelUrl
  if (!modelURL)
    throw new Error('The DeepSeek Harness MCP scenario requires the private mock URL.')
  const env = createDeepseekHarnessEnvironment({
    runDirectory: createTestDirectory('deepseek-mcp-profile-'),
    modelURL,
    modelKey: MODEL_KEY,
    mcpServers: [{ name: server.name, command: process.execPath, args: [server.script] }],
    ...(server.agentPreset !== undefined ? { agentPreset: server.agentPreset } : {}),
  })
  await withNativeWorker(context.leapmuxServer, { dataDirPrefix: 'deepseek-mcp-worker', workerName: 'DeepSeek MCP worker', env }, async (worker) => {
    const privateContext = { ...context, leapmuxServer: worker.server }
    const id = await openAgentViaAPI(worker.server.hubUrl, worker.server.adminToken, worker.workerId, context.workspaceId, server.workingDir, {
      agentProvider: AgentProvider.DEEPSEEK_HARNESS,
      ...agentOpenOptions(agentSettings(AgentProvider.DEEPSEEK_HARNESS)),
      optionValues: { permissionMode: 'act', permissions: 'danger-full-access' },
    })
    await tabById(context.page, id).click()
    await currentNativeAgent(privateContext)
    await use(privateContext)
  })
}

export async function invokeDeepseekHarnessMcp(
  context: ManagedNativeScenarioContext,
  options: { server: string, tool: string, callId: string, input: Record<string, unknown> },
): Promise<MockModelRequestRecord> {
  const turn = await runNativeToolTurn(context, {
    toolCalls: [mcpToolCall(context.provider, options.callId, options)],
    prompt: 'Run the exact scripted native MCP operation.',
    answer: 'The native MCP operation completed.',
  })
  return turn.resultRequest
}
