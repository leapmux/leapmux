import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, expect } from '../codebuddy-fixtures'
import { attachFile, sendWithAttachment, writeAttachmentFixture } from '../helpers/attachments'
import { lastUserText } from '../helpers/mockModelScript'
import { bashToolCall } from '../helpers/providerToolCalls'
import { queuedInputRow, steerButton } from '../helpers/steer'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code steering', () => {
  codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

  codebuddyTest('steers queued text into the active native turn', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    const gate = 'codebuddy-text-steer'
    await modelScript.queue(
      { gate, toolCalls: [bashToolCall(AgentProvider.CODEBUDDY, 'steer-shell', 'printf codebuddy-steer-ready')] },
      { text: 'I saw the steered instruction.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted shell command, then reply.'))
    await modelScript.waitForGate(gate)

    try {
      await sendMessage(page, 'Also inspect the queued instruction.')
      const queued = queuedInputRow(page, 'Also inspect the queued instruction.')
      await expect(queued).toBeVisible()
      await steerButton(queued).click()
      await expect(queued).toHaveCount(0)
    }
    finally {
      await modelScript.releaseGate(gate)
    }

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const second = status.requests.find(request => request.stepIndex === 1)
    expect(second?.protocol).toBe('openai-chat-completions')
    const body = JSON.stringify(second?.body)
    expect(body.includes('message-queue')).toBe(true)
    expect(body.includes('Also inspect the queued instruction.')).toBe(true)
    await expect(assistantBubbles(page).filter({ hasText: 'I saw the steered instruction.' })).toBeVisible()
    await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(1)
  })

  codebuddyTest('does not send a queued image through native text-only steering', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    const gate = 'codebuddy-image-steer'
    await modelScript.queue(
      { gate, toolCalls: [bashToolCall(AgentProvider.CODEBUDDY, 'image-steer-shell', 'printf codebuddy-steer-ready')] },
      { text: 'I saw the queued text.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted shell command, then reply.'))
    await modelScript.waitForGate(gate)

    try {
      const attachment = writeAttachmentFixture('image', 'codebuddy-steer.png')
      await attachFile(page, attachment)
      await sendWithAttachment(page, 'Also inspect the image I attached.')
      const queued = queuedInputRow(page, 'Also inspect the image')
      await expect(queued).toBeVisible()
      await steerButton(queued).click()
      await expect(queued).toHaveCount(0)
    }
    finally {
      await modelScript.releaseGate(gate)
    }

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const second = status.requests.find(request => request.stepIndex === 1)
    expect(second?.protocol).toBe('openai-chat-completions')
    const body = JSON.stringify(second?.body)
    expect(lastUserText(second?.body)).toContain('Also inspect the image I attached.')
    expect(lastUserText(second?.body)).toContain('Attached file "codebuddy-steer.png" (image/png)')
    expect(body.includes('data:image/png;base64,iVBORw0KGgo')).toBe(false)
    await expect(assistantBubbles(page).filter({ hasText: 'I saw the queued text.' })).toBeVisible()
    await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(1)
  })
})
