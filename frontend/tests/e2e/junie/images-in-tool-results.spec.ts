import { AgentProvider, ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { decompressContentToString } from '../../../src/lib/decompress'
import { getTestChannel } from '../helpers/api'
import { writeJunieMcpConfig } from '../helpers/junieMcp'
import { writeMcpImageServer } from '../helpers/mcpImageServer'
import { junieAnswerToolCall, mcpToolCall } from '../helpers/providerToolCalls'
import { writeToolImage } from '../helpers/toolImages'
import { openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { JUNIE_AGENT, expect as junieExpect, junieTest } from '../junie-fixtures'

junieTest.describe('Junie images in tool results', () => {
  junieTest('receives no image bytes in the ACP tool row after the model sees the PNG', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }, testInfo) => {
    let imageName = ''
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, JUNIE_AGENT, { optionValues: { brave_mode: 'on' }, prepare: (workingDir) => {
      imageName = writeToolImage(workingDir, 'junie-mcp')
      const server = writeMcpImageServer(workingDir, imageName)
      writeJunieMcpConfig(workingDir, 'image_probe', server.command, server.args)
    } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await modelScript.rule(
      { name: 'junie-image-capability', when: { system: 'capability filter agent' }, respond: { text: '1' } },
      { name: 'junie-image-task-name', when: { system: 'task description summarizer' }, respond: { text: 'MCP image task' } },
    )
    await modelScript.queue(
      { toolCalls: [mcpToolCall(AgentProvider.JUNIE, 'junie-mcp-image', { server: 'image_probe', tool: 'show', input: {} })] },
      { toolCalls: [junieAnswerToolCall('junie-image-answer', 'The MCP image is ready.')] },
    )
    await sendMessage(page, modelScript.prompt('Call the image_probe show tool once.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    junieExpect(JSON.stringify(status.requests.find(request => request.stepIndex === 1)?.body)).toContain('iVBORw0KGgo')
    const agentId = await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().getAttribute('data-tab-id') ?? ''
    const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    const transcript = await channel.callWorker(leapmuxServer.workerId, 'ListAgentMessages', ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, { agentId, limit: 200 })
    const rows = transcript.messages.map(message => ({
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
    }).find(row => row?.sessionUpdate === 'tool_call_update'
      && row.title === 'image_probe/show'
      && row.status === 'completed'
      && row._meta?.is_mcp_tool_call === true)
    junieExpect(completed).toBeDefined()
    junieExpect(completed?.content).toEqual([])
    await junieExpect(page.locator('[data-chat-scroll-container="true"]:visible button[aria-label="Open image"]')).toHaveCount(0)
  })
})
