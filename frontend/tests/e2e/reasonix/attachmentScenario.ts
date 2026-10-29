import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { readFileSync, realpathSync } from 'node:fs'
import { extname, resolve } from 'node:path'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'

/** Prove that the current prompt addresses a distinct native copy with every source byte. */
export function expectReasonixAttachmentBytes(request: MockModelRequestRecord, sourcePath: string, workingDir: string): string {
  if (request.protocol !== 'openai-chat-completions' || !isObject(request.body) || !Array.isArray(request.body.messages))
    throw new Error('The Reasonix attachment proof requires its native messages.')
  const last = request.body.messages.findLast((row: unknown) => isObject(row) && row.role !== 'system' && row.role !== 'developer')
  if (!isObject(last) || last.role !== 'user' || typeof last.content !== 'string')
    throw new Error('The Reasonix attachment must belong to the current user input.')
  const copies = [
    ...last.content.matchAll(/^@(\.reasonix\/attachments\/clipboard-[a-z0-9.-]+)$/gim),
  ]
  if (copies.length !== 1 || !copies[0]?.[1])
    throw new Error('The current Reasonix input requires one native attachment path.')
  const path = resolve(workingDir, copies[0][1])
  assertPrivateNativePath(path, workingDir)
  if (realpathSync(path) === realpathSync(sourcePath))
    throw new Error('The native Reasonix attachment must differ from the uploaded source.')
  if (extname(path) !== extname(sourcePath))
    throw new Error('The native Reasonix attachment has another file extension.')
  expect(readFileSync(path), 'the native attachment preserves every uploaded byte').toEqual(readFileSync(sourcePath))
  return path
}
