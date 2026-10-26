import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { nativeModelContextText } from '../helpers/nativeScenario'
import { museSubmitResultToolCall } from '../helpers/providerToolCalls'
import { steerQueuedInput } from '../helpers/steer'
import { assistantBubbles, sendMessage, tabById, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { museTest } from '../muse-fixtures'
import { MUSE_AGENT } from './scenarios'
import { MUSE_CHILD_GATE, openMuseChild } from './subagent-common.muse'

for (const active of [true, false]) {
  museTest(`delivers a human note to the ${active ? 'running' : 'completed'} native child`, async ({ native }, testInfo) => {
    const { page, modelScript } = native
    const note = `Read MUSE_CHILD_HUMAN_${active ? 'ACTIVE' : 'IDLE'} in your own session.`
    const answer = `The ${active ? 'running' : 'completed'} child read its human note.`
    const rule = 'the Muse child reads its human input'
    await withCleanup(async () => {
      // Muse admits completed-child followup only in a trusted workspace.
      if (!active) {
        const { agentId } = await openProviderAgent(native.leapmuxServer, native.workspaceId, MUSE_AGENT, {
          optionValues: { workspaceTrust: 'agent', permissionMode: 'allowAll' },
        })
        await tabById(page, agentId).click()
      }
      const child = await openMuseChild(native, active)
      await modelScript.rule({
        name: rule,
        when: { body: ['Workflow child completion protocol', note] },
        once: true,
        respond: { text: answer, toolCalls: [museSubmitResultToolCall('muse-child-note-submit', 'The human note was processed.')] },
      })
      if (active) {
        await steerQueuedInput(page, { message: note, match: note })
        await child.finish()
      }
      else {
        await sendMessage(page, note)
      }
      await expect.poll(async () => (await modelScript.status()).ruleMatches[rule] ?? 0).toBe(1)
      await waitForAgentIdle(page)
      const request = (await modelScript.status()).requests.find(request => request.rule === rule)
      if (!request)
        throw new Error('The native child did not consume its human note.')
      expect(nativeModelContextText(request)).toContain(note)
      await testInfo.attach('muse-child-input-request', { body: JSON.stringify({ childId: child.childId, parentId: child.parentId, request }), contentType: 'application/json' })
      for (const reload of [false, true]) {
        if (reload)
          await page.reload()
        await tabById(page, child.childId).click()
        await expect(userBubbles(page).filter({ hasText: note })).toHaveCount(1)
        await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
        await tabById(page, child.parentId).click()
        await expect(userBubbles(page).filter({ hasText: note })).toHaveCount(0)
      }
    }, async () => { await modelScript.releaseGateIfHeld(MUSE_CHILD_GATE) })
  })
}

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/517.
