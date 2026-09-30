import type { Page } from '@playwright/test'
import type { AttachmentKind } from './helpers/attachments'
import type { ModelScript } from './helpers/modelScriptFixture'
import { readFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { expect, GROK_E2E_SKIP_REASON, grokTest } from './grok-fixtures'
import { exerciseAttachmentDelivery } from './helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from './helpers/attachments'
import { createTestDirectory } from './helpers/runDirectory'
import { writeToolImage } from './helpers/toolImages'
import { assistantBubbles, expectUserMessage, waitForAgentIdle } from './helpers/ui'

type RequestCheck = (body: unknown, sourcePath: string) => void

async function exerciseGrokAttachment(
  page: Page,
  modelScript: ModelScript,
  kind: AttachmentKind,
  fileName: string,
  checkRequest: RequestCheck,
  fixturePath?: string,
): Promise<void> {
  await modelScript.queue({ text: 'Attachment received.' })
  const sourcePath = await expectAttachmentOutcome(page, kind, {
    supported: true,
    fileName,
    ...(fixturePath ? { fixturePath } : {}),
  })
  await sendWithAttachment(page, modelScript.prompt('Inspect the attached file.'))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page)

  const request = status.requests.find(record => record.stepIndex === 0)
  expect(request, 'the attached turn reached the model').toBeDefined()
  checkRequest(request?.body, sourcePath)
  await expectUserMessage(page, fileName)
  await expect(assistantBubbles(page).filter({ hasText: 'Attachment received.' }).first()).toBeVisible()
}

function expectGrokCopiedBytes(body: unknown, sourcePath: string, mimeType: string): void {
  const messages = (body as { messages?: { role?: string, content?: unknown }[] } | undefined)?.messages ?? []
  const userContent = messages.findLast(message => message.role === 'user')?.content
  expect(typeof userContent).toBe('string')
  const match = /<file_contents type="binary" path="([^"]+)" mime_type="([^"]+)" size="(\d+)"\/>/.exec(String(userContent))
  expect(match, 'the native request points at a copied file').not.toBeNull()
  const copiedPath = match![1]!
  expect(basename(copiedPath).endsWith(basename(sourcePath))).toBe(true)
  expect(match![2]).toBe(mimeType)
  const sourceBytes = readFileSync(sourcePath)
  expect(Number(match![3])).toBe(sourceBytes.length)
  expect(readFileSync(copiedPath)).toEqual(sourceBytes)
}

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

/**
 * 302 -- Grok Build attachments.
 *
 * Grok takes every attachment kind the matrix lists (text, image, PDF, binary),
 * so no kind is refused here.
 */
grokTest.describe('Grok Build attachments', () => {
  grokTest('accepts a text attachment and carries it through the turn', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'grok-notes.txt')
  })

  grokTest('accepts an image attachment and carries it through the turn', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    const directory = createTestDirectory('grok-attachment-image-')
    const fileName = writeToolImage(directory, 'grok-attachment')
    const imagePath = join(directory, fileName)
    await exerciseAttachmentDelivery(page, modelScript, 'image', fileName, { fixturePath: imagePath })
  })

  grokTest('accepts a PDF attachment and carries it through the turn', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    await exerciseGrokAttachment(page, modelScript, 'pdf', 'grok-doc.pdf', (body, sourcePath) => {
      expectGrokCopiedBytes(body, sourcePath, 'application/pdf')
    })
  })

  grokTest('accepts a binary attachment and carries it through the turn', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    await exerciseGrokAttachment(page, modelScript, 'binary', 'grok-blob.bin', (body, sourcePath) => {
      expectGrokCopiedBytes(body, sourcePath, 'application/macbinary')
    })
  })
})
