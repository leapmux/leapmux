import type { TestInfo } from '@playwright/test'
import type { MockModelRequestRecord, MockModelToolCall } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { withCleanup } from '../helpers/cleanup'
import { ruleRequest } from '../helpers/mockModelScript'
import { currentNativeAgent, selectedAgentTabId } from '../helpers/nativeScenario'
import { readToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, expectRowsInOrder, messageBubbles, messageContents, sendMessage, tabById, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { DROID_CHILD_SYSTEM } from './childIdentity'
import { droidChildNoticeRule } from './childNotice'
import { readDroidToolResult } from './toolResult'

const CHILD_TASK = 'Read the child note and report its marker.'
const CHILD_DESCRIPTION = 'Inspect the child note'

/**
 * Build the background Task through which the root starts the child. The Droid tool builder adds `call_` to the
 * scripted ID `droid-spawn`, so the mock model sends the call as `call_droid-spawn`, and Droid keeps that ID.
 */
export function childSpawnCall(childPrompt: string): MockModelToolCall {
  return spawnSubagentToolCall(AgentProvider.DROID, 'droid-spawn', { description: CHILD_DESCRIPTION, prompt: childPrompt, background: true })
}

/**
 * Read the result that Droid gave the root for `spawn`, the Task of {@link childSpawnCall}. Exactly one Task call and
 * exactly one result must carry the ID of the built call. A lookup by the scripted ID finds no result, because that
 * ID has no `call_` prefix.
 */
export function childSpawnResult(request: MockModelRequestRecord, spawn: MockModelToolCall): string {
  return readDroidToolResult(request, spawn.id, spawn.name).text
}

/**
 * Keep the native child Read, archive, and follow-up assertions in one provider-owned scenario.
 * The scenario ends with the tab of the child selected.
 */
export async function exerciseNativeChildTranscript(context: ManagedNativeScenarioContext, testInfo: TestInfo, options: { followUp: boolean }): Promise<void> {
  const { page, modelScript, leapmuxServer } = context
  const { workingDir } = await currentNativeAgent(context)

  const note = join(workingDir, 'droid-child-note.txt')
  writeFileSync(note, 'DROID_CHILD_READ_MARKER\n')
  const childPrompt = modelScript.prompt(CHILD_TASK)
  const childGate = 'droid-child-final'
  const followUp = modelScript.prompt('DROID_CHILD_FOLLOWUP_REQUEST: Report the file marker once more.')
  const followUpGate = 'droid-child-followup'
  await modelScript.rule(
    {
      name: 'the Droid child reads its note',
      when: { system: DROID_CHILD_SYSTEM, body: CHILD_TASK },
      respond: { text: modelScript.prompt('DROID_CHILD_EARLY'), toolCalls: [readToolCall(context.provider, 'droid-child-read', note)] },
      once: true,
    },
    {
      name: 'the Droid child reports the marker',
      when: { system: DROID_CHILD_SYSTEM, body: [CHILD_TASK, 'DROID_CHILD_EARLY'] },
      respond: { gate: childGate, text: modelScript.prompt('DROID_CHILD_FINAL') },
      once: true,
    },
    droidChildNoticeRule(CHILD_DESCRIPTION, { text: 'DROID_ROOT_CHILD_REPORTED' }, 'the Droid root reports its completed child'),
    {
      name: 'the Droid child answers its tab follow-up',
      when: { system: DROID_CHILD_SYSTEM, body: 'Report the file marker once more.' },
      respond: { gate: followUpGate, text: modelScript.prompt('DROID_CHILD_FOLLOWUP_DONE') },
      once: true,
    },
  )
  const spawn = childSpawnCall(childPrompt)
  const start = await modelScript.queue(
    { toolCalls: [spawn] },
    { text: 'DROID_ROOT_DONE' },
  )
  await sendMessage(page, modelScript.prompt('Delegate the note inspection to a background child.'))
  await modelScript.waitForSteps(start + 2)
  const taskResult = childSpawnResult(await modelScript.requestAt(start + 1), spawn)
  await testInfo.attach('droid-native-task-result', { body: taskResult, contentType: 'text/plain' })
  const factoryHome = leapmuxServer.agentEnv?.FACTORY_HOME_OVERRIDE
  if (!factoryHome)
    throw new Error('the Droid test needs an isolated Factory home')
  const nativeLog = join(factoryHome, '.factory', 'logs', 'droid-log-single.log')
  if (existsSync(nativeLog))
    await testInfo.attach('droid-native-log', { body: readFileSync(nativeLog), contentType: 'text/plain' })
  expect(taskResult).toContain('Task launched in background')
  await expect.poll(async () => (await modelScript.status()).ruleMatches['the Droid child reads its note'] ?? 0).toBe(1)
  // The root is the agent on screen before the child tab opens.
  const rootTabID = await selectedAgentTabId(page)

  const childTabID = await withCleanup(async () => {
    const row = await requireRegistryRow(page)
    await expect(row).toContainText(CHILD_DESCRIPTION)
    await expect(row).toHaveAttribute('data-status', 'running')
    const openedTabID = await openChildTabFromRow(page, row)
    await expect(userBubbles(page).filter({ hasText: childPrompt }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'DROID_CHILD_EARLY' }).first()).toBeVisible()
    await expect(messageContents(page).filter({ hasText: 'DROID_CHILD_READ_MARKER' }).first()).toBeVisible()
    await expectRowsInOrder(messageBubbles(page), [CHILD_TASK, 'DROID_CHILD_EARLY', 'DROID_CHILD_READ_MARKER'])
    await modelScript.waitForGate(childGate)
    await tabById(page, rootTabID).click()
    await expect(messageContents(page).filter({ hasText: 'DROID_CHILD_EARLY' })).toHaveCount(0)
    await expect(messageContents(page).filter({ hasText: 'DROID_CHILD_READ_MARKER' })).toHaveCount(0)
    await tabById(page, openedTabID).click()
    return openedTabID
  }, async () => {
    await modelScript.releaseGateIfHeld(childGate)
  })

  // The root consumed both queued steps before the child tab opened, so no step wait remains.
  await waitForAgentIdle(page)
  const row = await requireRegistryRow(page)
  await expectRowBecomesFinal(page, row)
  await expect(assistantBubbles(page).filter({ hasText: 'DROID_CHILD_FINAL' }).first()).toBeVisible()
  await expectRowsInOrder(messageBubbles(page), ['DROID_CHILD_READ_MARKER', 'DROID_CHILD_FINAL'])
  const status = await modelScript.status()
  expect(status.ruleMatches['the Droid child reads its note']).toBe(1)
  expect(status.ruleMatches['the Droid child reports the marker']).toBe(1)
  await expect.poll(async () => (await modelScript.status()).ruleMatches['the Droid root reports its completed child'] ?? 0).toBe(1)

  if (options.followUp) {
    await sendMessage(page, followUp)
    await modelScript.waitForGate(followUpGate)
    await withCleanup(async () => {
      const followUpRequest = ruleRequest(await modelScript.status(), 'the Droid child answers its tab follow-up')
      expect(JSON.stringify(followUpRequest.body).includes('DROID_CHILD_FINAL')).toBe(true)
      expect(JSON.stringify(followUpRequest.body).includes('DROID_ROOT_DONE')).toBe(false)
      await expect(userBubbles(page).filter({ hasText: 'Report the file marker once more.' })).toHaveCount(1)
      await expect(row).toHaveAttribute('data-status', 'running')
      await tabById(page, rootTabID).click()
      await expect(userBubbles(page).filter({ hasText: 'Report the file marker once more.' })).toHaveCount(0)
      await tabById(page, childTabID).click()
    }, async () => {
      await modelScript.releaseGateIfHeld(followUpGate)
    })
    await expect(assistantBubbles(page).filter({ hasText: 'DROID_CHILD_FOLLOWUP_DONE' })).toHaveCount(1)
    await expectRowBecomesFinal(page, row)
    const finalStatus = await modelScript.status()
    expect(finalStatus.ruleMatches['the Droid child answers its tab follow-up']).toBe(1)
    expect(finalStatus.unexpectedRequests).toHaveLength(0)
  }

  await expect(tabById(page, childTabID)).toHaveAttribute('aria-selected', 'true')
}
