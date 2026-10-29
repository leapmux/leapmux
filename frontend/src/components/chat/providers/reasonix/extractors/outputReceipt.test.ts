import { describe, expect, it } from 'vitest'
import fixture from '../../../../../../../testdata/reasonix_output_receipt_conformance.json'
import { reasonixContentWithoutProvenance, reasonixFailedOutputFilePointer, reasonixOutputFilePointer } from './outputReceipt'

const path = '/native/reasonix/session.outputs/native-call.txt'
const receipt = `[bash output kept out of context: 6001 lines, 632 KB]\nFull output: ${path}\nRead a window of it with read_file (offset/limit), or search it with grep. It is not in this conversation.\n\nOpening lines:\nfirst output line\n`

describe('reasonixOutputFilePointer', () => {
  it('accepts the exact native receipt and a Windows path', () => {
    expect(reasonixOutputFilePointer(receipt, 'bash', 'native-call')).toBe(path)
    const windows = String.raw`C:\Users\Native User\session.outputs\native-call.txt`
    expect(reasonixOutputFilePointer(receipt.replace(path, windows), 'bash', 'native-call')).toBe(windows)
  })

  it('follows native filename sanitization and the maximum identity length', () => {
    const id = `日本語/${'a'.repeat(110)}`
    const filename = `${'a'.repeat(96)}.txt`
    expect(reasonixOutputFilePointer(receipt.replace('native-call.txt', filename), 'bash', id)).toBe(path.replace('native-call.txt', filename))
  })

  it.each(['callKid', 'callſid', 'call😀id'])('replaces every non-ASCII identity character in %s', (callId) => {
    const sanitized = path.replace('native-call.txt', 'call-id.txt')
    expect(reasonixOutputFilePointer(receipt.replace(path, sanitized), 'bash', callId)).toBe(sanitized)
  })

  it.each([
    undefined,
    null,
    0,
    '',
    {},
    [],
    `quoted\n${receipt}`,
    receipt.replace('[bash', '[read_file'),
    receipt.replace('6001 lines', '0 lines'),
    receipt.replace('Read a window', 'Read another window'),
  ])('rejects an absent, quoted, or altered native receipt: %j', (value) => {
    expect(reasonixOutputFilePointer(value, 'bash', 'native-call')).toBeUndefined()
  })

  it.each([
    'relative/native-call.txt',
    'https://example.com/native-call.txt',
    'file:///native-call.txt',
    '/native/other-call.txt',
    '/native/native-call.txt\r',
    '/native/zero\0/native-call.txt',
  ])('rejects a foreign or non-filesystem path: %j', (invalid) => {
    expect(reasonixOutputFilePointer(receipt.replace(path, invalid), 'bash', 'native-call')).toBeUndefined()
  })

  it('requires an exact tool and call identity', () => {
    expect(reasonixOutputFilePointer(receipt, 'read_file', 'native-call')).toBeUndefined()
    expect(reasonixOutputFilePointer(receipt, 'bash', 'other')).toBeUndefined()
    expect(reasonixOutputFilePointer(receipt, '', 'native-call')).toBeUndefined()
    expect(reasonixOutputFilePointer(receipt, 'bash', '')).toBeUndefined()
  })
})

describe('reasonix output receipt conformance', () => {
  it('loads a nonempty shared corpus', () => {
    expect(fixture.cases.length).toBeGreaterThan(0)
  })

  it.each(fixture.cases)('$name', ({ text, normalized, tool, callId, path, headline, failedPath }) => {
    expect(reasonixContentWithoutProvenance(text)).toBe(normalized)
    expect(reasonixOutputFilePointer(text, tool, callId) ?? '').toBe(path)
    expect(reasonixFailedOutputFilePointer(text, tool, callId, headline) ?? '').toBe(failedPath)
  })
})
