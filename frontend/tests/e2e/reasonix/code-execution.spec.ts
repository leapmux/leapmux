import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { exerciseShellToolExecution, waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { reasonixInspectCapabilityToolCall, reasonixListCapabilitiesToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace, sendMessage } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'
import { assertReasonixCapabilityInspection, assertReasonixCoreCatalog, parseReasonixCapabilities } from './toolCatalog'

reasonixTest('confirms the code executor is absent from the complete native core and deferred catalog', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.REASONIX }
  await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, context.workspaceId, createTestDirectory('native-code-limit-'), agentOpenOptions(context.provider))
  await openWorkspace(page, context.workspaceId)
  const request = await sendNativeAnswer(context, 'Reply once while the native tool catalog remains available.', 'The actual native catalog turn completed.')
  assertReasonixCoreCatalog(request)
  const agent = await currentNativeAgent(context)
  const query = async (calls: ReturnType<typeof reasonixListCapabilitiesToolCall>[]) => {
    const start = (await modelScript.status()).stepCount
    await modelScript.queue({ toolCalls: calls }, nativeTextStep(context, 'The native capability discovery completed.'))
    await sendMessage(page, modelScript.prompt('Read the exact native capability descriptors.'))
    await waitForNativeToolSteps(context, start + 2)
    const next = (await modelScript.waitForSteps(start + 2)).requests.find(record => record.stepIndex === start + 1)
    if (!next)
      throw new Error('The native Reasonix capability results reached no next model request.')
    const snapshot = await readNativeMessageSnapshot(context, agent.id)
    expect(snapshot.agentId).toBe(agent.id)
    expect(snapshot.agentSessionId).toBe(agent.agentSessionId)
    return calls.map(call => nativeToolResult(next, call.id))
  }
  const list = (await query([reasonixListCapabilitiesToolCall('reasonix-complete-list')]))[0]!
  const capabilities = parseReasonixCapabilities(list)
  const descriptions = capabilities.length
    ? await query(capabilities.map((entry, index) => reasonixInspectCapabilityToolCall(`reasonix-inspect-${index}`, entry.id)))
    : []
  const inspected = capabilities.map((entry, index) => assertReasonixCapabilityInspection(descriptions[index]!, entry))
  await testInfo.attach('reasonix-complete-native-capability-catalog', {
    body: JSON.stringify({ agentId: agent.id, sessionId: agent.agentSessionId, list: JSON.parse(list), inspected }),
    contentType: 'application/json',
  })
  await exerciseShellToolExecution(context, { includeFailure: false })
})
