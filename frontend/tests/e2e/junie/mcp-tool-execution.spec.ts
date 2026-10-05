import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { writeJunieMcpConfig } from '../helpers/junieMcp'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { junieAnswerToolCall, mcpToolCall } from '../helpers/providerToolCalls'
import { openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expect, JUNIE_E2E_SKIP_REASON, junieTest, openJunieAgent } from '../junie-fixtures'

junieTest.describe('native mcp tool execution', () => {
  junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

  junieTest('runs a project MCP tool through the native agent', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const echoArguments = { query: 'probe', limit: 0, tail: 'END_MCP_ARGUMENTS' }
    await openJunieAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { brave_mode: 'on' }, (workingDir) => {
      const script = writeMcpFormServer(workingDir, 'echo-server.mjs', { expectedEchoArguments: echoArguments })
      writeJunieMcpConfig(workingDir, 'form_probe', process.execPath, [script])
    })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await modelScript.rule(
      { name: 'junie-echo-capability', when: { system: 'capability filter agent' }, respond: { text: '2' } },
      { name: 'junie-echo-task-name', when: { system: 'task description summarizer' }, respond: { text: 'MCP echo task' } },
    )
    await modelScript.queue(
      { toolCalls: [mcpToolCall(AgentProvider.JUNIE, 'junie-mcp-echo', { server: 'form_probe', tool: 'echo', input: echoArguments })] },
      { toolCalls: [junieAnswerToolCall('junie-echo-answer', 'The MCP call finished.')] },
    )
    await sendMessage(page, modelScript.prompt('Call the form_probe echo tool once.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(JSON.stringify(status.requests.find(request => request.stepIndex === 1)?.body)).toContain('PERMISSION_ACCEPTED')
    await expect(page.locator('[data-testid="message-bubble"]:visible').filter({ hasText: 'The MCP call finished.' }).first()).toBeVisible()
  })
})
