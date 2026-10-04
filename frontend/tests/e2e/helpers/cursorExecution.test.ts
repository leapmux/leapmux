import { Buffer } from 'node:buffer'
import { describe, expect, it } from 'vitest'
import { CursorExecution, cursorExecutionCallFrom } from './cursorExecution'
import { descend, encodeLengthDelimited, encodeStringField, encodeVarint, readLengthDelimitedFields } from './cursorWire'

function integer(field: number, value: number): Uint8Array {
  return Buffer.concat([encodeVarint(field * 8), encodeVarint(value)])
}

function reply(id: number, field: number, outcome: number, fields: readonly Uint8Array[], execID = 'call'): Uint8Array {
  return encodeLengthDelimited(2, Buffer.concat([
    integer(1, id),
    encodeStringField(15, execID),
    encodeLengthDelimited(field, encodeLengthDelimited(outcome, Buffer.concat(fields))),
  ]))
}

describe('CursorExecution', () => {
  it('keeps the actual shell output and exit code after the matching client reply', () => {
    const execution = new CursorExecution({ kind: 'shell', callID: 'call', command: 'printf actual' }, () => 9)
    expect(descend(execution.request(), [2, 2])).toBeDefined()
    const completed = execution.acceptReply(reply(9, 2, 2, [integer(3, 7), encodeStringField(5, 'actual stdout'), encodeStringField(6, 'actual stderr')]))
    expect(completed?.state).toBe('completed')
    if (completed?.state !== 'completed')
      throw new Error('the native shell reply did not complete')
    expect(completed.receipt).toMatchObject({ success: false, exitCode: 7 })
    expect(completed.receipt.text).toContain('actual stdout')
    expect(completed.receipt.text).toContain('actual stderr')
    expect(() => execution.request()).toThrow('already completed')
  })

  it('waits for real read and write replies before completing a new file', () => {
    let id = 0
    const execution = new CursorExecution({ kind: 'write', callID: 'call', path: '/project/new.txt', content: 'actual file\n' }, () => ++id)
    expect(descend(execution.request(), [2, 7])).toBeDefined()
    const next = execution.acceptReply(reply(1, 7, 4, [encodeStringField(1, '/project/new.txt')]))
    expect(next?.state).toBe('request')
    if (next?.state !== 'request')
      throw new Error('the absent native file did not start its write')
    const write = descend(next.request, [2, 3])
    expect(write).toBeDefined()
    const fields = readLengthDelimitedFields(write!)
    expect(new TextDecoder().decode(fields.get(2)?.[0])).toBe('actual file\n')
    const completed = execution.acceptReply(reply(2, 3, 1, [encodeStringField(1, '/project/new.txt'), encodeStringField(4, 'actual file\n')]))
    expect(completed?.state).toBe('completed')
    if (completed?.state !== 'completed')
      throw new Error('the native write did not complete')
    expect(completed.receipt).toMatchObject({ success: true, kind: 'write' })
  })

  it('checks the actual file after a write reply that omits file content', () => {
    let id = 0
    const execution = new CursorExecution({ kind: 'write', callID: 'call', path: '/project/new.txt', content: '' }, () => ++id)
    execution.acceptReply(reply(1, 7, 4, [encodeStringField(1, '/project/new.txt')]))
    const check = execution.acceptReply(reply(2, 3, 1, [encodeStringField(1, '/project/new.txt')]))
    expect(check?.state).toBe('request')
    if (check?.state !== 'request')
      throw new Error('the native write did not request its content check')
    expect(descend(check.request, [2, 7])).toBeDefined()
    const completed = execution.acceptReply(reply(3, 7, 1, [encodeStringField(1, '/project/new.txt'), encodeStringField(2, '')]))
    expect(completed?.state).toBe('completed')
  })

  it('uses actual read content for an exact targeted edit', () => {
    let id = 0
    const execution = new CursorExecution({ kind: 'edit', callID: 'call', path: '/project/edit.txt', before: 'old', after: 'new' }, () => ++id)
    const next = execution.acceptReply(reply(1, 7, 1, [encodeStringField(1, '/project/edit.txt'), encodeStringField(2, 'prefix old suffix\n')]))
    if (next?.state !== 'request')
      throw new Error('the native edit did not request a write')
    const fields = readLengthDelimitedFields(descend(next.request, [2, 3])!)
    expect(new TextDecoder().decode(fields.get(2)?.[0])).toBe('prefix new suffix\n')
  })

  it.each(['no requested target', 'old and old'])('refuses a nonunique edit target in %s without writing', (content) => {
    let id = 0
    const execution = new CursorExecution({ kind: 'edit', callID: 'call', path: '/project/edit.txt', before: 'old', after: 'new' }, () => ++id)
    const completed = execution.acceptReply(reply(1, 7, 1, [encodeStringField(1, '/project/edit.txt'), encodeStringField(2, content)]))
    expect(completed?.state).toBe('completed')
    if (completed?.state !== 'completed')
      throw new Error('the refused edit did not complete')
    expect(completed.receipt.success).toBe(false)
    expect(id).toBe(1)
  })

  it('rejects a different client request ID', () => {
    const execution = new CursorExecution({ kind: 'read', callID: 'call', path: '/project/a.txt' }, () => 4)
    expect(() => execution.acceptReply(reply(5, 7, 1, [encodeStringField(2, 'other')]))).toThrow('with id 5')
  })

  it('rejects a different tool ID or operation', () => {
    const execution = new CursorExecution({ kind: 'read', callID: 'call', path: '/project/a.txt' }, () => 4)
    expect(() => execution.acceptReply(reply(4, 7, 1, [encodeStringField(2, 'other')], 'different'))).toThrow('different tool call ID')
    expect(() => execution.acceptReply(reply(4, 3, 1, [encodeStringField(4, 'other')]))).toThrow('different operation')
  })

  it('ignores a heartbeat without completing the operation', () => {
    const execution = new CursorExecution({ kind: 'read', callID: 'call', path: '/project/a.txt' }, () => 4)
    expect(execution.acceptReply(encodeLengthDelimited(7, new Uint8Array(0)))).toBeUndefined()
    expect(descend(execution.request(), [2, 7])).toBeDefined()
  })

  it('rejects file content that differs from the actual requested write', () => {
    let id = 0
    const execution = new CursorExecution({ kind: 'write', callID: 'call', path: '/project/new.txt', content: 'wanted' }, () => ++id)
    execution.acceptReply(reply(1, 7, 4, [encodeStringField(1, '/project/new.txt')]))
    expect(() => execution.acceptReply(reply(2, 3, 1, [encodeStringField(4, 'different')]))).toThrow('different file contents')
  })
})

describe('cursorExecutionCallFrom', () => {
  it('preserves empty file content and edit replacement values', () => {
    expect(cursorExecutionCallFrom({ id: 'call', name: 'write', arguments: { path: '/project/a.txt', content: '' } }, 'write')).toMatchObject({ content: '' })
    expect(cursorExecutionCallFrom({ id: 'call', name: 'edit', arguments: { path: '/project/a.txt', before: 'old', after: '' } }, 'edit')).toMatchObject({ after: '' })
  })

  it('refuses absent IDs and required arguments', () => {
    expect(() => cursorExecutionCallFrom({ id: '', name: 'shell', arguments: { command: 'pwd' } }, 'shell')).toThrow('ID and arguments')
    expect(() => cursorExecutionCallFrom({ id: 'call', name: 'shell', arguments: { command: '' } }, 'shell')).toThrow('requires a command')
    expect(() => cursorExecutionCallFrom({ id: 'call', name: 'read', arguments: { path: '' } }, 'read')).toThrow('requires a path')
  })
})
