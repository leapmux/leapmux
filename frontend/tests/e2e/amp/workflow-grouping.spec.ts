import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ampTest } from '../amp-fixtures'
import { nativeToolOutcome } from '../helpers/nativeScenario'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectUngroupedChildRows } from '../helpers/workflowGrouping'

const MARKERS = ['FIRST', 'SECOND'] as const

// Amp runs each remote Task without a child session, so neither row links a child agent.
ampTest('keeps two actual remote Tasks as separate rows without a workflow group', async ({ native }) => {
  const { page, modelScript } = native
  for (const marker of MARKERS)
    await modelScript.rule({ name: `amp ungrouped ${marker}`, when: { user: `AMPWORKFLOW${marker}` }, respond: { text: `AMP_NATIVE_GROUP_REPORT_${marker}` }, once: true })
  const start = await modelScript.queue(
    { toolCalls: MARKERS.map(marker => spawnSubagentToolCall(AgentProvider.AMP, `amp-workflow-${marker}`, { description: `Run the ${marker.toLowerCase()} group task`, prompt: modelScript.prompt(`AMPWORKFLOW${marker} Report the requested result.`) })) },
    { text: 'The parent consumed both actual remote task reports.' },
  )
  await sendMessage(page, modelScript.prompt('Run both remote Tasks and collect both reports.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  const request = await modelScript.requestAt(start + 1)
  const status = await modelScript.status()
  for (const marker of MARKERS) {
    expect(status.ruleMatches[`amp ungrouped ${marker}`]).toBe(1)
    expect((await nativeToolOutcome(native, request, `amp-workflow-${marker}`)).text).toContain(`AMP_NATIVE_GROUP_REPORT_${marker}`)
  }
  await expectUngroupedChildRows(native, { childAgentIds: 'absent' })
})
