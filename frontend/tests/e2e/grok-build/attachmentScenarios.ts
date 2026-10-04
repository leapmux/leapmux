import type { Page } from '@playwright/test'
import type { AttachmentKind } from '../helpers/attachments'
import type { ModelScript } from '../helpers/modelScriptFixture'
import { readFileSync, realpathSync } from 'node:fs'
import { basename } from 'node:path'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { assistantBubbles, expectUserMessage, waitForAgentIdle } from '../helpers/ui'

type RequestCheck = (body: unknown, sourcePath: string) => void

/** Run an actual Grok attachment turn and retain its model, source, and UI guards. */
export async function exerciseGrokAttachment(
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

/** Read Grok's own copied-file descriptor and prove its actual bytes. */
export function expectGrokCopiedBytes(body: unknown, sourcePath: string, mimeType: string): void {
  if (!isObject(body) || !Array.isArray(body.messages))
    throw new Error('The native Grok request contains no messages array.')
  const messages = body.messages.map((message: unknown) => {
    if (!isObject(message) || typeof message.role !== 'string' || message.role === '')
      throw new Error('The native Grok request contains an invalid message.')
    return message
  })
  const userContent = messages.findLast(message => message.role === 'user')?.content
  expect(typeof userContent).toBe('string')
  const match = /<file_contents type="binary" path="([^"]+)" mime_type="([^"]+)" size="(\d+)"\/>/.exec(String(userContent))
  if (!match)
    throw new Error('The native Grok request contains no copied-file descriptor.')
  const [, copiedPath, copiedMimeType, copiedSize] = match
  if (!copiedPath || copiedMimeType === undefined || copiedSize === undefined)
    throw new Error('The native Grok request contains an incomplete copied-file descriptor.')
  expect(basename(copiedPath).endsWith(basename(sourcePath))).toBe(true)
  if (realpathSync(copiedPath) === realpathSync(sourcePath))
    throw new Error('The native Grok copied file must be distinct from the source file.')
  expect(copiedMimeType).toBe(mimeType)
  const sourceBytes = readFileSync(sourcePath)
  expect(Number(copiedSize)).toBe(sourceBytes.length)
  expect(readFileSync(copiedPath)).toEqual(sourceBytes)
}
