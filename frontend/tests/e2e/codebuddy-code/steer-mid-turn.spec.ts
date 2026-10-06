import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codebuddyTest, expect } from '../codebuddy-fixtures'
import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { attachFile, sendWithAttachment, writeAttachmentFixture } from '../helpers/attachments'
import { lastUserText } from '../helpers/mockModelScript'
import { exerciseSteerBeforeTool } from '../helpers/nativeToolSteering'
import { bashToolCall } from '../helpers/providerToolCalls'
import { queuedInputRow, steerButton } from '../helpers/steer'
import { getRecordedToasts } from '../helpers/toast'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code steering', () => {
  codebuddyTest('steers queued text into the active native turn', async ({ native }) => {
    const steered = await exerciseSteerBeforeTool(native)
    // CodeBuddy drains a steered message from its message queue into the request after the tool step.
    expect(steered.protocol).toBe('openai-chat-completions')
    expect(JSON.stringify(steered.body)).toContain('message-queue')
  })

  codebuddyTest('does not send a queued image through native text-only steering', async ({ authenticatedCodebuddyWorkspace, page, modelScript }) => {
    void authenticatedCodebuddyWorkspace
    const gate = 'codebuddy-image-steer'
    const start = await modelScript.queue(
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

    const status = await modelScript.waitForSteps(start + 3)
    await waitForAgentIdle(page)

    // The step after the tool step is the continuation of the FIRST turn,
    // after its tool result. The refused steer adds nothing to it.
    const continuation = await modelScript.requestAt(start + 1)
    expect(continuation.protocol).toBe('openai-chat-completions')
    const continuationBody = JSON.stringify(continuation.body)
    expect(continuationBody.includes('Also inspect the image')).toBe(false)
    expect(continuationBody.includes('codebuddy-steer.png')).toBe(false)

    // The step after it is the NEXT turn. It carries the prompt, the label of
    // the image and the image itself, as CodeBuddy's own next-turn request does:
    // a user_query text part and a typed image_url part with the source bytes.
    const nextTurn = await modelScript.requestAt(start + 2)
    expect(nextTurn.protocol).toBe('openai-chat-completions')
    expect(lastUserText(nextTurn.body)).toContain('Also inspect the image I attached.')
    expect(lastUserText(nextTurn.body)).toContain('Attached file "codebuddy-steer.png" (image/png)')
    await expectNativeAttachmentProof(page, status, 'image', attachment, { protocol: 'openai-chat-completions', stepIndex: start + 2 })
    await expect(assistantBubbles(page).filter({ hasText: 'I saw the queued text.' })).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'I saw the queued image.' })).toBeVisible()
    await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(2)
  })
})
