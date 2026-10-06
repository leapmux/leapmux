import type { MockModelToolCall } from '../helpers/mockModelScript'
import { expect } from '@playwright/test'
import { openNativeCatalogTurn } from '../helpers/nativeCodeExecution'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { exerciseShellToolExecution, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { reasonixInspectCapabilityToolCall, reasonixListCapabilitiesToolCall } from '../helpers/providerToolCalls'
import { reasonixTest } from '../reasonix-fixtures'
import { nativeContext } from './scenarios'
import { assertReasonixCapabilityInspection, assertReasonixCoreCatalog, parseReasonixCapabilities } from './toolCatalog'

reasonixTest('confirms the code executor is absent from the complete native core and deferred catalog', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const request = await openNativeCatalogTurn(context)
  assertReasonixCoreCatalog(request)
  const agent = await currentNativeAgent(context)
  const query = async (calls: MockModelToolCall[]) => {
    const { resultRequest } = await runNativeToolTurn(context, {
      toolCalls: calls,
      prompt: 'Read the exact native capability descriptors.',
      answer: 'The native capability discovery completed.',
    })
    const snapshot = await readNativeMessageSnapshot(context, agent.id)
    expect(snapshot.agentId).toBe(agent.id)
    expect(snapshot.agentSessionId).toBe(agent.agentSessionId)
    return calls.map(call => nativeToolResult(resultRequest, call.id))
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
