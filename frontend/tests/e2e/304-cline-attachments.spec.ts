import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { CLINE_E2E_SKIP_REASON, clineTest, expect } from './cline-fixtures'
import { expectAttachmentOutcome, sendWithAttachment } from './helpers/attachments'
import { readToolCall } from './helpers/providerToolCalls'
import { expectToolRowWithoutImage, writeToolImage } from './helpers/toolImages'
import { assistantBubbles, expectUserMessage, sendMessage, waitForAgentIdle } from './helpers/ui'

clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

const CLINE = AgentProvider.CLINE

/**
 * 304 -- Cline attachments and tool-result images.
 *
 * Cline takes text and image attachments (matrix). It takes no PDF and no other
 * binary kind. Its tool results are text only (matrix note 3), so a read of a
 * PNG draws the file name and no picture.
 */
clineTest.describe('Cline attachments', () => {
  // The file name states a word the prompt never gives, so the name in the user
  // message can only come from the attachment.
  clineTest('accepts a text attachment and carries it through the turn', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
    await modelScript.queue({ text: 'The note is attached.' })
    await expectAttachmentOutcome(page, 'text', { supported: true, fileName: 'cline-notes.txt' })
    await sendWithAttachment(page, modelScript.prompt('Read the attached note.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectUserMessage(page, 'cline-notes.txt')
    await expect(assistantBubbles(page).filter({ hasText: 'The note is attached.' }).first()).toBeVisible()
  })

  clineTest('accepts an image attachment and carries it through the turn', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
    await modelScript.queue({ text: 'The image is attached.' })
    await expectAttachmentOutcome(page, 'image', { supported: true, fileName: 'cline-shot.png' })
    await sendWithAttachment(page, modelScript.prompt('Describe the attached image.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectUserMessage(page, 'cline-shot.png')
    await expect(assistantBubbles(page).filter({ hasText: 'The image is attached.' }).first()).toBeVisible()
  })

  clineTest('refuses a PDF and a binary file', async ({ authenticatedClineWorkspace, page }) => {
    void authenticatedClineWorkspace
    await expectAttachmentOutcome(page, 'pdf', { supported: false })
    await expectAttachmentOutcome(page, 'binary', { supported: false })
  })
})

clineTest.describe('Cline images in tool results', () => {
  clineTest('a Read of a PNG draws the name and no picture in the tool row', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    const workingDir = authenticatedClineWorkspace.workingDir
    expect(workingDir, 'the agent workspace must expose a working directory').toBeTruthy()
    const name = writeToolImage(workingDir!, 'cline-19')
    const path = `${workingDir}/${name}`

    await modelScript.queue(
      { toolCalls: [readToolCall(CLINE, 'read-png', path)] },
      { text: `I opened ${name}.` },
    )
    await sendMessage(page, modelScript.prompt(`Read the file ${name} and describe it.`))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    // The name proves the tool ran. Cline builds tool results as text only, so
    // the row draws no picture (matrix note 3).
    await expectToolRowWithoutImage(page, 'tool-image-cline-19')
  })
})
