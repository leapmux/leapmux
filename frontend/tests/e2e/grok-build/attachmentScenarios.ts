import { readFileSync, realpathSync } from 'node:fs'
import { basename } from 'node:path'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'

/**
 * Read Grok's own copied-file descriptor and prove its actual bytes.
 *
 * Grok copies an attached PDF or binary file and states the copy in the last user message, so an attachment spec
 * passes this check as the `proof` of `exerciseAttachmentDelivery`.
 */
export function expectGrokCopiedBytes(body: unknown, sourcePath: string, mimeType: string): void {
  if (!isObject(body) || !Array.isArray(body.messages))
    throw new Error('The native Grok request contains no messages array.')
  const messages = body.messages.map((message: unknown) => {
    if (!isObject(message) || typeof message.role !== 'string' || message.role === '')
      throw new Error('The native Grok request contains an invalid message.')
    return message
  })
  const userContent = messages.findLast(message => message.role === 'user')?.content
  expect(typeof userContent, 'the last user message of the native Grok request holds text').toBe('string')
  const match = /<file_contents type="binary" path="([^"]+)" mime_type="([^"]+)" size="(\d+)"\/>/.exec(String(userContent))
  if (!match)
    throw new Error('The native Grok request contains no copied-file descriptor.')
  const [, copiedPath, copiedMimeType, copiedSize] = match
  if (!copiedPath || copiedMimeType === undefined || copiedSize === undefined)
    throw new Error('The native Grok request contains an incomplete copied-file descriptor.')
  expect(basename(copiedPath).endsWith(basename(sourcePath)), 'the Grok copy keeps the name of the source file').toBe(true)
  if (realpathSync(copiedPath) === realpathSync(sourcePath))
    throw new Error('The native Grok copied file must be distinct from the source file.')
  expect(copiedMimeType, 'the Grok copied-file descriptor states the media type of the source').toBe(mimeType)
  const sourceBytes = readFileSync(sourcePath)
  expect(Number(copiedSize), 'the Grok copied-file descriptor states the size of the source').toBe(sourceBytes.length)
  expect(readFileSync(copiedPath), 'the Grok copy holds the bytes of the source').toEqual(sourceBytes)
}
