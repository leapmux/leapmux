import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { openCommandCodeAgent } from '../command-code-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { getGlobalState } from '../helpers/server'
import { openWorkspace } from '../helpers/ui'
import { closeAgentViaAPI } from '../helpers/worktree'

/** Install one native MCP server before its real provider session starts. */
export async function withCommandCodeMcp(context: ManagedNativeScenarioContext, options: { name: string, script: string, workingDir: string }, run: () => Promise<void>): Promise<void> {
  const home = context.leapmuxServer.agentEnv?.HOME
  if (!home || !/^[\w-]+$/.test(options.name))
    throw new Error('The native Command Code MCP proof requires a private home and server name.')
  const path = join(home, '.commandcode/mcp.json')
  assertPrivateNativePath(path, getGlobalState().tmpDir)
  assertPrivateNativePath(options.script, getGlobalState().tmpDir)
  const original = readFileSync(path)
  const config: unknown = JSON.parse(original.toString('utf8'))
  if (!isObject(config) || !isObject(config.mcpServers))
    throw new Error('The private native MCP config has an invalid server table.')
  const servers = config.mcpServers
  let agentID: string | undefined
  await withCleanup(async () => {
    writeFileSync(path, JSON.stringify({ ...config, mcpServers: { ...servers, [options.name]: { command: process.execPath, args: [options.script] } } }), { mode: 0o600 })
    const agent = await openCommandCodeAgent(context.leapmuxServer, context.workspaceId, { permissionMode: 'bypass' }, options.workingDir)
    agentID = agent.agentId
    await openWorkspace(context.page, context.workspaceId)
    await run()
  }, async () => {
    try {
      if (agentID) {
        const close = await closeAgentViaAPI(context.leapmuxServer.hubUrl, context.leapmuxServer.adminToken, context.leapmuxServer.workerId, agentID)
        expect(close.failureMessage).toBe('')
      }
    }
    finally {
      writeFileSync(path, original, { mode: 0o600 })
    }
  })
}
