import { Buffer } from 'node:buffer'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { expectGrokCopiedBytes } from './attachmentScenarios'

const scratchRoot = resolve(process.cwd(), '../.tmp')
let directory: string
let source: string
let copied: string
beforeEach(() => {
  mkdirSync(scratchRoot, { recursive: true })
  directory = mkdtempSync(join(scratchRoot, 'grok-copied-bytes-unit-'))
  source = join(directory, 'document.pdf')
  copied = join(directory, 'owned-document.pdf')
  writeFileSync(source, Buffer.from([0x25, 0x50, 0x44, 0x46, 0, 0xFF]))
  writeFileSync(copied, readFileSync(source))
})
afterEach(() => rmSync(directory, { recursive: true, force: true }))

function body(mimeType = 'application/pdf', size = 6, path = copied) {
  return { messages: [{ role: 'user', content: `<file_contents type="binary" path="${path}" mime_type="${mimeType}" size="${size}"/>` }] }
}

describe('expectGrokCopiedBytes', () => {
  it('reads the last user descriptor and verifies actual copied bytes', () => {
    const request = body()
    request.messages.unshift({ role: 'user', content: 'An earlier request contains no file.' })
    expect(() => expectGrokCopiedBytes(request, source, 'application/pdf')).not.toThrow()
    expect(readFileSync(copied)).toEqual(readFileSync(source))
  })

  // A body with no message list fails before the descriptor search. A message list with no user text fails the text check.
  it.each([
    [undefined, 'contains no messages array'],
    [null, 'contains no messages array'],
    [{}, 'contains no messages array'],
    [{ messages: [] }, 'the last user message of the native Grok request holds text'],
    [{ messages: [{ role: 'assistant', content: 'No user descriptor.' }] }, 'the last user message of the native Grok request holds text'],
  ])('rejects an absent native user descriptor: %j', (request, error) => {
    expect(() => expectGrokCopiedBytes(request, source, 'application/pdf')).toThrow(error)
  })

  it('rejects a mismatched native MIME value', () => {
    expect(() => expectGrokCopiedBytes(body('application/octet-stream'), source, 'application/pdf')).toThrow('the Grok copied-file descriptor states the media type of the source')
  })

  it('rejects a mismatched native byte size', () => {
    expect(() => expectGrokCopiedBytes(body('application/pdf', 5), source, 'application/pdf')).toThrow('the Grok copied-file descriptor states the size of the source')
  })

  it('rejects different copied bytes even when the byte size matches', () => {
    writeFileSync(copied, Buffer.from([0x25, 0x50, 0x44, 0x46, 0, 0xFE]))
    expect(() => expectGrokCopiedBytes(body(), source, 'application/pdf')).toThrow('the Grok copy holds the bytes of the source')
  })

  it('rejects an absent actual copied file', () => {
    rmSync(copied)
    expect(() => expectGrokCopiedBytes(body(), source, 'application/pdf')).toThrow(/ENOENT/)
  })

  it('rejects a copied path that loses the original basename', () => {
    const foreign = join(directory, 'foreign.pdf')
    writeFileSync(foreign, readFileSync(source))
    expect(() => expectGrokCopiedBytes(body('application/pdf', 6, foreign), source, 'application/pdf')).toThrow('the Grok copy keeps the name of the source file')
  })

  it('rejects the original source path as a native copied-file descriptor', () => {
    expect(() => expectGrokCopiedBytes(body('application/pdf', 6, source), source, 'application/pdf')).toThrow('distinct from the source file')
    expect(readFileSync(source)).toEqual(readFileSync(copied))
  })

  it('rejects a path alias to the original source file', () => {
    const alias = `${directory}/./document.pdf`
    expect(() => expectGrokCopiedBytes(body('application/pdf', 6, alias), source, 'application/pdf')).toThrow('distinct from the source file')
    expect(readFileSync(alias)).toEqual(readFileSync(source))
  })

  it('rejects a directory symlink to the original source file', () => {
    const link = join(directory, 'source-link')
    symlinkSync(directory, link, 'junction')
    const alias = join(link, 'document.pdf')
    expect(() => expectGrokCopiedBytes(body('application/pdf', 6, alias), source, 'application/pdf')).toThrow('distinct from the source file')
    expect(readFileSync(alias)).toEqual(readFileSync(source))
  })

  it('accepts a directory symlink to a distinct copied file', () => {
    const link = join(directory, 'copied-link')
    symlinkSync(directory, link, 'junction')
    const alias = join(link, 'owned-document.pdf')
    expect(() => expectGrokCopiedBytes(body('application/pdf', 6, alias), source, 'application/pdf')).not.toThrow()
    expect(readFileSync(alias)).toEqual(readFileSync(source))
  })

  it.each([
    { messages: 'not an array' },
    { messages: 7 },
    { messages: {} },
    { messages: null },
  ])('rejects a malformed native message collection with its validation error: %j', (request) => {
    expect(() => expectGrokCopiedBytes(request, source, 'application/pdf')).toThrow('native Grok request contains no messages array')
  })

  it.each([{ message: null }, { message: 7 }, { message: [] }, { message: { content: 'No role.' } }])('rejects a malformed native message entry with its validation error: $message', ({ message }) => {
    expect(() => expectGrokCopiedBytes({ messages: [message] }, source, 'application/pdf')).toThrow('native Grok request contains an invalid message')
  })
})
