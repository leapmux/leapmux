import { Buffer } from 'node:buffer'
import { cpSync, existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { KIRO_OPTION, KIRO_POLICY_PRESET } from '../../../src/generated/contracts/kiro-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { kiroToolResult } from '../helpers/kiroToolResult'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { readMcpServerReceipt } from '../helpers/mcpServerReceipt'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { withNativeWorker } from '../helpers/nativeWorker'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { expect, KIRO_AGENT, kiroTest } from '../kiro-fixtures'
import { kiroCatalogEnvironment } from './catalogEnvironment'
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
    const context = { page, modelScript, leapmuxServer: server, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.KIRO }
    let receiptLog = ''
    await openProviderAgent(server, context.workspaceId, KIRO_AGENT, { optionValues: { [KIRO_OPTION.PolicyPreset]: KIRO_POLICY_PRESET.AllowAll }, prepare: (workingDir) => {
      receiptLog = join(workingDir, 'native-catalog-echo.jsonl')
      const script = writeMcpEchoServer(workingDir, { receiptLog })
      const project = join(workingDir, '.kiro', 'settings')
      mkdirSync(project, { recursive: true })
      writeFileSync(join(project, 'mcp.json'), JSON.stringify({ mcpServers: { echo_probe: { command: process.execPath, args: [script] } } }))
    } })
    await openWorkspace(page, context.workspaceId)
    await expect.poll(() => existsSync(receiptLog) && readMcpServerReceipt(receiptLog).toolCatalogs.some(catalog => catalog.tools.some(tool => tool.name === 'echo'))).toBe(true)
    const first = await sendNativeAnswer(context, 'Reply once while the complete native active registry remains available.', 'The native active catalog receipt completed.')
    await testInfo.attach('kiro-native-first-active-request', { body: Buffer.from(JSON.stringify(first)), contentType: 'application/json' })
    expect(first.mockCredential?.accepted).toBe(true)
    const catalog = kiroActiveToolCatalog(first)
    assertKiroActiveCatalog(catalog)
    expect(kiroScriptExecutors(catalog)).toEqual([])
    const echo = catalog.find(tool => tool.name === 'mcp_echo_probe_echo')
    expect(echo?.inputSchema.properties).toMatchObject({ value: { type: 'string' } })
    const step = (await modelScript.status()).stepCount
    const value = 'KIRO_COMPLETE_ACTIVE_ECHO'
    const call = mcpToolCall(AgentProvider.KIRO, 'native-active-echo', { server: 'echo_probe', tool: 'echo', input: { value } })
    await modelScript.queue({ toolCalls: [call] }, { text: 'The native active echo tool completed.' })
    await sendMessage(page, modelScript.prompt('Invoke the controlled native echo tool once.'))
    const loaded = await modelScript.waitForSteps(step + 2)
    await waitForAgentIdle(page)
    const next = loaded.requests.find(request => request.stepIndex === step + 1)
    if (!next)
      throw new Error('The native Kiro echo tool reached no next model request.')
    const result = kiroToolResult(next, call.id)
    await testInfo.attach('kiro-native-active-echo-receipt', { body: Buffer.from(JSON.stringify({ call, result, request: next })), contentType: 'application/json' })
    expect(result.failed).toBe(false)
    expect(result.text).toContain(`MCP_ECHO:${value}`)
    expect(readMcpServerReceipt(receiptLog).toolResults).toContainEqual(expect.objectContaining({ tool: 'echo', text: `MCP_ECHO:${value}`, isError: false }))
    expect(next.mockCredential?.accepted).toBe(true)
    await testInfo.attach('kiro-native-complete-active-catalog', { body: Buffer.from(JSON.stringify({ catalog, serverReceipt: readMcpServerReceipt(receiptLog) })), contentType: 'application/json' })
    await exerciseShellToolExecution({ ...context, readToolResult: kiroToolResult }, { includeFailure: false })
  })
})
