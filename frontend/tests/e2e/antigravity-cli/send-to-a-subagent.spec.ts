import { expect } from '@playwright/test'
import { antigravityTest } from '../antigravity-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { finishAntigravityChildWithReload, openAntigravityRunningChild } from './childScenarios'

antigravityTest('sends a new prompt to the finished native child and revives its row', async ({ native }) => {
  const child = await openAntigravityRunningChild(native)
  await finishAntigravityChildWithReload(native, child)
  const task = 'Answer the follow-up in the same native child.'
  const prompt = native.modelScript.prompt(task)
  const answer = 'ANTIGRAVITY_CHILD_FOLLOWUP_ANSWER'
  const gate = 'antigravity-child-followup'
  await native.modelScript.rule({
    name: 'the native child answers its follow-up',
    when: { user: task },
    respond: { text: answer, gate },
    once: true,
  })
  await withCleanup(async () => {
    await sendMessage(native.page, prompt)
    await native.modelScript.waitForGate(gate)
    await expect(child.row).toHaveAttribute('data-status', 'running')
    await native.modelScript.releaseGate(gate)
    await waitForAgentIdle(native.page)
    await expect(child.row).toHaveAttribute('data-status', 'succeeded')
    await expect(messageContents(native.page).filter({ hasText: answer }).first()).toBeVisible()
  }, async () => {
    await native.modelScript.releaseGateIfHeld(gate)
  })
})
