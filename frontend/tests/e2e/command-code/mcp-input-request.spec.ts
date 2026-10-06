import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { mcpProbeServer } from '../helpers/mcpProbeServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { disposeNativeControlObservation, installNativeControlObservation, readNativeControlObservation } from '../helpers/nativeControlObservation'
import { nativeTextStep } from '../helpers/nativeScenario'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { expectNoControlBanner, sendMessage } from '../helpers/ui'
import { newProviderWorkingDir } from '../helpers/workspace'
import { createMcpCloseControl } from './mcpCloseControl'
import { withCommandCodeMcp } from './mcpScenarios'
import { COMMAND_CODE_AGENT, nativeContext } from './scenarios'

commandCodeTest('ignores the actual unsupported MCP input request and ends its tool after a controlled server close', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const workingDir = newProviderWorkingDir(COMMAND_CODE_AGENT)
  const receipt = join(workingDir, 'native-mcp-form-receipt.json')
  const formServer = writeMcpFormServer(workingDir, 'form-server.mjs', { receiptLog: receipt })
  // The agent starts the close control, which runs the form server until the test closes it.
  const control = createMcpCloseControl(workingDir, formServer.script)
  const server = mcpProbeServer(formServer.name, control.script)
  await withCommandCodeMcp(context, { server, workingDir }, async () => {
    const observation = { id: 'command-code-native-form', testId: 'elicitation-form' }
    await withCleanup(async () => {
      await page.evaluate(installNativeControlObservation, observation)
      const call = mcpToolCall(context.provider, 'native-mcp-form', { server: server.name, tool: 'ask', input: {} })
      const start = await modelScript.queue({ toolCalls: [call] }, nativeTextStep(context, 'The native MCP server close reached the model.'))
      await sendMessage(page, modelScript.prompt('Call the native form probe once.'))
      await modelScript.waitForSteps(start + 1)
      await expect.poll(() => existsSync(receipt) ? readMcpServerReceipt(receipt).elicitationRequests.length : 0).toBe(1)
      const pending = readMcpServerReceipt(receipt)
      expect(pending.initializeCapabilities).not.toHaveProperty('elicitation')
      expect(pending.elicitationReplies).toEqual([])
      expect(pending.toolResults).toEqual([])
      expect(await page.evaluate(readNativeControlObservation, observation.id)).toBe(false)
      await expectNoControlBanner(page)
      control.close()
      await waitForNativeToolSteps(context, start + 2)
      const native = readMcpServerReceipt(receipt)
      expect(native.initializeCapabilities).not.toHaveProperty('elicitation')
      expect(native.elicitationRequests).toEqual(pending.elicitationRequests)
      expect(native.elicitationReplies).toEqual([])
      expect(nativeToolResult(await modelScript.requestAt(start + 1), call.id)).toMatch(/closed|exit|disconnect/i)
      expect(await page.evaluate(readNativeControlObservation, observation.id)).toBe(false)
      await testInfo.attach('command-code-native-mcp-no-reply', { body: JSON.stringify(native), contentType: 'application/json' })
    }, async () => {
      control.close()
      await page.evaluate(disposeNativeControlObservation, observation.id)
    })
  })
})
