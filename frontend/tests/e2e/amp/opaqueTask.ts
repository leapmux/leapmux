import type { Locator } from '@playwright/test'
import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { BackgroundTaskKind, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { cleanupOnFailure, withCleanup } from '../helpers/cleanup'
import { ruleRequest } from '../helpers/mockModelScript'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { uniqueMarker } from '../helpers/shellArguments'
import { requireRegistryRow } from '../helpers/subagentRegistry'
import { messageContents, sendMessage, tabById, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { ampToolResultReader } from './toolResult'

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
  const release = async () => {
    await context.modelScript.releaseGateIfHeld(gate)
  }
  return cleanupOnFailure(async () => {
    await context.modelScript.rule({ name: rule, when: { user: task.prompt }, respond: { text: report, stream: { chunkChars: progress.length, delayMs: 0, gates: [{ afterChunk: 1, name: gate }] } } })
    const start = await context.modelScript.queue(
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
    const childRequest = ruleRequest(status, rule)
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
        // The second queued step answers the parent request that carries the report of the task.
        const request = await context.modelScript.requestAt(start + 1)
        await tabById(context.page, agent.id).click()
        await waitForAgentIdle(context.page)
        expect((await ampToolResultReader(context)(request, callId)).text).toContain(report)
        await expect(messageContents(context.page).filter({ hasText: report }).first()).toBeVisible()
        await expect(row).toHaveAttribute('data-status', 'succeeded')
        finished = true
      },
    }
  }, release)
}

/**
 * Hold an actual remote Amp Task, run `proveLimit` while the task runs, and finish the task even when the proof fails.
 * After a reload, require one saved task that holds no child agent: the stream of Amp carries the remote Task call and
 * its report without a child session ID.
 */
export async function exerciseOpaqueAmpTaskLimit(
  context: ManagedNativeScenarioContext,
  proveLimit: (task: OpaqueAmpTask) => Promise<void>,
): Promise<void> {
  const task = await openOpaqueAmpTask(context)
  await withCleanup(() => proveLimit(task), task.finish)
  await context.page.reload()
  await waitForSettingsHydrated(context.page, 'permissionMode')
  const saved = await readNativeSidebarSnapshot(context, task.parentId)
  expect(saved.backgroundTasks).toHaveLength(1)
  expect(saved.backgroundTasks[0]?.childAgentId).toBe('')
}
