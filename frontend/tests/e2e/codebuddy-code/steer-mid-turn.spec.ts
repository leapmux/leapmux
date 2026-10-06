import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codebuddyTest, expect } from '../codebuddy-fixtures'
import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { attachFile, sendWithAttachment, writeAttachmentFixture } from '../helpers/attachments'
import { lastUserText } from '../helpers/mockModelScript'
import { bashToolCall } from '../helpers/providerToolCalls'
import { queuedInputRow, steerButton } from '../helpers/steer'
import { getRecordedToasts } from '../helpers/toast'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code steering', () => {
  codebuddyTest('steers queued text into the active native turn', async ({ authenticatedCodebuddyWorkspace, page, modelScript }) => {
    void authenticatedCodebuddyWorkspace
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

  codebuddyTest('does not send a queued image through native text-only steering', async ({ authenticatedCodebuddyWorkspace, page, modelScript }) => {
    void authenticatedCodebuddyWorkspace
    const gate = 'codebuddy-image-steer'
    await modelScript.queue(
      { gate, toolCalls: [bashToolCall(AgentProvider.CODEBUDDY, 'image-steer-shell', 'printf codebuddy-steer-ready')] },
      { text: 'I saw the queued text.' },
      { text: 'I saw the queued image.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted shell command, then reply.'))
    await modelScript.waitForGate(gate)

    const attachment = writeAttachmentFixture('image', 'codebuddy-steer.png')
    try {
      await attachFile(page, attachment)
      await sendWithAttachment(page, 'Also inspect the image I attached.')
      const queued = queuedInputRow(page, 'Also inspect the image')
      await expect(queued).toBeVisible()
      await steerButton(queued).click()
      // CodeBuddy's steer drain keeps the text of the blocks only, so the
      // worker refuses to steer an image. The refusal reaches the reader as a
      // warning toast, and only a failed steer shows one. The toast is the
      // barrier: after it, the input must still wait in the queue for the
      // next turn.
      await expect.poll(async () => (await getRecordedToasts(page)).some(toast => toast.variant === 'danger' && toast.message.includes('steer'))).toBe(true)
      await expect(queued).toBeVisible()
    }
    finally {
      await modelScript.releaseGate(gate)
    }

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    // Step 1 is the continuation of the FIRST turn, after its tool result.
    // The refused steer adds nothing to it.
    const continuation = status.requests.find(request => request.stepIndex === 1)
    expect(continuation?.protocol).toBe('openai-chat-completions')
    const continuationBody = JSON.stringify(continuation?.body)
    expect(continuationBody.includes('Also inspect the image')).toBe(false)
    expect(continuationBody.includes('codebuddy-steer.png')).toBe(false)

    // Step 2 is the NEXT turn. It carries the prompt, the label of the image
    // and the image itself, as CodeBuddy's own next-turn request does: a
    // user_query text part and a typed image_url part with the source bytes.
    const nextTurn = status.requests.find(request => request.stepIndex === 2)
    expect(nextTurn?.protocol).toBe('openai-chat-completions')
    expect(lastUserText(nextTurn?.body)).toContain('Also inspect the image I attached.')
    expect(lastUserText(nextTurn?.body)).toContain('Attached file "codebuddy-steer.png" (image/png)')
    await expectNativeAttachmentProof(page, status, 'image', attachment, 'openai-chat-completions', {}, 2)
    await expect(assistantBubbles(page).filter({ hasText: 'I saw the queued text.' })).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'I saw the queued image.' })).toBeVisible()
    await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(2)
  })
})
