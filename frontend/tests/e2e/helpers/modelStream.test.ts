import { IncomingMessage, ServerResponse } from 'node:http'
import { Socket } from 'node:net'
import { describe, expect, it } from 'vitest'
import { createBufferedModelStream, createModelStream } from './modelStream'

function response(): ServerResponse {
  return new ServerResponse(new IncomingMessage(new Socket()))
}

async function collect(chunks: AsyncIterable<string>): Promise<string[]> {
  const values: string[] = []
  for await (const chunk of chunks)
    values.push(chunk)
  return values
}

describe('createModelStream', () => {
  it('counts reasoning and text in one ordered stream and holds after the last chunk', async () => {
    const held: string[] = []
    const stream = createModelStream(response(), { chunkChars: 2, delayMs: 0, gates: [
      { afterChunk: 1, name: 'thinking' },
      { afterChunk: 3, name: 'text' },
    ] }, async (name) => {
      held.push(name)
      return true
    })
    expect(await collect(stream.chunks('abcd'))).toEqual(['ab', 'cd'])
    expect(held).toEqual(['thinking'])
    expect(await collect(stream.chunks('ef'))).toEqual(['ef'])
    expect(held).toEqual(['thinking', 'text'])
    expect(stream.active).toBe(true)
  })

  it('holds a chunk until its gate releases', async () => {
    let release!: (released: boolean) => void
    let reached!: () => void
    const reachedGate = new Promise<void>(resolve => reached = resolve)
    const heldGate = new Promise<boolean>(resolve => release = resolve)
    const stream = createModelStream(response(), { chunkChars: 1, delayMs: 0, gates: [{ afterChunk: 1, name: 'hold' }] }, () => {
      reached()
      return heldGate
    })
    const chunks = stream.chunks('ab')
    expect(await chunks.next()).toEqual({ done: false, value: 'a' })
    let delivered = false
    const next = chunks.next().then((value) => {
      delivered = true
      return value
    })
    await reachedGate
    expect(delivered).toBe(false)
    release(true)
    expect(await next).toEqual({ done: false, value: 'b' })
    expect(await chunks.next()).toEqual({ done: true, value: undefined })
  })

  it('stops later fields after a cancelled gate', async () => {
    const stream = createModelStream(response(), { chunkChars: 1, delayMs: 0, gates: [{ afterChunk: 1, name: 'cancel' }] }, async () => false)
    expect(await collect(stream.chunks('ab'))).toEqual(['a'])
    expect(stream.active).toBe(false)
    expect(await collect(stream.chunks('later'))).toEqual([])
  })

  it('emits no chunks for an absent field and preserves an explicit empty field', async () => {
    const stream = createModelStream(response())
    expect(await collect(stream.chunks(undefined))).toEqual([])
    expect(await collect(stream.chunks(''))).toEqual([''])
    expect(await collect(stream.chunks('whole'))).toEqual(['whole'])
  })

  it('stops after the response ends', async () => {
    const output = response()
    const stream = createModelStream(output, { chunkChars: 1, delayMs: 0 })
    const chunks = stream.chunks('ab')
    expect(await chunks.next()).toEqual({ done: false, value: 'a' })
    output.end()
    expect(await chunks.next()).toEqual({ done: true, value: undefined })
    expect(stream.active).toBe(false)
  })

  it('refuses a release gate without a scenario handler before it emits output', () => {
    expect(() => createModelStream(response(), { chunkChars: 1, delayMs: 0, gates: [{ afterChunk: 1, name: 'missing' }] })).toThrow('scenario-owned gate handler')
  })
})

describe('createBufferedModelStream', () => {
  it('holds native actor generation at the stated combined reasoning and text chunks', async () => {
    const controller = new AbortController()
    const reached: string[] = []
    const stream = createBufferedModelStream(controller.signal, { chunkChars: 2, delayMs: 0, gates: [{ afterChunk: 3, name: 'thinking-end' }, { afterChunk: 4, name: 'answer-start' }] }, async (name) => {
      reached.push(name)
      return true
    })
    expect(await collect(stream.chunks('think'))).toEqual(['th', 'in', 'k'])
    expect(reached).toEqual(['thinking-end'])
    expect(await collect(stream.chunks('done'))).toEqual(['do', 'ne'])
    expect(reached).toEqual(['thinking-end', 'answer-start'])
    expect(stream.active).toBe(true)
  })

  it('stops buffered output when cancellation arrives during a native generation delay', async () => {
    const controller = new AbortController()
    const stream = createBufferedModelStream(controller.signal, { chunkChars: 1, delayMs: 60_000 })
    const chunks = stream.chunks('ab')
    expect(await chunks.next()).toEqual({ done: false, value: 'a' })
    const waiting = chunks.next()
    controller.abort()
    expect(await waiting).toEqual({ done: true, value: undefined })
    expect(await collect(stream.chunks('later'))).toEqual([])
  })

  it('starts no generation for an already cancelled native turn', async () => {
    const controller = new AbortController()
    controller.abort()
    const stream = createBufferedModelStream(controller.signal)
    expect(await collect(stream.chunks('never'))).toEqual([])
    expect(stream.active).toBe(false)
  })
})
