import { expect } from '@playwright/test'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { bashToolCall, ohMyPiYieldToolCall } from '../helpers/providerToolCalls'
import { steerQueuedInput } from '../helpers/steer'
import { openHeldChildTab } from '../helpers/subagentRegistry'
import { applyPermissionPreset, messageContents, tabById, userBubbles } from '../helpers/ui'
import { ohMyPiTest } from '../ohmypi-fixtures'
import { ohMyPiHeldChild } from './childScenario'

ohMyPiTest('delivers a steered message to the native child and keeps its parent usable', async ({ native }) => {
  const { page, modelScript } = native
  await applyPermissionPreset(page, 'bypass')
  const message = 'Also say OMP_CHILD_STEERED in your report.'
  await modelScript.rule({
    name: 'the child reads its steered message',
    when: {
      body: ['"name":"yield"', message],
    },
    respond: {
      toolCalls: [
        ohMyPiYieldToolCall('steered-child-yield', 'OMP_CHILD_STEERED'),
      ],
    },
    once: true,
  })
  const child = await openHeldChildTab(native, {
    ...ohMyPiHeldChild(),
    heldAnswer: {
      toolCalls: [
        bashToolCall(native.provider, 'child-ready', 'printf child-ready'),
      ],
    },
  })
  try {
    await steerQueuedInput(page, { message, match: 'Also say' })
    await child.finish()
    await expect.poll(async () => (await modelScript.status()).ruleMatches['the child reads its steered message'] ?? 0).toBe(1)
    await tabById(page, child.childId).click()
    await expect(userBubbles(page).filter({ hasText: message })).toBeVisible()
    await expect(messageContents(page).filter({ hasText: 'OMP_CHILD_STEERED' }).last()).toBeVisible()
    await tabById(page, child.parentId).click()
    await sendNativeAnswer(native, 'Confirm the parent still accepts input.', 'The parent still accepts input.')
  }
  finally {
    await child.release()
  }
})
