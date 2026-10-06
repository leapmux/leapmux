import type { TestInfo } from '@playwright/test'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { isObject } from '../../../src/lib/jsonPick'
import { expect } from '../droid-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { currentNativeAgent, selectedAgentTabId } from '../helpers/nativeScenario'
import { readToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, expectRowsInOrder, messageBubbles, messageContents, sendMessage, tabById, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { DROID_CHILD_SYSTEM } from './childIdentity'
import { droidChildNoticeRule } from './childNotice'

const CHILD_TASK = 'Read the child note and report its marker.'

/** Keep the native child Read, archive, and follow-up assertions in one provider-owned scenario. */
export async function exerciseNativeChildTranscript(context: ManagedNativeScenarioContext, testInfo: TestInfo, options: { followUp: boolean }): Promise<string> {
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
    droidChildNoticeRule('Inspect the child note', { text: 'DROID_ROOT_CHILD_REPORTED' }, 'the Droid root reports its completed child'),
    {
      name: 'the Droid child answers its tab follow-up',
      when: { system: DROID_CHILD_SYSTEM, body: 'Report the file marker once more.' },
      respond: { gate: followUpGate, text: modelScript.prompt('DROID_CHILD_FOLLOWUP_DONE') },
      once: true,
    },
  )
  const start = await modelScript.queue(
    { toolCalls: [spawnSubagentToolCall(context.provider, 'droid-spawn', { description: 'Inspect the child note', prompt: childPrompt, background: true })] },
    { text: 'DROID_ROOT_DONE' },
  )
  await sendMessage(page, modelScript.prompt('Delegate the note inspection to a background child.'))
  await modelScript.waitForSteps(start + 2)
  const rootBody = (await modelScript.requestAt(start + 1)).body
  if (!isObject(rootBody) || !Array.isArray(rootBody.messages))
    throw new Error('the Droid model request has no messages after its Task call')
  const toolResult = rootBody.messages.find(message => isObject(message) && message.role === 'tool')
  if (!isObject(toolResult) || typeof toolResult.content !== 'string')
    throw new Error('the Droid model request has no Task result')
  await testInfo.attach('droid-native-task-result', { body: toolResult.content, contentType: 'text/plain' })
  const factoryHome = leapmuxServer.agentEnv?.FACTORY_HOME_OVERRIDE
  if (!factoryHome)
    throw new Error('the Droid test needs an isolated Factory home')
  const nativeLog = join(factoryHome, '.factory', 'logs', 'droid-log-single.log')
  if (existsSync(nativeLog))
    await testInfo.attach('droid-native-log', { body: readFileSync(nativeLog), contentType: 'text/plain' })
  expect(toolResult.content).toContain('Task launched in background')
  await expect.poll(async () => (await modelScript.status()).ruleMatches['the Droid child reads its note'] ?? 0).toBe(1)
  // The root is the agent on screen before the child tab opens.
  const rootTabID = await selectedAgentTabId(page)

  const childTabID = await withCleanup(async () => {
    const row = await requireRegistryRow(page)
    await expect(row).toContainText('Inspect the child note')
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
      const followUpRequest = (await modelScript.status()).requests.find(request => request.rule === 'the Droid child answers its tab follow-up')
      expect(followUpRequest).toBeDefined()
      expect(JSON.stringify(followUpRequest?.body ?? {}).includes('DROID_CHILD_FINAL')).toBe(true)
      expect(JSON.stringify(followUpRequest?.body ?? {}).includes('DROID_ROOT_DONE')).toBe(false)
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

  return childTabID
}
