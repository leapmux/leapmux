import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { commandCodeTest, createCommandCodeWorkingDir, expect } from '../command-code-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { disposeNativeControlObservation, installNativeControlObservation, readNativeControlObservation } from '../helpers/nativeControlObservation'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { sendMessage } from '../helpers/ui'
import { createMcpCloseControl } from './mcpCloseControl'
import { withCommandCodeMcp } from './mcpScenarios'
import { nativeContext } from './scenarios'

commandCodeTest('ignores the actual unsupported MCP input request and ends its tool after a controlled server close', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const workingDir = createCommandCodeWorkingDir()
  const receipt = join(workingDir, 'native-mcp-form-receipt.json')
  const script = writeMcpFormServer(workingDir, 'form-server.mjs', { receiptLog: receipt })
  const control = createMcpCloseControl(workingDir, script)
  await withCommandCodeMcp(context, { name: 'form_probe', script: control.script, workingDir }, async () => {
    const observation = { id: 'command-code-native-form', testId: 'elicitation-form' }
    await withCleanup(async () => {
      await page.evaluate(installNativeControlObservation, observation)
      const start = (await modelScript.status()).stepCount
      const call = mcpToolCall(context.provider, 'native-mcp-form', { server: 'form_probe', tool: 'ask', input: {} })
      await modelScript.queue({ toolCalls: [call] }, { text: 'The native MCP server close reached the model.' })
      await sendMessage(page, modelScript.prompt('Call the native form probe once.'))
      await modelScript.waitForSteps(start + 1)
      await expect.poll(() => existsSync(receipt) ? readMcpServerReceipt(receipt).elicitationRequests.length : 0).toBe(1)
      const pending = readMcpServerReceipt(receipt)
      expect(pending.initializeCapabilities).not.toHaveProperty('elicitation')
      expect(pending.elicitationReplies).toEqual([])
      expect(pending.toolResults).toEqual([])
      expect(await page.evaluate(readNativeControlObservation, observation.id)).toBe(false)
      await expect(page.locator('[data-testid="control-banner"]:visible')).toHaveCount(0)
      control.close()
      await waitForNativeToolSteps(context, start + 2)
      const status = await modelScript.status()
      const native = readMcpServerReceipt(receipt)
      expect(native.initializeCapabilities).not.toHaveProperty('elicitation')
      expect(native.elicitationRequests).toEqual(pending.elicitationRequests)
      expect(native.elicitationReplies).toEqual([])
      expect(nativeToolResult(status.requests.find(request => request.stepIndex === start + 1), call.id)).toMatch(/closed|exit|disconnect/i)
      expect(await page.evaluate(readNativeControlObservation, observation.id)).toBe(false)
      await testInfo.attach('command-code-native-mcp-no-reply', { body: JSON.stringify(native), contentType: 'application/json' })
    }, async () => {
      control.close()
      await page.evaluate(disposeNativeControlObservation, observation.id)
    })
  })
})
