import { ACP_UPDATE } from '../../../src/generated/contracts/acp-protocol'
import { decompressContentToString } from '../../../src/lib/decompress'
import { MCP_IMAGE_SERVER_NAME, writeMcpImageServer } from '../helpers/mcpImageServer'
import { readAllAgentMessages } from '../helpers/nativeMessages'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { expectPngInRequest, writeToolImage } from '../helpers/toolImages'
import { chatScrollContainer, openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { expect as junieExpect, junieTest } from '../junie-fixtures'
import { junieCapabilityAnswer } from './housekeeping'
import { writeJunieMcpConfig } from './mcpConfig'
import { JUNIE_AGENT, nativeContext } from './scenarios'

junieTest.describe('Junie images in tool results', () => {
  junieTest('receives no image bytes in the ACP tool row after the model sees the PNG', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }, testInfo) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    let imageName = ''
    await openProviderAgent(leapmuxServer, context.workspaceId, JUNIE_AGENT, { optionValues: { brave_mode: 'on' }, prepare: (workingDir) => {
      imageName = writeToolImage(workingDir, 'junie-mcp')
      const server = writeMcpImageServer(workingDir, imageName)
      writeJunieMcpConfig(workingDir, server.name, server.command, server.args)
    } })
    await openWorkspace(page, context.workspaceId)
    await modelScript.rule(junieCapabilityAnswer('junie-image-capability', '1'))
    const { resultRequest } = await runNativeToolTurn(context, {
      toolCalls: [mcpToolCall(context.provider, 'junie-mcp-image', { server: MCP_IMAGE_SERVER_NAME, tool: 'show', input: {} })],
      prompt: `Call the ${MCP_IMAGE_SERVER_NAME} show tool once.`,
      answer: 'The MCP image is ready.',
    })
    expectPngInRequest(resultRequest)
    const messages = await readAllAgentMessages(context, await selectedAgentTabId(page))
    const rows = messages.map(message => ({
      spanType: message.spanType,
      content: decompressContentToString(message.content, message.contentCompression),
    }))
    await testInfo.attach('junie-mcp-image-worker-rows', { body: JSON.stringify(rows, null, 2), contentType: 'application/json' })
    const completed = rows.map((row) => {
      if (!row.content)
        return null
      return JSON.parse(row.content) as {
        sessionUpdate?: string
        title?: string
        status?: string
        content?: unknown[]
        _meta?: { is_mcp_tool_call?: boolean }
      }
    }).find(row => row?.sessionUpdate === ACP_UPDATE.ToolCallUpdate
      && row.title === `${MCP_IMAGE_SERVER_NAME}/show`
      && row.status === 'completed'
      && row._meta?.is_mcp_tool_call === true)
    junieExpect(completed).toBeDefined()
    junieExpect(completed?.content).toEqual([])
    await junieExpect(chatScrollContainer(page).locator('button[aria-label="Open image"]')).toHaveCount(0)
  })
})
