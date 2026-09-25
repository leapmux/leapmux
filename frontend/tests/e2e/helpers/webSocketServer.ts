/**
 * A minimal WebSocket server endpoint for the mock model server (RFC 6455).
 *
 * Amp's CLI reaches its thread actor over a WebSocket, so the Amp half of the mock
 * (`./ampSurface`) must accept one. The run has no WebSocket library among its
 * dependencies, and Node supplies a WebSocket CLIENT but no server, so this module
 * speaks the protocol itself. It holds what the mock needs and nothing else:
 *
 *   - the opening handshake, with one subprotocol chosen from the client's offer;
 *   - text and binary messages, fragmented or not, of any length the cap admits;
 *   - ping, pong and the closing handshake;
 *   - unmasked text frames from the server.
 *
 * It offers no extension. A client that offers `permessage-deflate` gets none, which
 * the protocol allows, and then sends uncompressed frames.
 */
import type { IncomingMessage } from 'node:http'
import type { Duplex } from 'node:stream'
import { Buffer } from 'node:buffer'
import { createHash } from 'node:crypto'

/** The GUID the handshake appends to the client's key (RFC 6455, section 1.3). */
const HANDSHAKE_GUID = '258EAFA5-E914-47DA-95CA-C5AB0DC85B11'

/** The largest message the endpoint accepts. A larger one closes the connection with 1009. */
export const MAX_WEBSOCKET_MESSAGE_BYTES = 16 * 1024 * 1024

const OPCODE = {
  Continuation: 0x0,
  Text: 0x1,
  Binary: 0x2,
  Close: 0x8,
  Ping: 0x9,
  Pong: 0xA,
} as const

/** The close codes the endpoint sends (RFC 6455, section 7.4.1). */
export const CLOSE_CODE = {
  Normal: 1000,
  ProtocolError: 1002,
  TooBig: 1009,
} as const

/** One accepted connection. */
export interface WebSocketConnection {
  /** The subprotocol the handshake chose, or '' when the client offered none. */
  readonly protocol: string
  /** True once either side started to close. */
  readonly closed: boolean
  /** Send one text message. The connection drops a send after the close. */
  send: (text: string) => void
  /** Start the closing handshake. */
  close: (code?: number, reason?: string) => void
  /** Called with each complete text or binary message, as UTF-8 text. */
  onMessage: (handler: (text: string) => void) => void
  /** Called once, when the connection ends for any reason. */
  onClose: (handler: () => void) => void
}

export interface AcceptOptions {
  /**
   * The subprotocols the endpoint speaks, in preference order. The handshake takes
   * the first one the client offered. The handshake refuses a client that offered
   * subprotocols when the endpoint speaks none of them.
   */
  protocols?: readonly string[]
}

/** The accept key the handshake answers a client key with. */
export function webSocketAcceptKey(clientKey: string): string {
  return createHash('sha1').update(clientKey + HANDSHAKE_GUID).digest('base64')
}

/**
 * Complete the opening handshake of one upgrade request, or refuse it.
 *
 * Returns null after it answered a refusal: a request that is not a WebSocket upgrade,
 * or that offered only subprotocols the endpoint does not speak.
 */
export function acceptWebSocket(request: IncomingMessage, socket: Duplex, head: Buffer, options: AcceptOptions = {}): WebSocketConnection | null {
  const key = request.headers['sec-websocket-key']
  const upgrade = String(request.headers.upgrade ?? '').toLowerCase()
  if (typeof key !== 'string' || key === '' || upgrade !== 'websocket' || request.method !== 'GET') {
    refuse(socket, 400, 'Bad Request')
    return null
  }
  const offered = String(request.headers['sec-websocket-protocol'] ?? '')
    .split(',')
    .map(value => value.trim())
    .filter(value => value !== '')
  const protocol = offered.length === 0 ? '' : (options.protocols ?? []).find(candidate => offered.includes(candidate))
  if (protocol === undefined) {
    refuse(socket, 400, 'Bad Request')
    return null
  }
  const lines = [
    'HTTP/1.1 101 Switching Protocols',
    'Upgrade: websocket',
    'Connection: Upgrade',
    `Sec-WebSocket-Accept: ${webSocketAcceptKey(key)}`,
    ...(protocol ? [`Sec-WebSocket-Protocol: ${protocol}`] : []),
  ]
  socket.write(`${lines.join('\r\n')}\r\n\r\n`)
  return new Connection(socket, protocol, head)
}

function refuse(socket: Duplex, status: number, text: string): void {
  socket.end(`HTTP/1.1 ${status} ${text}\r\nConnection: close\r\nContent-Length: 0\r\n\r\n`)
}

/** Encode one unmasked frame from the server. */
export function encodeFrame(opcode: number, payload: Buffer, fin = true): Buffer {
  const first = (fin ? 0x80 : 0) | opcode
  let header: Buffer
  if (payload.byteLength < 126) {
    header = Buffer.from([first, payload.byteLength])
  }
  else if (payload.byteLength <= 0xFFFF) {
    header = Buffer.alloc(4)
    header[0] = first
    header[1] = 126
    header.writeUInt16BE(payload.byteLength, 2)
  }
  else {
    header = Buffer.alloc(10)
    header[0] = first
    header[1] = 127
    header.writeBigUInt64BE(BigInt(payload.byteLength), 2)
  }
  return Buffer.concat([header, payload])
}

/** One decoded frame. */
interface Frame {
  fin: boolean
  opcode: number
  payload: Buffer
}

/** The result of reading one frame off the front of a buffer. */
type FrameRead
  = | { kind: 'frame', frame: Frame, rest: Buffer }
    | { kind: 'incomplete' }
    | { kind: 'error', code: number, reason: string }

/**
 * Read one client frame off the front of `data`.
 *
 * A client MUST mask each frame that it sends (RFC 6455, section 5.1). A server that
 * receives an unmasked frame closes the connection.
 */
export function readClientFrame(data: Buffer): FrameRead {
  if (data.byteLength < 2)
    return { kind: 'incomplete' }
  const first = data[0]!
  const second = data[1]!
  if ((first & 0x70) !== 0)
    return { kind: 'error', code: CLOSE_CODE.ProtocolError, reason: 'reserved bits are set, and the handshake agreed no extension' }
  const masked = (second & 0x80) !== 0
  if (!masked)
    return { kind: 'error', code: CLOSE_CODE.ProtocolError, reason: 'the client did not mask a frame' }
  let length = second & 0x7F
  let offset = 2
  if (length === 126) {
    if (data.byteLength < 4)
      return { kind: 'incomplete' }
    length = data.readUInt16BE(2)
    offset = 4
  }
  else if (length === 127) {
    if (data.byteLength < 10)
      return { kind: 'incomplete' }
    const big = data.readBigUInt64BE(2)
    if (big > BigInt(MAX_WEBSOCKET_MESSAGE_BYTES))
      return { kind: 'error', code: CLOSE_CODE.TooBig, reason: 'the frame is too large' }
    length = Number(big)
    offset = 10
  }
  if (length > MAX_WEBSOCKET_MESSAGE_BYTES)
    return { kind: 'error', code: CLOSE_CODE.TooBig, reason: 'the frame is too large' }
  if (data.byteLength < offset + 4 + length)
    return { kind: 'incomplete' }
  const mask = data.subarray(offset, offset + 4)
  const payload = Buffer.from(data.subarray(offset + 4, offset + 4 + length))
  for (let index = 0; index < payload.byteLength; index++)
    payload[index] = payload[index]! ^ mask[index % 4]!
  return { kind: 'frame', frame: { fin: (first & 0x80) !== 0, opcode: first & 0x0F, payload }, rest: data.subarray(offset + 4 + length) }
}

class Connection implements WebSocketConnection {
  private buffered: Buffer
  private fragments: Buffer[] = []
  private fragmentBytes = 0
  private messageHandlers: ((text: string) => void)[] = []
  private closeHandlers: (() => void)[] = []
  private closing = false
  private ended = false

  constructor(private readonly socket: Duplex, readonly protocol: string, head: Buffer) {
    this.buffered = Buffer.from(head)
    socket.on('data', (chunk: Buffer) => {
      this.buffered = Buffer.concat([this.buffered, chunk])
      this.drain()
    })
    // An upgraded socket keeps the HTTP server's half-open setting, so a client
    // that ends its side would otherwise leave this side open, and the server
    // could never finish closing.
    socket.on('end', () => {
      socket.end()
      this.finish()
    })
    socket.on('close', () => this.finish())
    socket.on('error', () => this.finish())
    if (this.buffered.byteLength > 0)
      queueMicrotask(() => this.drain())
  }

  get closed(): boolean {
    return this.closing || this.ended
  }

  send(text: string): void {
    if (this.closed)
      return
    this.socket.write(encodeFrame(OPCODE.Text, Buffer.from(text, 'utf8')))
  }

  close(code: number = CLOSE_CODE.Normal, reason = ''): void {
    if (this.closed)
      return
    this.closing = true
    const payload = Buffer.alloc(2 + Buffer.byteLength(reason))
    payload.writeUInt16BE(code, 0)
    payload.write(reason, 2)
    this.socket.write(encodeFrame(OPCODE.Close, payload))
    this.socket.end()
  }

  onMessage(handler: (text: string) => void): void {
    this.messageHandlers.push(handler)
  }

  onClose(handler: () => void): void {
    if (this.ended) {
      handler()
      return
    }
    this.closeHandlers.push(handler)
  }

  private drain(): void {
    while (!this.ended) {
      const read = readClientFrame(this.buffered)
      if (read.kind === 'incomplete')
        return
      if (read.kind === 'error') {
        this.close(read.code, read.reason)
        return
      }
      this.buffered = Buffer.from(read.rest)
      this.handleFrame(read.frame)
    }
  }

  private handleFrame(frame: Frame): void {
    switch (frame.opcode) {
      case OPCODE.Ping:
        if (!this.closed)
          this.socket.write(encodeFrame(OPCODE.Pong, frame.payload))
        return
      case OPCODE.Pong:
        return
      case OPCODE.Close:
        // Echo the client's code, as the closing handshake asks, and end the socket.
        if (!this.closing) {
          this.closing = true
          this.socket.write(encodeFrame(OPCODE.Close, frame.payload.subarray(0, 2)))
          this.socket.end()
        }
        return
      case OPCODE.Text:
      case OPCODE.Binary:
      case OPCODE.Continuation:
        this.handleData(frame)
        return
      default:
        this.close(CLOSE_CODE.ProtocolError, `unknown opcode ${frame.opcode}`)
    }
  }

  private handleData(frame: Frame): void {
    const starts = frame.opcode !== OPCODE.Continuation
    if (starts === (this.fragments.length > 0)) {
      this.close(CLOSE_CODE.ProtocolError, starts ? 'a new message began inside a fragmented one' : 'a continuation frame has no message to continue')
      return
    }
    this.fragmentBytes += frame.payload.byteLength
    if (this.fragmentBytes > MAX_WEBSOCKET_MESSAGE_BYTES) {
      this.close(CLOSE_CODE.TooBig, 'the message is too large')
      return
    }
    this.fragments.push(frame.payload)
    if (!frame.fin)
      return
    const text = Buffer.concat(this.fragments).toString('utf8')
    this.fragments = []
    this.fragmentBytes = 0
    for (const handler of this.messageHandlers)
      handler(text)
  }

  private finish(): void {
    if (this.ended)
      return
    this.ended = true
    this.closing = true
    const handlers = this.closeHandlers
    this.closeHandlers = []
    for (const handler of handlers)
      handler()
  }
}
