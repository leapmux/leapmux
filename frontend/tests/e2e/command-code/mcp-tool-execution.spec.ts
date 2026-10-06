import { join } from 'node:path'
import { commandCodeTest, createCommandCodeWorkingDir, expect } from '../command-code-fixtures'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { invokeNativeMcpTool } from '../helpers/mcpExecution'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { messageContents } from '../helpers/ui'
import { withCommandCodeMcp } from './mcpScenarios'
import { nativeContext } from './scenarios'

commandCodeTest('calls the real native MCP server and uses its result in the next model request', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const workingDir = createCommandCodeWorkingDir()
  const receipt = join(workingDir, 'native-mcp-receipt.json')
  const server = writeMcpEchoServer(workingDir, { receiptLog: receipt })
  await withCommandCodeMcp(context, { server, workingDir }, async () => {
    const value = `native-computed-${40 + 2}`
    const callId = 'native-mcp-echo'
    const request = await invokeNativeMcpTool(context, { server: server.name, tool: 'echo', callId, input: { value } })
    const native = readMcpServerReceipt(receipt)
    expect(native.initializeCapabilities).not.toBeNull()
    expect(native.toolResults).toHaveLength(1)
    expect(native.toolResults[0]?.text).toBe(`MCP_ECHO:${value}`)
    expect(nativeToolResult(request, callId)).toContain(native.toolResults[0]!.text)
    await expect(messageContents(page).filter({ hasText: native.toolResults[0]!.text }).first()).toBeVisible()
    await page.reload()
    await expect(messageContents(page).filter({ hasText: native.toolResults[0]!.text }).first()).toBeVisible()
  })
})
