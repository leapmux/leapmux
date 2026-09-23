import type { Server as HttpServer } from 'node:http'
/**
 * One port that serves HTTP/1.1 and HTTP/2 cleartext at the same time.
 *
 * `cursor-agent` needs both from a single endpoint: its startup calls and its
 * OpenTelemetry exporter are HTTP/1.1, and its `agent.v1.AgentService/Run`
 * stream is HTTP/2. Every other provider the mock serves is HTTP/1.1 only, and
 * stays unaffected -- a connection whose first bytes are not the HTTP/2 preface
 * reaches exactly the server it always did.
 *
 * Node cannot do this with one server. `http2.createServer` accepts
 * `allowHTTP1`, but it writes its SETTINGS preface as soon as the socket opens,
 * and an HTTP/1.1 client reads those bytes as a malformed status line:
 * `Parse Error: Expected HTTP/, RTSP/ or ICE/`. So sniff the CLIENT preface
 * instead, which the client sends first, and hand the socket to whichever server
 * owns it.
 */
import type { Http2Server } from 'node:http2'
import type { Server as NetServer, Socket } from 'node:net'
import { Buffer } from 'node:buffer'
import { createServer as createRawServer } from 'node:net'
import process from 'node:process'

/**
 * The HTTP/2 connection preface, which a client sends before any frame.
 *
 * Its full form ends `SM\r\n\r\n`; these first bytes identify it, and no
 * HTTP/1.1 request line can begin with them (`PRI` is not a method any client
 * sends, and the version token would be wrong in any case).
 */
const HTTP2_CLIENT_PREFACE = Buffer.from('PRI * HTTP/2.0\r\n')

export interface DualVersionListener {
  /** The raw listener. Close THIS to stop accepting; the two servers hold no socket of their own. */
  server: NetServer
}

/**
 * Route every accepted connection to `http1` or `http2` by its first bytes.
 *
 * Neither server listens itself. Each receives sockets through `connection`,
 * which is the same path its own `listen` would have used.
 */
export function createDualVersionListener(http1: HttpServer, http2: Http2Server): DualVersionListener {
  const server = createRawServer((socket: Socket) => {
    // Read until the preface is either matched or ruled out. One TCP segment
    // usually carries the whole thing, but nothing guarantees it: a first chunk
    // shorter than the preface compares unequal, and the connection would then
    // go to the HTTP/1.1 server, which answers an HTTP/2 client with a parse
    // error. Accumulate instead, and decide once the answer cannot change.
    let head = Buffer.alloc(0)
    const onData = (chunk: Buffer): void => {
      head = Buffer.concat([head, chunk])
      const comparable = Math.min(head.byteLength, HTTP2_CLIENT_PREFACE.byteLength)
      const stillPossible = head.subarray(0, comparable).equals(HTTP2_CLIENT_PREFACE.subarray(0, comparable))
      if (stillPossible && head.byteLength < HTTP2_CLIENT_PREFACE.byteLength)
        return
      socket.removeListener('data', onData)
      // PAUSE before unshifting: the target server attaches its own listeners
      // when it takes the socket, and the bytes must still be queued for it.
      socket.pause()
      socket.unshift(head)
      const target = stillPossible ? http2 : http1
      target.emit('connection', socket)
      // Resume on the NEXT TICK, never in this one. `emit('connection')` only
      // starts the target's setup; an HTTP/2 session attaches its reader after
      // the current tick. A resume here drains the unshifted preface before
      // anything reads it, and the session then starts mid-stream and fails
      // with `Received bad client magic byte string` -- which reaches the agent
      // as "Session closed with error code 2" and reaches the test as a
      // provider that answers nothing.
      process.nextTick(() => socket.resume())
    }
    socket.on('data', onData)
    // A client that opens a connection and sends nothing would otherwise hold a
    // socket with no server attached, and no server to time it out.
    socket.on('error', () => socket.destroy())
  })
  return { server }
}
