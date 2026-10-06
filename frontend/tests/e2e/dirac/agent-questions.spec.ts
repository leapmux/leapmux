import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { WatchReplayMode } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { WatchEventsRequestSchema, WatchEventsResponseSchema, WatchMode } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { diracTest, expect } from '../dirac-fixtures'
import { getTestChannel } from '../helpers/api'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { controlButton, waitForSettingsHydrated } from '../helpers/ui'
import { exerciseQuestionReply } from './questionScenarios'
import { nativeContext } from './scenarios'

diracTest.describe('dirac agent questions', () => {
  diracTest('returns the selected form answer through the native question tool', async ({ askingDiracWorkspace, page, leapmuxServer, modelScript }, testInfo) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDiracWorkspace.workspaceId })
    await waitForSettingsHydrated(page)
    const agentId = await selectedAgentTabId(page)

    const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    const channelId = await channel.getOrOpenChannel(leapmuxServer.workerId)
    const request = create(WatchEventsRequestSchema, {
      agents: [{ agentId, mode: WatchMode.FULL, replay: WatchReplayMode.LATEST, cursorSeq: 0n }],
      updateId: 1n,
    })
    const watch = channel.stream(channelId, 'WatchEvents', toBinary(WatchEventsRequestSchema, request))
    const nativeControls: { requestId: string, payload: unknown, state: number }[] = []
    let subscribed = false
    let watchError: Error | undefined
    watch.onMessage((frame) => {
      try {
        const response = fromBinary(WatchEventsResponseSchema, frame.payload)
        if (response.event.case === 'updateAck') {
          if (response.event.value.rejectedAgents.length > 0)
            throw new Error('The Worker refused the Dirac control watch.')
          subscribed = response.event.value.updateId === 1n
          return
        }
        if (response.event.case !== 'agentEvent' || response.event.value.agentId !== agentId)
          return
        const event = response.event.value.event
        if (event.case !== 'controlRequest' && event.case !== 'controlResponseChanged')
          return
        nativeControls.push({
          requestId: event.value.requestId,
          payload: JSON.parse(new TextDecoder().decode(event.value.payload)),
          state: event.value.responseState,
        })
      }
      catch (error) {
        watchError = new Error('The Worker sent an invalid Dirac control event.', { cause: error })
      }
    })
    watch.onError(error => watchError = error)

    try {
      await expect.poll(() => {
        if (watchError)
          throw watchError
        return subscribed
      }).toBe(true)
      const answer = await exerciseQuestionReply(context, async (form) => {
        await testInfo.attach('dirac-question-form', { body: await form.evaluate(element => element.outerHTML), contentType: 'text/html' })
        await form.getByRole('button', { name: 'Choose an option *', exact: true }).click()
        await page.getByRole('menuitemradio', { name: 'Red', exact: true }).filter({ visible: true }).click()
        // A question form names its positive action Approve, and a permission names it Allow.
        const approve = controlButton(page, 'allow')
        await expect(approve).toHaveText('Approve')
        await approve.click()
      })
      expect(answer).toContain('Red')
      expect(answer).not.toContain('Blue')
      if (watchError)
        throw watchError
    }
    finally {
      watch.cancel()
      await testInfo.attach('dirac-native-question-controls', { body: JSON.stringify(nativeControls, null, 2), contentType: 'application/json' })
    }
  })
})

diracTest('returns a typed answer and refuses empty or whitespace-only text', async ({ askingDiracWorkspace, page, leapmuxServer, modelScript }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDiracWorkspace.workspaceId })
  const result = await exerciseQuestionReply(context, async (form) => {
    await form.getByRole('button', { name: 'Choose an option *', exact: true }).click()
    await page.getByRole('menuitemradio', { name: 'Other answer', exact: true }).filter({ visible: true }).click()
    const approve = controlButton(page, 'allow')
    await approve.click()
    await expect(form).toContainText('Answer')
    const answer = form.getByLabel('Answer *', { exact: true })
    await expect(approve).toBeDisabled()
    await answer.fill('   ')
    await expect(approve).toBeDisabled()
    await answer.fill('  Green  ')
    await expect(approve).toBeEnabled()
    await approve.click()
  })
  expect(result).toContain('<answer>\nGreen\n</answer>')
  expect(result).not.toContain('  Green  ')
  expect(result).not.toContain('Blue')
  expect(result).not.toContain('Red')
})

diracTest('returns native question cancellation without selecting an offered answer', async ({ askingDiracWorkspace, page, leapmuxServer, modelScript }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDiracWorkspace.workspaceId })
  const result = await exerciseQuestionReply(context, async () => {
    await page.locator('[data-testid="control-more-actions"]:visible').click()
    await page.getByRole('menuitem', { name: 'Cancel', exact: true }).filter({ visible: true }).click()
  })
  expect(result).toContain('The user declined to answer the follow-up question.')
  expect(result).not.toContain('<answer>')
})
