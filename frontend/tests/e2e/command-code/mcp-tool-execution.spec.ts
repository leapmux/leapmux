import { join } from 'node:path'
import { commandCodeTest, createCommandCodeWorkingDir, expect } from '../command-code-fixtures'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { messageContents, sendMessage } from '../helpers/ui'
import { withCommandCodeMcp } from './mcpScenarios'
import { nativeContext } from './scenarios'

commandCodeTest('calls the real native MCP server and uses its result in the next model request', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const workingDir = createCommandCodeWorkingDir()
  const receipt = join(workingDir, 'native-mcp-receipt.json')
  const script = writeMcpEchoServer(workingDir, { receiptLog: receipt })
  await withCommandCodeMcp(context, { name: 'echo_probe', script, workingDir }, async () => {
    const start = (await modelScript.status()).stepCount
    const value = `native-computed-${40 + 2}`
    const call = mcpToolCall(context.provider, 'native-mcp-echo', { server: 'echo_probe', tool: 'echo', input: { value } })
    await modelScript.queue({ toolCalls: [call] }, { text: 'The native MCP result reached the model.' })
    await sendMessage(page, modelScript.prompt('Call the supplied native MCP tool.'))
    await waitForNativeToolSteps(context, start + 2)
    const status = await modelScript.status()
    const native = readMcpServerReceipt(receipt)
    expect(native.initializeCapabilities).not.toBeNull()
    expect(native.toolResults).toHaveLength(1)
    expect(native.toolResults[0]?.text).toBe(`MCP_ECHO:${value}`)
    expect(nativeToolResult(status.requests.find(request => request.stepIndex === start + 1), call.id)).toContain(native.toolResults[0]!.text)
    await expect(messageContents(page).filter({ hasText: native.toolResults[0]!.text }).first()).toBeVisible()
    await page.reload()
    await expect(messageContents(page).filter({ hasText: native.toolResults[0]!.text }).first()).toBeVisible()
  })
})
