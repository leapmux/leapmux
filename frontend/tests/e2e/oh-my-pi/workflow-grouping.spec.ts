import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { ohMyPiYieldToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectUngroupedChildRows } from '../helpers/workflowGrouping'
import { ohMyPiTest } from '../ohmypi-fixtures'

const MARKERS = ['FIRST', 'SECOND'] as const

ohMyPiTest('keeps two actual native task children as separate ungrouped rows', async ({ native }) => {
  const { page, modelScript } = native
  await applyPermissionPreset(page, 'bypass')
  for (const marker of MARKERS)
    await modelScript.rule({ name: `omp ungrouped ${marker}`, when: { user: `OMPGROUP${marker}`, body: '"name":"yield"' }, respond: { toolCalls: [ohMyPiYieldToolCall(`omp-yield-${marker}`, `OMP_NATIVE_GROUP_REPORT_${marker}`)] }, once: true })
  const start = await modelScript.queue(
    { toolCalls: MARKERS.map(marker => spawnSubagentToolCall(AgentProvider.OH_MY_PI, `omp-task-${marker}`, { description: `Run the ${marker.toLowerCase()} group task`, prompt: modelScript.prompt(`OMPGROUP${marker} Report the requested result.`) })) },
    { text: 'The parent consumed both actual native task reports.' },
  )
  await sendMessage(page, modelScript.prompt('Run both native task children and collect both reports.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  const request = await modelScript.requestAt(start + 1)
  const status = await modelScript.status()
  for (const marker of MARKERS) {
    expect(status.ruleMatches[`omp ungrouped ${marker}`]).toBe(1)
    expect(nativeToolResult(request, `omp-task-${marker}`)).toContain(`OMP_NATIVE_GROUP_REPORT_${marker}`)
  }
  await expectUngroupedChildRows(native, { childAgentIds: 'distinct' })
})
