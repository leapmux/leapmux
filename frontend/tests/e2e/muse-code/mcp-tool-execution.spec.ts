/**
 * The model calls a Model Context Protocol tool through Muse, and its result
 * reaches the next native model request.
 *
 * A live MCP server in the shared Muse settings blocks the host's asynchronous
 * compaction path, so this spec registers the echo server for its own agent and
 * restores the settings afterwards.
 */
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { newNativeWorkingDir, openNativeAgent } from '../helpers/nativeAgentOpen'
import { withNativeConfigurationFile } from '../helpers/nativeConfigurationFile'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolResultContent } from '../helpers/nativeToolResult'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { getGlobalState } from '../helpers/server'
import { applyPermissionPreset } from '../helpers/ui'
import { museTest } from '../muse-fixtures'
import { nativeContext } from './scenarios'

/** The private Muse settings of the run. */
function museSettingsPath(home: string | undefined): string {
  if (!home)
    throw new Error('The Muse MCP proof requires the isolated native home.')
  return join(home, '.config', 'muse', 'settings.json')
}

/** Register the echo server in the shared settings, open a fresh agent under it, and restore. */
async function withMuseEchoAgent(native: Awaited<ReturnType<typeof nativeContext>>, use: () => Promise<void>): Promise<void> {
  const settingsPath = museSettingsPath(native.leapmuxServer.agentEnv?.HOME)
  const work = newNativeWorkingDir(native, 'muse-echo-agent-')
  const server = writeMcpEchoServer(work, { receiptLog: join(work, 'echo-receipt.json') })
  const settings: unknown = JSON.parse(readFileSync(settingsPath, 'utf8'))
  if (typeof settings !== 'object' || settings === null || !('mcpServers' in settings))
    throw new Error('The private Muse settings hold no MCP server table.')
  const servers = (settings as { mcpServers: Record<string, unknown> }).mcpServers
  const content = JSON.stringify({ ...(settings as object), mcpServers: { ...servers, [server.name]: { command: server.command, args: [...server.args] } } })
  await withNativeConfigurationFile({ path: settingsPath, content, runDir: getGlobalState().tmpDir }, async () => {
    await openNativeAgent(native, { workingDir: work })
    await applyPermissionPreset(native.page, 'bypass')
    await use()
  })
}

museTest('executes a real MCP echo through the native Muse model protocol', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const native = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await withMuseEchoAgent(native, async () => {
    await exerciseMcpEcho(native, 'muse')
  })
})

museTest('preserves the exact native MCP call identity and result', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const native = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await withMuseEchoAgent(native, async () => {
    const callId = 'muse-mcp-identity'
    const { resultRequest } = await runNativeToolTurn(native, {
      toolCalls: [mcpToolCall(native.provider, callId, { server: 'echo_probe', tool: 'echo', input: { value: 'MUSE_MCP_IDENTITY' } })],
      prompt: 'Call the echo server once with the identity value.',
      answer: 'The native MCP echo returned its identity value.',
      permissions: 'none',
    })
    expect(String(nativeToolResultContent(resultRequest, callId))).toContain('MCP_ECHO:MUSE_MCP_IDENTITY')
  })
})
