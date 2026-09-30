import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { CLINE_E2E_SKIP_REASON, clineTest, expect } from './cline-fixtures'
import { exerciseAttachmentDelivery, expectRefusedAttachmentsAbsent } from './helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from './helpers/attachments'
import { readToolCall } from './helpers/providerToolCalls'
import { expectToolRowWithoutImage, writeToolImage } from './helpers/toolImages'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

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
  clineTest('accepts a text attachment and carries it through the turn', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'cline-notes.txt')
  })

  clineTest('accepts an image attachment and carries it through the turn', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'image', 'cline-shot.png')
  })

  clineTest('refuses a PDF and a binary file', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
    const pdf = await expectAttachmentOutcome(page, 'pdf', { supported: false })
    const binary = await expectAttachmentOutcome(page, 'binary', { supported: false })
    await expectRefusedAttachmentsAbsent(page, modelScript, [pdf, binary])
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
