import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { writeJunieMcpConfig } from '../helpers/junieMcp'
import { writeMcpFormServer } from '../helpers/mcpFormServer'
import { nativeMcpRefusal, readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { junieAnswerToolCall, mcpToolCall } from '../helpers/providerToolCalls'
import { openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expect as junieExpect, junieTest, openJunieAgent } from '../junie-fixtures'

junieTest.describe('Junie MCP input form', () => {
  junieTest('declines a local MCP form request without opening a browser form', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }, testInfo) => {
    let receiptLog = ''
    await openJunieAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { brave_mode: 'on' }, (workingDir) => {
      receiptLog = join(workingDir, 'form-receipt.json')
      const script = writeMcpFormServer(workingDir, 'form-server.mjs', { receiptLog })
      writeJunieMcpConfig(workingDir, 'form_probe', process.execPath, [script])
    })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await modelScript.rule(
      { name: 'junie-mcp-capability', when: { system: 'capability filter agent' }, respond: { text: '1' } },
      { name: 'junie-mcp-task-name', when: { system: 'task description summarizer' }, respond: { text: 'MCP form task' } },
    )
    await modelScript.queue(
      { toolCalls: [mcpToolCall(AgentProvider.JUNIE, 'junie-mcp-form', { server: 'form_probe', tool: 'ask', input: {} })] },
      { toolCalls: [junieAnswerToolCall('junie-mcp-answer', 'The form completed.')] },
    )
    await sendMessage(page, modelScript.prompt('Call the form_probe ask tool once.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    await junieExpect.poll(() => existsSync(receiptLog)).toBe(true)
    const receipt = readMcpServerReceipt(receiptLog)
    const refusal = nativeMcpRefusal(receipt)
    await testInfo.attach('junie-mcp-form-native-reply', { body: JSON.stringify(receipt), contentType: 'application/json' })
    junieExpect(refusal.reply.error).toMatchObject({ code: -32601, message: 'Server does not support elicitation/create' })
    junieExpect(refusal.toolResult.id).toBe(refusal.request.toolRequestId)
    junieExpect(nativeToolResult(status.requests.find(request => request.stepIndex === 1), 'junie-mcp-form')).toContain(refusal.toolResult.text)
    await junieExpect(page.getByTestId('elicitation-form').filter({ visible: true })).toHaveCount(0)
  })
})
