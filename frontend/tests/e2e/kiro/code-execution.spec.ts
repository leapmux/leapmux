import { Buffer } from 'node:buffer'
import { cpSync } from 'node:fs'
import { join } from 'node:path'
import { KIRO_OPTION, KIRO_POLICY_PRESET } from '../../../src/generated/contracts/kiro-protocol'
import { kiroToolResult } from '../helpers/kiroToolResult'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { readMcpServerReceipt, waitForMcpToolListed } from '../helpers/mcpServerReceipt'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeTextStep } from '../helpers/nativeScenario'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { withNativeWorker } from '../helpers/nativeWorker'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { expect, KIRO_AGENT, kiroTest } from '../kiro-fixtures'
import { kiroCatalogEnvironment } from './catalogEnvironment'
import { writeKiroProjectMcpServers } from './mcpConfiguration'
import { nativeContext } from './scenarios'
import { assertKiroActiveCatalog, kiroActiveToolCatalog, kiroScriptExecutors } from './toolCatalog'

kiroTest('proves the complete active native catalog and actual shell output', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const suiteHome = leapmuxServer.agentEnv.KIRO_HOME
  if (!suiteHome)
    throw new Error('The native Kiro catalog probe requires its isolated profile.')
  const profile = createTestDirectory('kiro-native-active-catalog-home-')
  cpSync(suiteHome, profile, { recursive: true })

  await withNativeWorker(leapmuxServer, {
    dataDirPrefix: 'kiro-native-active-catalog-worker',
    workerName: 'Kiro native active catalog test',
    env: kiroCatalogEnvironment({ KIRO_HOME: profile }),
  }, async ({ server }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer: server, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    const echoDirectory = createTestDirectory('kiro-native-catalog-echo-')
    const receiptLog = join(echoDirectory, 'native-catalog-echo.jsonl')
    const echoServer = writeMcpEchoServer(echoDirectory, { receiptLog })
    await openProviderAgent(server, context.workspaceId, KIRO_AGENT, { optionValues: { [KIRO_OPTION.PolicyPreset]: KIRO_POLICY_PRESET.AllowAll }, prepare: (workingDir) => {
      writeKiroProjectMcpServers(workingDir, echoServer)
    } })
    await openWorkspace(page, context.workspaceId)
    await waitForMcpToolListed(receiptLog, 'echo')
    const first = await sendNativeAnswer(context, 'Reply once while the complete native active registry remains available.', 'The native active catalog receipt completed.')
    await testInfo.attach('kiro-native-first-active-request', { body: Buffer.from(JSON.stringify(first)), contentType: 'application/json' })
    expect(first.mockCredential?.accepted).toBe(true)
    const catalog = kiroActiveToolCatalog(first)
    assertKiroActiveCatalog(catalog)
    expect(kiroScriptExecutors(catalog)).toEqual([])
    const value = 'KIRO_COMPLETE_ACTIVE_ECHO'
    const call = mcpToolCall(context.provider, 'native-active-echo', { server: echoServer.name, tool: 'echo', input: { value } })
    const echo = catalog.find(tool => tool.name === call.name)
    expect(echo?.inputSchema.properties).toMatchObject({ value: { type: 'string' } })
    // The AllowAll preset runs the tool without a permission request. The turn therefore waits only for its model steps,
    // and it clicks no Allow button.
    const start = await modelScript.queue({ toolCalls: [call] }, nativeTextStep(context, 'The native active echo tool completed.'))
    await sendMessage(page, modelScript.prompt('Invoke the controlled native echo tool once.'))
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    const next = await modelScript.requestAt(start + 1)
    const result = kiroToolResult(next, call.id)
    await testInfo.attach('kiro-native-active-echo-receipt', { body: Buffer.from(JSON.stringify({ call, result, request: next })), contentType: 'application/json' })
    expect(result.failed).toBe(false)
    expect(result.text).toContain(`MCP_ECHO:${value}`)
    expect(readMcpServerReceipt(receiptLog).toolResults).toContainEqual(expect.objectContaining({ tool: 'echo', text: `MCP_ECHO:${value}`, isError: false }))
    expect(next.mockCredential?.accepted).toBe(true)
    await testInfo.attach('kiro-native-complete-active-catalog', { body: Buffer.from(JSON.stringify({ catalog, serverReceipt: readMcpServerReceipt(receiptLog) })), contentType: 'application/json' })
    await exerciseShellToolExecution(context, { includeFailure: false })
  })
})
