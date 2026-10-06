import type { Locator } from '@playwright/test'
import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { BackgroundTaskKind, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ampToolResultReader } from '../helpers/ampToolResult'
import { cleanupOnFailure } from '../helpers/cleanup'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { uniqueMarker } from '../helpers/shellArguments'
import { requireRegistryRow } from '../helpers/subagentRegistry'
import { messageContents, sendMessage, tabById, waitForAgentIdle } from '../helpers/ui'

export interface OpaqueAmpTask {
  row: Locator
  parentId: string
  progress: string
  report: string
  childRequest: MockModelRequestRecord
  finish: () => Promise<void>
}

/** Exercise the disabled row's product guard without waiting for Playwright to enable it. */
export async function clickOpaqueAmpTaskRow(row: Locator): Promise<void> {
  await row.evaluate((element) => {
    if (!(element instanceof HTMLButtonElement) || element.getAttribute('aria-disabled') !== 'true')
      throw new Error('The opaque Amp Task requires an actual disabled row button.')
    element.click()
  })
}

/** Hold an actual remote Amp Task while its parent exposes only the call row. */
export async function openOpaqueAmpTask(context: ManagedNativeScenarioContext, options: {
  report?: string
  task?: { description: string, prompt: string }
  parentPrompt?: string
  parentAnswer?: string
  ruleName?: string
  callId?: string
} = {}): Promise<OpaqueAmpTask> {
  const marker = uniqueMarker()
  const gate = `amp-opaque-${marker}`
  const rule = options.ruleName ?? `amp-opaque-child-${marker}`
  const progress = options.report ? options.report.slice(0, 10) : `AMPCHILDPROGRESS${marker}`
  const report = options.report ?? `${progress} The actual remote task reports forty-two.`
  const callId = options.callId ?? `amp-task-${marker}`
  const task = options.task ?? { description: 'Run the remote counting task', prompt: `AMPREMOTETASK${marker} Count to forty-two and report the result.` }
  const agent = await currentNativeAgent(context)
  const start = (await context.modelScript.status()).stepCount
  const release = async () => {
    await context.modelScript.releaseGateIfHeld(gate)
  }
  return cleanupOnFailure(async () => {
    await context.modelScript.rule({ name: rule, when: { user: task.prompt }, respond: { text: report, stream: { chunkChars: progress.length, delayMs: 0, gates: [{ afterChunk: 1, name: gate }] } } })
    await context.modelScript.queue(
      { toolCalls: [spawnSubagentToolCall(context.provider, callId, { description: task.description, prompt: context.modelScript.prompt(task.prompt) })] },
      { text: options.parentAnswer ?? 'The parent consumed the actual remote report.' },
    )
    await sendMessage(context.page, context.modelScript.prompt(options.parentPrompt ?? 'Start the remote counting task and wait for its report.'))
    await context.modelScript.waitForGate(gate)
    const row = await requireRegistryRow(context.page)
    await expect(row).toHaveAttribute('data-status', 'running')
    await expect(row).toHaveAttribute('data-child-agent-id', '')
    await expect(row).toHaveAttribute('aria-disabled', 'true')
    const snapshot = await readNativeSidebarSnapshot(context, agent.id)
    const nativeTask = snapshot.backgroundTasks.find(value => value.kind === BackgroundTaskKind.SUBAGENT && value.status === BackgroundTaskStatus.RUNNING)
    expect(nativeTask).toBeDefined()
    expect(nativeTask?.childAgentId).toBe('')
    const status = await context.modelScript.status()
    const childRequest = status.requests.find(request => request.rule === rule)
    if (!childRequest)
      throw new Error('The remote Amp Task produced no actual child model request.')
    let finished = false
    return {
      row,
      parentId: agent.id,
      progress,
      report,
      childRequest,
      finish: async () => {
        if (finished)
          return
        await release()
        const completed = await context.modelScript.waitForSteps(start + 2)
        await tabById(context.page, agent.id).click()
        await waitForAgentIdle(context.page)
        const request = completed.requests.find(value => value.stepIndex === start + 1)
        if (!request)
          throw new Error('The remote Amp Task report reached no native parent request.')
        expect((await ampToolResultReader(context)(request, callId)).text).toContain(report)
        await expect(messageContents(context.page).filter({ hasText: report }).first()).toBeVisible()
        await expect(row).toHaveAttribute('data-status', 'completed')
        finished = true
      },
    }
  }, release)
}
