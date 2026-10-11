import { Buffer } from 'node:buffer'
import { describe, expect, it } from 'vitest'
import { createTimingLogReceiver } from './timingFixture'

describe('createTimingLogReceiver', () => {
  it('keeps a native JSON timing marker split across stream chunks', () => {
    let receivedAt = 10
    const receiver = createTimingLogReceiver(() => receivedAt)
    const marker = { marker: 'agent_startup_timing', phase: 'before_initialize', agent_id: 'actual-agent', time: '2026-09-30T00:00:00Z' }
    const line = JSON.stringify(marker)
    receiver.receive(Buffer.from(line.slice(0, 23)))
    receivedAt = 20
    receiver.receive(Buffer.from(`${line.slice(23)}\n`))
    expect(receiver.logLines).toEqual([{ raw: line, json: marker, rxAt: 20 }])
  })

  it('keeps complete JSON and non-JSON lines in receive order', () => {
    const receiver = createTimingLogReceiver(() => 30)
    receiver.receive(Buffer.from('plain backend output\n{"phase":"ready"}\r\n\n'))
    expect(receiver.logLines).toEqual([
      { raw: 'plain backend output', json: null, rxAt: 30 },
      { raw: '{"phase":"ready"}', json: { phase: 'ready' }, rxAt: 30 },
    ])
  })

  it('keeps stdout and stderr partial JSON records independent', () => {
    const receiver = createTimingLogReceiver(() => 40)
    receiver.receive(Buffer.from('{"phase":"stdout'), 'stdout')
    receiver.receive(Buffer.from('{"phase":"stderr'), 'stderr')
    receiver.receive(Buffer.from('"}\n'), 'stdout')
    receiver.receive(Buffer.from('"}\n'), 'stderr')
    expect(receiver.logLines.map(line => line.json)).toEqual([{ phase: 'stdout' }, { phase: 'stderr' }])
  })

  it('preserves UTF-8 and flushes final lines once at stream end', () => {
    const receiver = createTimingLogReceiver(() => 50)
    const text = Buffer.from('{"phase":"\u{1F6A7}"}')
    const glyph = text.indexOf(Buffer.from('\u{1F6A7}'))
    receiver.receive(text.subarray(0, glyph + 1), 'stderr')
    receiver.receive(text.subarray(glyph + 1), 'stderr')
    expect(receiver.logLines).toEqual([])
    receiver.end('stderr')
    receiver.end('stderr')
    expect(receiver.logLines).toEqual([{ raw: '{"phase":"\u{1F6A7}"}', json: { phase: '\u{1F6A7}' }, rxAt: 50 }])
  })
})
