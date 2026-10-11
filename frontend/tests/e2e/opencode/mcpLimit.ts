/**
 * The MCP scenarios of the OpenCode family. Kilo builds on OpenCode and reads the same inline configuration, so
 * both providers use this module with their own binary and configuration variable.
 */
import type { McpProbeServer } from '../helpers/mcpProbeServer'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { join } from 'node:path'
import { isObject } from '../../../src/lib/jsonPick'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { invokeNativeMcpTool } from '../helpers/mcpExecution'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { assertMcpServerName } from '../helpers/mcpProbeServer'
import { newNativeWorkingDir } from '../helpers/nativeAgentOpen'
import { withNativeStartupWorker } from '../helpers/nativeStartupWorker'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { openWorkspace } from '../helpers/ui'
import { expectUnsupportedMcpInput } from '../helpers/unsupportedMcpInput'

/** Keep the isolated native configuration, and add one local MCP server under its own name. */
export function opencodeMcpServerConfiguration(original: string, server: McpProbeServer): string {
  assertMcpServerName(server.name)
  const value: unknown = JSON.parse(original)
  if (!isObject(value) || !isObject(value.provider))
    throw new Error('The native MCP limit requires an existing isolated provider configuration.')
  const command = [server.command, ...server.args]
  if (command.some(part => part.length === 0))
    throw new Error('The native MCP server requires a complete executable command.')
  const mcp = value.mcp === undefined ? {} : value.mcp
  if (!isObject(mcp))
    throw new Error('The native MCP configuration must contain a server object.')
  return JSON.stringify({ ...value, mcp: { ...mcp, [server.name]: { type: 'local', command } } })
}

/** Run the actual OpenCode-family MCP refusal through a private Worker profile. */
export async function exerciseOpencodeMcpInputLimit(
  context: ManagedNativeScenarioContext,
  options: { binaryName: string, configurationVariable: string },
): Promise<void> {
  const original = context.leapmuxServer.agentEnv?.[options.configurationVariable]
  if (!original)
    throw new Error('The native MCP limit requires its isolated inline configuration.')
  const launch = resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: options.binaryName, holdWhen: ['acp'] })
  // The agent works in the directory of its form server, which follows the rule of the provider of the context.
  const directory = newNativeWorkingDir(context, 'opencode-family-mcp-limit-')
  const receiptLog = join(directory, 'native-mcp-receipt.json')
  const server = writeMcpFormServer(directory, 'form-server.mjs', { receiptLog })
  const configuration = opencodeMcpServerConfiguration(original, server)
  await withNativeStartupWorker(context, launch, {
    workerEnvironment: () => ({ [options.configurationVariable]: configuration }),
  }, async (workerId, wrapper) => {
    const privateContext = { ...context, leapmuxServer: { ...context.leapmuxServer, workerId } }
    const privateServer = privateContext.leapmuxServer
    await openAgentViaAPI({ hubUrl: privateServer.hubUrl, adminToken: privateServer.adminToken, workerId }, context.workspaceId, directory, agentOpenOptions(context.provider))
    await wrapper.entry
    await wrapper.release()
    await openWorkspace(context.page, context.workspaceId)
    const callId = 'native-family-mcp-form'
    await expectUnsupportedMcpInput(privateContext, {
      receiptLog,
      callId,
      invoke: () => invokeNativeMcpTool(privateContext, { server: server.name, tool: 'ask', callId, input: {} }),
    })
  })
}
