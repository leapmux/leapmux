import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { AgentProvider, WatchReplayMode } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { WatchEventsRequestSchema, WatchEventsResponseSchema, WatchMode } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { DIRAC_E2E_SKIP_REASON, diracTest, expect } from '../dirac-fixtures'
import { getTestChannel } from '../helpers/api'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { askUserQuestionToolCall, diracRespondToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { exerciseQuestionReply } from './questionScenarios'
import { nativeContext } from './scenarios'

diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

diracTest.describe('dirac agent questions', () => {
  diracTest('returns the selected form answer through the native question tool', async ({ askingDiracWorkspace, page, leapmuxServer, modelScript }, testInfo) => {
    void askingDiracWorkspace
    await waitForSettingsHydrated(page)
    const agentId = await page.locator('[data-testid="tab"][data-tab-type="agent"]:visible').first().getAttribute('data-tab-id')
    if (!agentId)
      throw new Error('The Dirac question test needs an agent ID.')

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
      const callId = 'dirac-question'
      await modelScript.queue(
        { toolCalls: [askUserQuestionToolCall(AgentProvider.DIRAC, callId, [{
          question: 'Which color should I use?',
          header: 'Color',
          options: [{ label: 'Blue', description: 'Use blue.' }, { label: 'Red', description: 'Use red.' }],
        }])] },
        { toolCalls: [diracRespondToolCall('dirac-question-complete', 'complete', 'The question result was recorded.')] },
      )
      await sendMessage(page, modelScript.prompt('Ask me which color to use, then complete the task.'))
      await modelScript.waitForSteps(1)

      const banner = page.getByTestId('control-banner').filter({ visible: true })
      const form = banner.getByTestId('elicitation-form')
      await expect(banner).toContainText('Which color should I use?')
      await expect(form).toBeVisible()
      await testInfo.attach('dirac-question-form', { body: await form.evaluate(element => element.outerHTML), contentType: 'text/html' })
      await form.getByRole('button', { name: 'Choose an option *', exact: true }).click()
      await page.getByRole('menuitemradio', { name: 'Red', exact: true }).filter({ visible: true }).click()
      await page.getByTestId('control-actions').getByRole('button', { name: 'Approve', exact: true }).click()

      const status = await modelScript.waitForSteps(2)
      const answer = nativeToolResult(status.requests.find(record => record.stepIndex === 1), callId)
      expect(answer).toContain('Red')
      expect(answer).not.toContain('Blue')
      if (watchError)
        throw watchError
      await waitForAgentIdle(page)
      await expect(banner).toHaveCount(0)
      await expect(assistantBubbles(page).filter({ hasText: 'The question result was recorded.' }).first()).toBeVisible()
    }
    finally {
      watch.cancel()
      await testInfo.attach('dirac-native-question-controls', { body: JSON.stringify(nativeControls, null, 2), contentType: 'application/json' })
    }
  })
})

diracTest('returns a typed answer and refuses empty or whitespace-only text', async ({ askingDiracWorkspace, page, leapmuxServer, modelScript }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDiracWorkspace.workspaceId })
  const result = await exerciseQuestionReply(context, async () => {
    const form = page.locator('[data-testid="elicitation-form"]:visible')
    await form.getByRole('button', { name: 'Choose an option *', exact: true }).click()
    await page.getByRole('menuitemradio', { name: 'Other answer', exact: true }).filter({ visible: true }).click()
    await page.locator('[data-testid="control-allow-btn"]:visible').click()
    await expect(form).toContainText('Answer')
    const answer = form.getByLabel('Answer *', { exact: true })
    const approve = page.locator('[data-testid="control-allow-btn"]:visible')
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
