import { describe, expect, it } from 'vitest'
import { acpSupplementProtocol, acpSupplementRawOutput, acpSupplementTerminals, acpToolSupplement } from './toolSupplement'

const frame = { sessionUpdate: 'tool_call_update', toolCallId: 'call-1', status: 'completed' }

describe('acpToolSupplement', () => {
  it('answers the envelope that names this frame', () => {
    expect(acpToolSupplement(frame, { ...frame, title: 'Read' })).toEqual({ ...frame, title: 'Read' })
  })

  it('refuses an envelope stored beside another frame', () => {
    expect(acpToolSupplement(frame, { ...frame, toolCallId: 'call-2' })).toBeUndefined()
    expect(acpToolSupplement(frame, { ...frame, status: 'pending' })).toBeUndefined()
    expect(acpToolSupplement(frame, { ...frame, sessionUpdate: 'tool_call' })).toBeUndefined()
  })

  // A frame with no id identifies no call, so nothing can be matched to it.
  it('refuses a frame that names no call', () => {
    expect(acpToolSupplement({ sessionUpdate: 'tool_call' }, { sessionUpdate: 'tool_call' })).toBeUndefined()
    expect(acpToolSupplement({ ...frame, toolCallId: '' }, { ...frame, toolCallId: '' })).toBeUndefined()
  })

  it('refuses a supplement that is not an object', () => {
    expect(acpToolSupplement(frame, undefined)).toBeUndefined()
    expect(acpToolSupplement(frame, 'call-1')).toBeUndefined()
  })
})

describe('acpSupplementTerminals', () => {
  it('reads every field the worker stored for a terminal', () => {
    const terminals = acpSupplementTerminals({
      terminals: { 'term-1': { output: 'ok\n', truncated: true, exitCode: 3 } },
    })
    expect(terminals.get('term-1')).toEqual({ output: 'ok\n', truncated: true, exitCode: 3 })
  })

  // The worker writes one or the other, never both. Bytes that carry both are
  // ill-formed, and the SIGNAL is what such a process actually reported: a signalled
  // process has no status of its own, so any code beside it is noise.
  it('keeps one half of an ill-formed pair, and it is the signal', () => {
    const terminals = acpSupplementTerminals({
      terminals: { 'term-1': { output: '', truncated: false, exitCode: 3, signal: 'killed' } },
    })
    expect(terminals.get('term-1')).toEqual({ output: '', truncated: false, signal: 'killed' })
  })

  // The worker omits `exitCode` for a signalled process and `signal` for a normal
  // exit, so each one must be absent rather than answered as a default.
  it('leaves out the field the worker omitted', () => {
    expect(acpSupplementTerminals({ terminals: { a: { output: '', truncated: false, exitCode: 0 } } }).get('a'))
      .toEqual({ output: '', truncated: false, exitCode: 0 })
    expect(acpSupplementTerminals({ terminals: { a: { output: '', signal: 'terminated' } } }).get('a'))
      .toEqual({ output: '', truncated: false, signal: 'terminated' })
  })

  // An entry with no readable output is DROPPED, not coerced: the caller tells a
  // terminal it can read from one the host no longer holds, and `''` would read as a
  // command that printed nothing.
  it('drops an entry whose output is not text', () => {
    expect(acpSupplementTerminals({ terminals: { a: { output: 7 }, b: 'nope', c: { truncated: true } } }).size).toBe(0)
  })

  it('answers nothing for a supplement that stores no terminal', () => {
    expect(acpSupplementTerminals(undefined).size).toBe(0)
    expect(acpSupplementTerminals({ terminals: 'none' }).size).toBe(0)
  })

  // The AGENT chooses the terminal ids. A plain object answers `terminals.toString`
  // with a function off `Object.prototype`, and the caller then reads an entry whose
  // `output` is undefined through a field the IR declares as `string` -- which the
  // command body dereferences and crashes on.
  it.each(['toString', 'constructor', 'valueOf', 'hasOwnProperty', '__proto__'])('holds no entry for the inherited id %s', (id) => {
    expect(acpSupplementTerminals(undefined).get(id)).toBeUndefined()
    expect(acpSupplementTerminals({ terminals: { 'term-1': { output: 'ok' } } }).get(id)).toBeUndefined()
  })
})

describe('acp supplement payload readers', () => {
  it('answer only an object payload', () => {
    expect(acpSupplementProtocol({ protocol: { content: [] } })).toEqual({ content: [] })
    expect(acpSupplementProtocol({ protocol: 'x' })).toBeUndefined()
    expect(acpSupplementRawOutput({ rawOutput: { content: [] } })).toEqual({ content: [] })
    expect(acpSupplementRawOutput(undefined)).toBeUndefined()
  })
})
