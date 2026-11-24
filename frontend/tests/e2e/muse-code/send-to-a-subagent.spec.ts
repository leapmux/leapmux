import { expect } from '@playwright/test'
import { AgentInputState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { withCleanup } from '../helpers/cleanup'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { nativeModelContextText } from '../helpers/nativeScenario'
import { museSubmitResultToolCall } from '../helpers/providerToolCalls'
import { queuedInputRow, steerButton } from '../helpers/steer'
import { assistantBubbles, expectRowsInOrder, messageContents, tabById, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { museTest } from '../muse-fixtures'
import { MUSE_AGENT } from './scenarios'
import { attachMuseChildReadinessFailure, MUSE_CHILD_GATE, openMuseChild } from './subagent-common.muse'

for (const active of [true, false]) {
  museTest(`returns the exact reply to a human note sent to the ${active ? 'running' : 'completed'} native child`, async ({ native }, testInfo) => {
    const { page, modelScript } = native
    const note = `Read MUSE_CHILD_HUMAN_${active ? 'ACTIVE' : 'IDLE'} in your own session.`
    const answer = `The ${active ? 'running' : 'completed'} child read its human note.`
    const before = 'The child will run its second command.'
    const rule = 'the Muse child reads its human input'
    await withCleanup(async () => {
      // Muse admits completed-child followup only in a trusted workspace.
      if (!active) {
        const { agentId } = await openProviderAgent(native.leapmuxServer, native.workspaceId, MUSE_AGENT, {
          optionValues: { workspaceTrust: 'agent', permissionMode: 'allowAll' },
        })
        await tabById(page, agentId).click()
      }
      const child = await openMuseChild(native, active, active ? before : undefined)
      const cli = await mintCLITokenForAdmin(native.leapmuxServer)
      await modelScript.rule({
        name: rule,
        when: {
          body: ['Workflow child completion protocol', note],
        },
        once: true,
        respond: {
          text: answer,
          toolCalls: [
            museSubmitResultToolCall('muse-child-note-submit', 'The human note was processed.'),
          ],
        },
      })
      const args = [
        'agent',
        'send',
        child.childId,
        note,
        '--request-id',
        'muse-child-human-note',
        '--wait',
        '--all',
        '--timeout',
        '25s',
      ]
      const steer = async () => {
        const queued = queuedInputRow(page, note)
        await steerButton(queued).click()
        await expect(queued.and(page.locator(`:not([data-input-state="${AgentInputState.AWAITING_ECHO}"])`))).toHaveCount(0)
        await child.finish()
      }
      try {
        const [result] = await Promise.all([
          runCLI(cli, args) as Promise<{ input_id: string }>,
          active
            ? steer()
            : Promise.resolve(),
        ])
        await expect.poll(async () => (await modelScript.status()).ruleMatches[rule] ?? 0).toBe(1)
        expect(result).toMatchObject({
          input_id: expect.any(String),
          turn_outcome: 'completed',
          replies: [answer],
        })
        expect(await runCLI(cli, args)).toMatchObject({
          input_id: result.input_id,
          turn_outcome: 'completed',
          replies: [answer],
        })
      }
      catch (error) {
        await attachMuseChildReadinessFailure(native, {
          parentId: child.parentId,
          childId: child.childId,
        }, testInfo).catch(() => {})
        throw error
      }
      await waitForAgentIdle(page)
      const request = (await modelScript.status()).requests.find(request => request.rule === rule)
      if (!request)
        throw new Error('The native child did not consume its human note.')
      expect(nativeModelContextText(request)).toContain(note)
      await testInfo.attach('muse-child-input-request', {
        body: JSON.stringify({ childId: child.childId, parentId: child.parentId, request }),
        contentType: 'application/json',
      })
      for (const reload of [false, true]) {
        if (reload)
          await page.reload()
        await tabById(page, child.childId).click()
        await expect(userBubbles(page).filter({ hasText: note })).toHaveCount(1)
        await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
        if (active) {
          await expectRowsInOrder(messageContents(page), [
            before,
            note,
            answer,
          ])
        }
        await tabById(page, child.parentId).click()
        await expect(userBubbles(page).filter({ hasText: note })).toHaveCount(0)
      }
    }, async () => { await modelScript.releaseGateIfHeld(MUSE_CHILD_GATE) })
  })
}

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/517.
