/**
 * A Model Context Protocol server's elicitation request reaches no native Muse input
 * route. The binary holds no elicitation support at all: its MCP handshake requests
 * no elicitation capability, the pending tool call raises no control banner, and the
 * server's request stays unanswered until the turn is interrupted.
 */
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { invokeNativeMcpTool, withNativeMcpFormAgent } from '../helpers/mcpExecution'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { interruptButton, waitForAgentIdle } from '../helpers/ui'
import { museTest } from '../muse-fixtures'
import { nativeContext } from './scenarios'

/** The private Muse settings of the run; a probe server registers beside the echo. */
function museSettingsPath(server: { leapmuxServer: { agentEnv?: { HOME?: string } } }): string {
  const home = server.leapmuxServer.agentEnv?.HOME
  if (!home)
    throw new Error('The Muse MCP input proof requires the isolated native home.')
  return join(home, '.config', 'muse', 'settings.json')
}

museTest('proves the absent native MCP elicitation route without a browser form', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  // The elicitation never resolves, so the turn ends interrupted and its queued
  // answer stays unconsumed on purpose.
  modelScript.allowUnconsumed('the absent native elicitation route holds the turn until the interrupt')
  const native = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const settingsPath = museSettingsPath(native)
  await withNativeMcpFormAgent(native, {
    directoryPrefix: 'muse-native-mcp-form-',
    configurationPath: settingsPath,
    configuration: (server) => {
      const settings: unknown = JSON.parse(readFileSync(settingsPath, 'utf8'))
      if (typeof settings !== 'object' || settings === null || !('mcpServers' in settings))
        throw new Error('The private Muse settings hold no MCP server table.')
      const servers = (settings as { mcpServers: Record<string, unknown> }).mcpServers
      return { ...(settings as object), mcpServers: { ...servers, [server.name]: { command: server.command, args: [...server.args] } } }
    },
  }, async ({ server, receiptLog }) => {
    const callId = 'muse-native-elicitation'
    await expectNoNativeControl(native, {
      testId: 'elicitation-form',
      additionalTestIds: ['control-banner'],
      relatedProof: async () => {
        const pending = invokeNativeMcpTool(native, { server: server.name, tool: 'ask', callId, input: {} })
        // The elicitation never resolves: muse holds no route to answer it. Wait
        // for the server to have SEEN its own request, then interrupt the turn.
        await expect.poll(async () => readMcpServerReceipt(receiptLog).elicitationRequests.length, { timeout: 30000 }).toBeGreaterThan(0)
        await interruptButton(native.page).click()
        await waitForAgentIdle(native.page)
        // The interrupt ends the turn before any tool result lands: the call's wait rejects still waiting.
        await expect(pending).rejects.toThrow(/exceeded while waiting on the predicate/)
      },
    })
    const receipt = readMcpServerReceipt(receiptLog)
    expect(receipt.initializeCapabilities, 'the native MCP handshake requests no elicitation capability').not.toHaveProperty('elicitation')
    expect(receipt.elicitationReplies).toEqual([])
    await native.page.reload()
    await waitForAgentIdle(native.page)
    await expect(native.page.getByTestId('elicitation-form')).toHaveCount(0)
  })
})
