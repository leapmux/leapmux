import type { IncomingHttpHeaders, IncomingMessage } from 'node:http'
import type { AddressInfo, Socket } from 'node:net'
import type { WebSocketConnection } from './webSocketServer'
import { Buffer } from 'node:buffer'
import { randomBytes } from 'node:crypto'
import { createServer } from 'node:http'
import { connect } from 'node:net'
import { Duplex } from 'node:stream'
import { setImmediate } from 'node:timers'
import { afterEach, describe, expect, it } from 'vitest'
import { acceptWebSocket, CLOSE_CODE, encodeFrame, MAX_WEBSOCKET_MESSAGE_BYTES, readClientFrame, webSocketAcceptKey } from './webSocketServer'

/** A server that accepts every upgrade and hands each connection to the test. */
async function startServer(protocols?: readonly string[]) {
  const accepted: WebSocketConnection[] = []
  const waiters: ((connection: WebSocketConnection) => void)[] = []
  const server = createServer((_request, response) => response.writeHead(404).end())
  // An upgraded socket leaves the HTTP server's own tracking, so the close ends it here.
  const sockets: Duplex[] = []
  server.on('upgrade', (request, socket, head) => {
    sockets.push(socket)
    const connection = acceptWebSocket(request, socket, head, protocols ? { protocols } : {})
    if (!connection)
      return
    const waiter = waiters.shift()
    if (waiter)
      waiter(connection)
    else
      accepted.push(connection)
  })
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve))
  const { port } = server.address() as AddressInfo
  return {
    url: `ws://127.0.0.1:${port}/actors/socket`,
    port,
    next: () => new Promise<WebSocketConnection>((resolve) => {
      const ready = accepted.shift()
      if (ready)
        resolve(ready)
      else
        waiters.push(resolve)
    }),
    close: () => new Promise<void>((resolve) => {
      for (const socket of sockets)
        socket.destroy()
      server.closeAllConnections()
      server.close(() => resolve())
    }),
  }
}

type TestServer = Awaited<ReturnType<typeof startServer>>
let current: TestServer | undefined

afterEach(async () => {
  await current?.close()
  current = undefined
})

/** A masked client frame, as a browser would send it. */
function clientFrame(opcode: number, payload: Buffer, fin = true): Buffer {
  const mask = randomBytes(4)
  const masked = Buffer.from(payload)
  for (let index = 0; index < masked.byteLength; index++)
    masked[index] = masked[index]! ^ mask[index % 4]!
  const unmasked = encodeFrame(opcode, masked, fin)
  // Insert the mask after the length field and set the mask bit.
  const headerLength = unmasked.byteLength - masked.byteLength
  const header = Buffer.from(unmasked.subarray(0, headerLength))
  header[1] = header[1]! | 0x80
  return Buffer.concat([header, mask, masked])
}

/** Open a raw connection and complete the handshake by hand. */
async function rawConnection(port: number, extraHeaders = ''): Promise<{ socket: Socket, response: string }> {
  const socket = connect(port, '127.0.0.1')
  await new Promise<void>(resolve => socket.once('connect', resolve))
  const key = randomBytes(16).toString('base64')
  socket.write(`GET /actors/socket HTTP/1.1\r\nHost: 127.0.0.1\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: ${key}\r\nSec-WebSocket-Version: 13\r\n${extraHeaders}\r\n`)
  const response = await new Promise<string>((resolve) => {
    let text = ''
    const onData = (chunk: Buffer) => {
      text += chunk.toString('latin1')
      if (text.includes('\r\n\r\n')) {
        socket.off('data', onData)
        resolve(text)
      }
    }
    socket.on('data', onData)
  })
  return { socket, response }
}

describe('webSocketAcceptKey', () => {
  // The example of RFC 6455, section 1.3.
  it('answers the key of the RFC\'s own example', () => {
    expect(webSocketAcceptKey('dGhlIHNhbXBsZSBub25jZQ==')).toBe('s3pPLMBiTxaQ9kYGzzhZRbK+xOo=')
  })
})

describe('readClientFrame', () => {
  it('reads a masked frame of each length form', () => {
    for (const size of [0, 5, 125, 126, 65_535, 65_536]) {
      const payload = Buffer.alloc(size, 0x61)
      const read = readClientFrame(clientFrame(0x1, payload))
      expect(read.kind, String(size)).toBe('frame')
      if (read.kind === 'frame')
        expect(read.frame.payload.equals(payload), String(size)).toBe(true)
    }
  })

  it('waits for the rest of a frame that arrived in part', () => {
    const frame = clientFrame(0x1, Buffer.from('hello'))
    for (let cut = 0; cut < frame.byteLength; cut++)
      expect(readClientFrame(frame.subarray(0, cut)).kind, String(cut)).toBe('incomplete')
  })

  // A frame exactly at the cap is accepted. It waits for its payload, and one byte
  // more is refused before any byte of it arrives.
  it('takes a frame of exactly the cap, and refuses one byte more', () => {
    const header = (length: number) => {
      const bytes = Buffer.alloc(10)
      bytes[0] = 0x81
      bytes[1] = 0x80 | 127
      bytes.writeBigUInt64BE(BigInt(length), 2)
      return bytes
    }
    expect(readClientFrame(header(MAX_WEBSOCKET_MESSAGE_BYTES)).kind).toBe('incomplete')
    expect(readClientFrame(header(MAX_WEBSOCKET_MESSAGE_BYTES + 1))).toMatchObject({ kind: 'error', code: CLOSE_CODE.TooBig })
  })

  it('reads the fin bit and the opcode, and leaves the bytes after the frame', () => {
    const first = clientFrame(0x2, Buffer.from('a'), false)
    const second = clientFrame(0x0, Buffer.from('b'))
    const read = readClientFrame(Buffer.concat([first, second]))
    expect(read).toMatchObject({ kind: 'frame', frame: { fin: false, opcode: 0x2 } })
    if (read.kind === 'frame')
      expect(read.rest.equals(second)).toBe(true)
  })

  it('refuses an unmasked frame, a reserved bit and a frame past the cap', () => {
    expect(readClientFrame(encodeFrame(0x1, Buffer.from('x')))).toMatchObject({ kind: 'error', code: CLOSE_CODE.ProtocolError })
    const reserved = clientFrame(0x1, Buffer.from('x'))
    reserved[0] = reserved[0]! | 0x40
    expect(readClientFrame(reserved)).toMatchObject({ kind: 'error', code: CLOSE_CODE.ProtocolError })
    const huge = Buffer.alloc(10)
    huge[0] = 0x81
    huge[1] = 0x80 | 127
    huge.writeBigUInt64BE(BigInt(MAX_WEBSOCKET_MESSAGE_BYTES + 1), 2)
    expect(readClientFrame(huge)).toMatchObject({ kind: 'error', code: CLOSE_CODE.TooBig })
  })
})

describe('acceptWebSocket', () => {
  it('exchanges text messages with a real client and chooses the offered subprotocol', async () => {
    current = await startServer(['rivet'])
    const client = new WebSocket(current.url, ['rivet', 'rivet_encoding.bare'])
    const serverSide = await current.next()
    await new Promise<void>(resolve => client.addEventListener('open', () => resolve(), { once: true }))
    expect(client.protocol).toBe('rivet')
    expect(serverSide.protocol).toBe('rivet')

    const received = new Promise<string>(resolve => serverSide.onMessage(resolve))
    client.send('{"jsonrpc":"2.0","id":1,"method":"executor_connect"}')
    expect(await received).toBe('{"jsonrpc":"2.0","id":1,"method":"executor_connect"}')

    const large = 'x'.repeat(70_000)
    const echoed = new Promise<string>(resolve => client.addEventListener('message', event => resolve(String(event.data)), { once: true }))
    serverSide.send(large)
    expect(await echoed).toBe(large)

    const closed = new Promise<void>(resolve => serverSide.onClose(resolve))
    client.close(1000)
    await closed
    expect(serverSide.closed).toBe(true)
  })

  it('reassembles a fragmented message and answers a ping', async () => {
    current = await startServer()
    const { socket, response } = await rawConnection(current.port)
    expect(response).toMatch(/^HTTP\/1\.1 101/)
    const serverSide = await current.next()
    const received = new Promise<string>(resolve => serverSide.onMessage(resolve))
    const replies: Buffer[] = []
    socket.on('data', (chunk: Buffer) => replies.push(chunk))
    socket.write(Buffer.concat([
      clientFrame(0x1, Buffer.from('frag'), false),
      clientFrame(0x9, Buffer.from('p')),
      clientFrame(0x0, Buffer.from('mented'), true),
    ]))
    expect(await received).toBe('fragmented')
    await expect.poll(() => Buffer.concat(replies).subarray(0, 3)).toEqual(Buffer.from([0x8A, 0x01, 0x70]))
    socket.destroy()
  })

  it('closes a connection that sends an unmasked frame, with the protocol error code', async () => {
    current = await startServer()
    const { socket } = await rawConnection(current.port)
    const serverSide = await current.next()
    const replies: Buffer[] = []
    socket.on('data', (chunk: Buffer) => replies.push(chunk))
    const ended = new Promise<void>(resolve => socket.once('end', resolve))
    const closed = new Promise<void>(resolve => serverSide.onClose(resolve))
    socket.write(encodeFrame(0x1, Buffer.from('x')))
    await ended
    await closed
    const [frame] = serverFrames(Buffer.concat(replies))
    expect(frame).toMatchObject({ opcode: 0x8 })
    expect(frame!.payload.readUInt16BE(0)).toBe(CLOSE_CODE.ProtocolError)
    socket.destroy()
  })

  it('drops a send after the close, so the close frame is the last frame on the wire', async () => {
    current = await startServer()
    const { socket } = await rawConnection(current.port)
    const serverSide = await current.next()
    const replies: Buffer[] = []
    socket.on('data', (chunk: Buffer) => replies.push(chunk))
    const ended = new Promise<void>(resolve => socket.once('end', resolve))
    serverSide.close()
    expect(serverSide.closed).toBe(true)
    serverSide.send('late')
    await ended
    expect(serverFrames(Buffer.concat(replies))).toEqual([{ fin: true, opcode: 0x8, payload: Buffer.from([0x03, 0xE8]) }])
    socket.destroy()
  })

  it('refuses a client whose subprotocols it does not speak, and accepts one that offers a spoken one', async () => {
    current = await startServer(['rivet'])
    const refused = await rawConnection(current.port, 'Sec-WebSocket-Protocol: other\r\n')
    expect(refused.response).toMatch(/^HTTP\/1\.1 400/)
    refused.socket.destroy()
    const plain = await rawConnection(current.port, 'Sec-WebSocket-Protocol: rivet\r\n')
    expect(plain.response).toMatch(/^HTTP\/1\.1 101/)
    expect(plain.response).toContain('Sec-WebSocket-Protocol: rivet')
    plain.socket.destroy()
  })

  // A client that offers no subprotocol gets none. The protocol forbids a
  // subprotocol header in the answer to such a client.
  it('chooses no subprotocol for a client that offers none', async () => {
    current = await startServer(['rivet'])
    const { socket, response } = await rawConnection(current.port)
    expect(response).toMatch(/^HTTP\/1\.1 101/)
    expect(response).not.toContain('Sec-WebSocket-Protocol')
    expect((await current.next()).protocol).toBe('')
    socket.destroy()
  })
})

/** One frame that the server wrote. */
interface ServerFrame {
  fin: boolean
  opcode: number
  payload: Buffer
}

/** Every whole unmasked frame in `data`, which the server writes. */
function serverFrames(data: Buffer): ServerFrame[] {
  const frames: ServerFrame[] = []
  let offset = 0
  while (offset + 2 <= data.byteLength) {
    let length = data[offset + 1]! & 0x7F
    let start = offset + 2
    if (length === 126) {
      length = data.readUInt16BE(offset + 2)
      start = offset + 4
    }
    else if (length === 127) {
      length = Number(data.readBigUInt64BE(offset + 2))
      start = offset + 10
    }
    frames.push({ fin: (data[offset]! & 0x80) !== 0, opcode: data[offset]! & 0x0F, payload: Buffer.from(data.subarray(start, start + length)) })
    offset = start + length
  }
  return frames
}

/**
 * A socket that records what the endpoint writes. `receive` delivers bytes as the
 * client would send them, and `endInput` ends the client's side.
 */
function fakeSocket() {
  const written: Buffer[] = []
  let ended = false
  const socket = new Duplex({
    read() {},
    write(chunk: Buffer | string, _encoding, callback) {
      written.push(Buffer.from(chunk))
      callback()
    },
    final(callback) {
      ended = true
      callback()
    },
  })
  return {
    socket,
    /** The bytes after the handshake: the frames that the endpoint wrote. */
    frames: () => {
      const all = Buffer.concat(written)
      const bodyStart = all.indexOf('\r\n\r\n')
      return serverFrames(all.subarray(bodyStart < 0 ? all.byteLength : bodyStart + 4))
    },
    handshake: () => Buffer.concat(written).toString('latin1'),
    ended: () => ended,
    receive: (data: Buffer) => socket.push(data),
    endInput: () => socket.push(null),
  }
}

/** An upgrade request that holds only the fields the endpoint reads. */
function upgradeRequest(headers: IncomingHttpHeaders, method = 'GET'): IncomingMessage {
  return { method, headers: { 'upgrade': 'websocket', 'sec-websocket-key': 'dGhlIHNhbXBsZSBub25jZQ==', ...headers } } as IncomingMessage
}

/** Let the stream deliver what the test pushed. */
function settle(): Promise<void> {
  return new Promise(resolve => setImmediate(resolve))
}

/** A close frame from the client, with its code. */
function clientClose(code: number): Buffer {
  const payload = Buffer.alloc(2)
  payload.writeUInt16BE(code, 0)
  return clientFrame(0x8, payload)
}

describe('acceptWebSocket handshake', () => {
  it('answers the accept key of the client key', () => {
    const fake = fakeSocket()
    expect(acceptWebSocket(upgradeRequest({}), fake.socket, Buffer.alloc(0))).not.toBeNull()
    expect(fake.handshake()).toBe('HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n\r\n')
  })

  it('takes the first protocol of its own preference that the client offered', () => {
    const fake = fakeSocket()
    const connection = acceptWebSocket(upgradeRequest({ 'sec-websocket-protocol': 'b, a' }), fake.socket, Buffer.alloc(0), { protocols: ['a', 'b'] })
    expect(connection?.protocol).toBe('a')
  })

  it.each([
    ['no key', upgradeRequest({ 'sec-websocket-key': undefined })],
    ['an empty key', upgradeRequest({ 'sec-websocket-key': '' })],
    ['another upgrade', upgradeRequest({ upgrade: 'h2c' })],
    ['a POST', upgradeRequest({}, 'POST')],
  ])('refuses a request with %s', (_label, request) => {
    const fake = fakeSocket()
    expect(acceptWebSocket(request, fake.socket, Buffer.alloc(0))).toBeNull()
    expect(fake.handshake()).toMatch(/^HTTP\/1\.1 400 Bad Request\r\n/)
    expect(fake.ended()).toBe(true)
  })

  it('reads the upgrade header without regard to case', () => {
    const fake = fakeSocket()
    expect(acceptWebSocket(upgradeRequest({ upgrade: 'WebSocket' }), fake.socket, Buffer.alloc(0))).not.toBeNull()
  })
})

describe('acceptWebSocket connection', () => {
  function open(head: Buffer = Buffer.alloc(0)) {
    const fake = fakeSocket()
    const connection = acceptWebSocket(upgradeRequest({}), fake.socket, head)!
    const messages: string[] = []
    connection.onMessage(text => messages.push(text))
    let closes = 0
    connection.onClose(() => closes++)
    return { fake, connection, messages, closes: () => closes }
  }

  // A client may send its first frame in the same packet as the handshake, and the
  // HTTP server hands those bytes over as the head.
  it('reads a message that arrived with the handshake', async () => {
    const { messages } = open(clientFrame(0x1, Buffer.from('early')))
    await settle()
    expect(messages).toEqual(['early'])
  })

  it('reads a frame that arrives in several pieces', async () => {
    const { fake, messages } = open()
    const frame = clientFrame(0x1, Buffer.from('pieces'))
    for (const piece of [frame.subarray(0, 1), frame.subarray(1, 5), frame.subarray(5)])
      fake.receive(piece)
    await settle()
    expect(messages).toEqual(['pieces'])
  })

  it('reads a binary message as UTF-8 text', async () => {
    const { fake, messages } = open()
    fake.receive(clientFrame(0x2, Buffer.from('bytes')))
    await settle()
    expect(messages).toEqual(['bytes'])
  })

  it('echoes the code of the client\'s close, and ends its side', async () => {
    const { fake, connection, messages } = open()
    fake.receive(clientClose(4000))
    await settle()
    expect(fake.frames()).toEqual([{ fin: true, opcode: 0x8, payload: Buffer.from([0x0F, 0xA0]) }])
    expect(fake.ended()).toBe(true)
    expect(connection.closed).toBe(true)
    expect(messages).toEqual([])
  })

  it('answers no ping after it started to close', async () => {
    const { fake, connection } = open()
    connection.close()
    fake.receive(clientFrame(0x9, Buffer.from('p')))
    await settle()
    expect(fake.frames().map(frame => frame.opcode)).toEqual([0x8])
  })

  it('ignores a pong', async () => {
    const { fake, messages } = open()
    fake.receive(clientFrame(0xA, Buffer.from('p')))
    await settle()
    expect(fake.frames()).toEqual([])
    expect(messages).toEqual([])
  })

  it.each([
    ['a continuation frame with no message', [clientFrame(0x0, Buffer.from('x'))]],
    ['a new message inside a fragmented one', [clientFrame(0x1, Buffer.from('a'), false), clientFrame(0x1, Buffer.from('b'))]],
    ['an unknown opcode', [clientFrame(0x3, Buffer.from('x'))]],
  ])('closes with the protocol error code for %s', async (_label, frames) => {
    const { fake, messages } = open()
    fake.receive(Buffer.concat(frames))
    await settle()
    const [close] = fake.frames()
    expect(close?.opcode).toBe(0x8)
    expect(close!.payload.readUInt16BE(0)).toBe(CLOSE_CODE.ProtocolError)
    expect(messages).toEqual([])
  })

  // Each fragment is under the cap, and the message is over it. The cap limits the
  // message, so the endpoint closes rather than hold the whole of it.
  it('closes with the too-big code for a fragmented message past the cap', async () => {
    const { fake, messages } = open()
    const half = Buffer.alloc(MAX_WEBSOCKET_MESSAGE_BYTES / 2 + 1, 0x61)
    fake.receive(Buffer.concat([clientFrame(0x1, half, false), clientFrame(0x0, half, true)]))
    await settle()
    const [close] = fake.frames()
    expect(close?.opcode).toBe(0x8)
    expect(close!.payload.readUInt16BE(0)).toBe(CLOSE_CODE.TooBig)
    expect(messages).toEqual([])
  })

  // An upgraded socket keeps the HTTP server's half-open setting, so the endpoint
  // must end its own side when the client ends, or the close never finishes.
  it('ends its side and reports the close once when the client ends its side', async () => {
    const { fake, connection, closes } = open()
    fake.endInput()
    await settle()
    expect(fake.ended()).toBe(true)
    expect(connection.closed).toBe(true)
    expect(closes()).toBe(1)
    fake.socket.destroy()
    await settle()
    expect(closes()).toBe(1)
  })

  it('calls a close handler at once when the connection already ended', async () => {
    const { fake, connection } = open()
    fake.endInput()
    await settle()
    let called = false
    connection.onClose(() => {
      called = true
    })
    expect(called).toBe(true)
  })
})
