import type { Server as HttpServer } from 'node:http'
import type { Http2Server, ServerHttp2Session } from 'node:http2'
import type { Server as NetServer, Socket } from 'node:net'
import { Buffer } from 'node:buffer'
import { createServer as createRawServer } from 'node:net'
import process from 'node:process'

/**
 * These bytes identify the HTTP/2 client preface before any protocol frame.
 * An HTTP/1.1 request cannot use this method and version together.
 */
const HTTP2_CLIENT_PREFACE = Buffer.from('PRI * HTTP/2.0\r\n')

export interface DualVersionListener {
  /** Only the raw listener accepts connections. Neither protocol server listens. */
  server: NetServer
  /** Stop acceptance and close every owned socket and HTTP/2 session. */
  close: () => Promise<void>
}

/**
 * Serve HTTP/1.1 and HTTP/2 cleartext through one port.
 * Cursor uses HTTP/1.1 for startup and HTTP/2 for its Run stream.
 * Node's HTTP/2 server sends SETTINGS before an HTTP/1.1 client can send its request.
 * Inspect the client preface before either protocol server receives the socket.
 */
export function createDualVersionListener(http1: HttpServer, http2: Http2Server): DualVersionListener {
  const sockets = new Set<Socket>()
  const sessions = new Set<ServerHttp2Session>()
  http2.on('session', (session) => {
    sessions.add(session)
    session.once('close', () => sessions.delete(session))
  })
  const server = createRawServer((socket: Socket) => {
    sockets.add(socket)
    socket.once('close', () => sockets.delete(socket))
    // A partial preface can arrive in a separate TCP segment.
    // Accumulate bytes until the protocol choice cannot change.
    let head = Buffer.alloc(0)
    const onData = (chunk: Buffer): void => {
      head = Buffer.concat([head, chunk])
      const comparable = Math.min(head.byteLength, HTTP2_CLIENT_PREFACE.byteLength)
      const stillPossible = head.subarray(0, comparable).equals(HTTP2_CLIENT_PREFACE.subarray(0, comparable))
      if (stillPossible && head.byteLength < HTTP2_CLIENT_PREFACE.byteLength)
        return
      socket.removeListener('data', onData)
      // Pause before restoring the bytes for the target server's listeners.
      socket.pause()
      socket.unshift(head)
      const target = stillPossible ? http2 : http1
      target.emit('connection', socket)
      // HTTP/2 attaches its reader after this tick.
      // An immediate resume loses the preface and causes a protocol error.
      process.nextTick(() => {
        if (!socket.destroyed)
          socket.resume()
      })
    }
    socket.on('data', onData)
    // The listener also owns sockets before a protocol server receives them.
    socket.on('error', () => socket.destroy())
  })
  let closing: Promise<void> | undefined
  return {
    server,
    close: () => {
      closing ??= new Promise<void>((resolve, reject) => {
        server.close(error => error ? reject(error) : resolve())
        for (const session of sessions)
          session.destroy()
        for (const socket of sockets)
          socket.destroy()
      })
      return closing
    },
  }
}
