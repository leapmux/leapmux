import type { ManagedNativeScenarioContext } from './nativeScenario'
import { join } from 'node:path'
import process from 'node:process'
import { isObject } from '../../../src/lib/jsonPick'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { openAgentViaAPI } from './api'
import { writeMcpFormServer } from './mcpFormServer'
import { withNativeStartupWorker } from './nativeStartupWorker'
import { resolveNativeStartupLaunch } from './nativeStartupWrapper'
import { runNativeToolTurn } from './nativeToolExecution'
import { mcpToolCall } from './providerToolCalls'
import { createTestDirectory } from './runDirectory'
import { openWorkspace } from './ui'
import { expectUnsupportedMcpInput } from './unsupportedMcpInput'

/** Keep the native model configuration while adding one actual local MCP form server. */
export function opencodeMcpFormConfiguration(original: string, command: readonly string[]): string {
  return opencodeMcpServerConfiguration(original, 'form_probe', command)
}

/** Preserve the isolated native configuration while adding one exact local MCP server. */
export function opencodeMcpServerConfiguration(original: string, serverName: string, command: readonly string[]): string {
  const value: unknown = JSON.parse(original)
  if (!isObject(value) || !isObject(value.provider))
    throw new Error('The native MCP limit requires an existing isolated provider configuration.')
  if (command.length === 0 || command.some(part => part.length === 0))
    throw new Error('The native MCP server requires a complete executable command.')
  if (!/^[\w-]+$/.test(serverName))
    throw new Error('The native MCP server requires one exact server name.')
  const mcp = value.mcp === undefined ? {} : value.mcp
  if (!isObject(mcp))
    throw new Error('The native MCP configuration must contain a server object.')
  return JSON.stringify({ ...value, mcp: { ...mcp, [serverName]: { type: 'local', command: [...command] } } })
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
  const directory = createTestDirectory('opencode-family-mcp-limit-')
  const receiptLog = join(directory, 'native-mcp-receipt.json')
  const script = writeMcpFormServer(directory, 'form-server.mjs', { receiptLog })
  const configuration = opencodeMcpFormConfiguration(original, [process.execPath, script])
  await withNativeStartupWorker(context, launch, {
    workerEnvironment: () => ({ [options.configurationVariable]: configuration }),
  }, async (workerId, wrapper) => {
    const privateContext = { ...context, leapmuxServer: { ...context.leapmuxServer, workerId } }
    const server = privateContext.leapmuxServer
    await openAgentViaAPI(server.hubUrl, server.adminToken, workerId, context.workspaceId, directory, { agentProvider: context.provider, ...agentOpenOptions(agentSettings(context.provider)) })
    await wrapper.entry
    await wrapper.release()
    await openWorkspace(context.page, context.workspaceId)
    const callId = 'native-family-mcp-form'
    await expectUnsupportedMcpInput(privateContext, {
      receiptLog,
      callId,
      invoke: async () => (await runNativeToolTurn(privateContext, {
        toolCalls: [mcpToolCall(context.provider, callId, { server: 'form_probe', tool: 'ask', input: {} })],
        prompt: 'Call the actual local form_probe ask tool once.',
        answer: 'The actual native MCP refusal returned to the model.',
      })).resultRequest,
    })
  })
}
