/** Native v2 MCP configuration and browser form acceptance. */
import type { McpProbeServer } from '../helpers/mcpProbeServer'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { exerciseMcpProbeFormRoundTrip } from '../helpers/mcpProbeForm'
import { assertMcpServerName } from '../helpers/mcpProbeServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { newNativeWorkingDir } from '../helpers/nativeAgentOpen'
import { withNativeStartupWorker } from '../helpers/nativeStartupWorker'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { applyPermissionPreset, openWorkspace } from '../helpers/ui'
import { nativeContext } from './scenarios'

/** Keep the isolated native configuration, and add one local MCP server under its own name. */
export function opencodeMcpServerConfiguration(original: string, server: McpProbeServer): string {
  assertMcpServerName(server.name)
  const value: unknown = JSON.parse(original)
  if (!isObject(value) || !isObject(value.providers))
    throw new Error('The native MCP form requires an existing isolated provider configuration.')
  const command = [server.command, ...server.args]
  if (command.some(part => part.length === 0))
    throw new Error('The native MCP server requires a complete executable command.')
  const mcp = value.mcp === undefined
    ? {}
    : value.mcp
  if (!isObject(mcp))
    throw new Error('The native MCP configuration must contain a server object.')
  const servers = mcp.servers ?? {}
  if (!isObject(servers))
    throw new Error('The native MCP configuration must contain a servers object.')
  return JSON.stringify({
    ...value,
    mcp: {
      ...mcp,
      servers: {
        ...servers,
        [server.name]: { type: 'local', command },
      },
    },
  })
}

/** Answer the native MCP form through a private Worker profile and verify its receipt. */
export async function exerciseOpenCodeMcpInput(
  context: ManagedNativeScenarioContext,
  options: { binaryName: string, configurationVariable: string },
): Promise<void> {
  const original = context.leapmuxServer.agentEnv?.[options.configurationVariable]
  if (!original)
    throw new Error('The native MCP form requires its isolated inline configuration.')
  const launch = resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, {
    binaryName: options.binaryName,
    holdWhen: ['serve'],
  })
  // The agent works in the directory of its form server, which follows the rule of the provider of the context.
  const directory = newNativeWorkingDir(context, 'opencode-native-mcp-form-')
  const receiptLog = join(directory, 'native-mcp-receipt.json')
  const server = writeMcpFormServer(directory, 'form-server.mjs', { receiptLog })
  const configuration = opencodeMcpServerConfiguration(original, server)
  await withNativeStartupWorker(context, launch, {
    workerEnvironment: () => ({ [options.configurationVariable]: configuration }),
  }, async (workerId, wrapper) => {
    const privateContext = await nativeContext({
      ...context,
      leapmuxServer: { ...context.leapmuxServer, workerId },
    })
    const privateServer = privateContext.leapmuxServer
    await openAgentViaAPI({ hubUrl: privateServer.hubUrl, adminToken: privateServer.adminToken, workerId }, context.workspaceId, directory, agentOpenOptions(context.provider))
    await wrapper.entry
    await wrapper.release()
    await openWorkspace(context.page, context.workspaceId)
    const callId = 'native-opencode-mcp-form'
    await applyPermissionPreset(context.page, 'bypass')
    await exerciseMcpProbeFormRoundTrip(privateContext, { callId, reloadBeforeSubmit: true })
    const receipt = readMcpServerReceipt(receiptLog)
    expect(receipt.elicitationReplies).toHaveLength(1)
    expect(receipt.elicitationReplies[0]).toMatchObject({
      kind: 'result',
      result: {
        action: 'accept',
        content: { count: 0, enabled: false, color: 'b' },
      },
    })
  })
}
