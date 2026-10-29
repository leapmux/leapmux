import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { expectReasonixAttachmentBytes } from './attachmentScenario'

let directory: string
let source: string
let copy: string
const reference = '.reasonix/attachments/clipboard-20261029-123456.123456-000001.bin'
function request(content: unknown, role = 'user'): MockModelRequestRecord {
  return {
    protocol: 'openai-chat-completions',
    path: '/v1/chat/completions',
    body: {
      messages: [
        { role, content },
      ],
    },
  }
}

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'reasonix-attachment-proof-'))
  source = join(directory, 'uploaded.bin')
  copy = join(directory, reference)
  mkdirSync(join(directory, '.reasonix/attachments'), { recursive: true })
  const bytes = Uint8Array.from([0, 255, 1, 42, 0])
  writeFileSync(source, bytes)
  writeFileSync(copy, bytes)
})
afterEach(() => rmSync(directory, { recursive: true, force: true }))

describe('expectReasonixAttachmentBytes', () => {
  it('compares all bytes of the distinct native copy addressed by the current input', () => {
    expect(expectReasonixAttachmentBytes(request(`Inspect the upload.\n\n@${reference}\n`), source, directory)).toBe(copy)
  })

  it('rejects changed bytes, a missing copy, and another extension', () => {
    writeFileSync(copy, Uint8Array.from([0, 255, 1, 41, 0]))
    expect(() => expectReasonixAttachmentBytes(request(`@${reference}`), source, directory)).toThrow('the native attachment preserves every uploaded byte')
    rmSync(copy)
    expect(() => expectReasonixAttachmentBytes(request(`@${reference}`), source, directory)).toThrow('does not exist')
    const pdf = copy.replace('.bin', '.pdf')
    writeFileSync(pdf, Uint8Array.from([0, 255, 1, 42, 0]))
    expect(() => expectReasonixAttachmentBytes(request(`@${reference.replace('.bin', '.pdf')}`), source, directory)).toThrow('another file extension')
  })

  it.each([
    '',
    'filename.bin',
    `Quoted @${reference}`,
    `@${reference}\n@${reference}`,
    '@.reasonix/attachments/../private.bin',
  ])('rejects an absent or ambiguous native descriptor: %j', (content) => {
    expect(() => expectReasonixAttachmentBytes(request(content), source, directory)).toThrow('one native attachment path')
  })

  it.each(['assistant', 'tool'])('rejects an attachment found only in historical input before a %s row', (role) => {
    const input = request('unrelated', role)
    input.body = {
      messages: [
        { role: 'user', content: `@${reference}` },
        { role, content: 'unrelated' },
      ],
    }
    expect(() => expectReasonixAttachmentBytes(input, source, directory)).toThrow('current user input')
  })

  it.each([
    undefined,
    null,
    {},
    { messages: [] },
    {
      messages: [
        { role: 'user', content: [] },
      ],
    },
  ])('rejects malformed native input: %j', (body) => {
    expect(() => expectReasonixAttachmentBytes({
      ...request(''),
      body,
    }, source, directory)).toThrow('Reasonix')
  })

  it('rejects a copy that is the uploaded file itself', () => {
    expect(() => expectReasonixAttachmentBytes(request(`@${reference}`), copy, directory)).toThrow('differ from the uploaded source')
  })

  it.runIf(process.platform !== 'win32')('rejects a native path that resolves outside the workspace', () => {
    const outside = mkdtempSync(join(resolve(import.meta.dirname, '../../../../.tmp'), 'reasonix-external-attachment-'))
    try {
      const target = join(outside, 'external.bin')
      writeFileSync(target, 'external')
      rmSync(copy)
      symlinkSync(target, copy)
      expect(() => expectReasonixAttachmentBytes(request(`@${reference}`), source, directory)).toThrow('outside the E2E run')
    }
    finally {
      rmSync(outside, { recursive: true, force: true })
    }
  })
})
