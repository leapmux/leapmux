import { expect } from '@playwright/test'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { museSubmitResultToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, chatScrollContainer, sendMessage, tabById, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { MUSE_E2E_SKIP_REASON, museTest } from '../muse-fixtures'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { MUSE_AGENT, nativeContext } from './scenarios'
import { expectMuseChildResult, MUSE_CHILD_OUTPUT, MUSE_CHILD_PROGRESS, MUSE_CHILD_TASK, openMuseChild } from './subagent-common.muse'

museTest('opens the actual child history and preserves its native session after reload', async ({ native }) => {
  const { page } = native
  const child = await openMuseChild(native, false)
  for (const reload of [false, true]) {
    if (reload)
      await page.reload()
    await tabById(page, child.childId).click()
    await waitForAgentIdle(page)
    await expect(userBubbles(page).filter({ hasText: MUSE_CHILD_TASK })).toHaveCount(1)
    await expect(chatScrollContainer(page).getByText(MUSE_CHILD_PROGRESS, { exact: true })).toBeVisible()
    await expect(chatScrollContainer(page).getByText(MUSE_CHILD_OUTPUT, { exact: true })).toBeVisible()
    await expectMuseChildResult(native)
    const snapshot = await readNativeMessageSnapshot(native, child.childId)
    expect(snapshot.agentSessionId).not.toBe('')
    const frames = snapshot.messages.map(message => nativeMessageBody(message))
    expect(JSON.stringify(frames)).toContain(MUSE_CHILD_OUTPUT)
    const spans = snapshot.messages.filter(message => message.spanId)
    expect(spans.length).toBeGreaterThan(0)
    expect(spans.every(message => message.agentSessionId === snapshot.agentSessionId)).toBe(true)
  }
})

const restartTest = createProviderProcessTest({
  agent: MUSE_AGENT,
  nativeContext,
  skipReason: MUSE_E2E_SKIP_REASON,
  openOptions: { optionValues: { workspaceTrust: 'agent', permissionMode: 'allowAll' } },
})

restartTest('restores the same native child route and history after its owner restarts', async ({ native, separateHubWorker }) => {
  const { page, modelScript } = native
  const child = await openMuseChild(native, false)
  const before = await readNativeMessageSnapshot(native, child.childId)
  await tabById(page, child.parentId).click()
  await exerciseWorkerRestart(native, separateHubWorker, 'worker')
  await tabById(page, child.childId).click()
  await expectMuseChildResult(native)
  const after = await readNativeMessageSnapshot(native, child.childId)
  expect(after.agentSessionId).toBe(before.agentSessionId)
  expect(after.messages.map(message => message.id)).toEqual(before.messages.map(message => message.id))
  const note = 'MUSERESTOREDCHILDNOTE: read this after your owner restarts.'
  const answer = 'The restored child read the exact new note.'
  const rule = 'the restored Muse child reads its exact note'
  await modelScript.rule({
    name: rule,
    when: { body: ['Workflow child completion protocol', note] },
    once: true,
    respond: { text: answer, toolCalls: [museSubmitResultToolCall('muse-restored-child-submit', 'The restored child processed its note.')] },
  })
  await sendMessage(page, note)
  await expect.poll(async () => (await modelScript.status()).ruleMatches[rule] ?? 0).toBe(1)
  await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
  await waitForAgentIdle(page)
  await page.reload()
  await tabById(page, child.childId).click()
  await expect(userBubbles(page).filter({ hasText: MUSE_CHILD_TASK })).toHaveCount(1)
  await expect(userBubbles(page).filter({ hasText: note })).toHaveCount(1)
  await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
  await tabById(page, child.parentId).click()
  await expect(userBubbles(page).filter({ hasText: note })).toHaveCount(0)
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/516.
