import type { Server, Socket } from 'node:net'
import { createServer } from 'node:net'
import { configureCaptureSmtpViaAPI, waitForEmailEnabled } from './api'

export interface CaptureSmtpServer {
  host: string
  port: number
  messages: string[]
  stop: () => Promise<void>
  waitForMessage: (timeoutMs?: number) => Promise<string>
}

function reply(socket: Socket, code: number, message: string) {
  socket.write(`${code} ${message}\r\n`)
}

/**
 * Start a minimal loopback SMTP server that accepts one message at a time.
 *
 * The hub is pointed at this relay with `tls_mode: "none"` so E2E tests can
 * capture verification and password-reset emails without external infra.
 */
export async function startCaptureSmtpServer(): Promise<CaptureSmtpServer> {
  const messages: string[] = []
  // FIFO of waiters: a second concurrent waitForMessage queues behind the
  // first instead of silently discarding its resolver (which would hang the
  // first waiter until its timeout with the message already delivered).
  const waiters: Array<(body: string) => void> = []
  const timers: Array<ReturnType<typeof setTimeout>> = []

  const server: Server = createServer((socket) => {
    let buffer = ''
    let inData = false
    let dataLines: string[] = []

    reply(socket, 220, 'localhost ESMTP test')

    socket.on('data', (chunk) => {
      buffer += chunk.toString()
      while (true) {
        const nl = buffer.indexOf('\r\n')
        if (nl === -1)
          break
        const line = buffer.slice(0, nl)
        buffer = buffer.slice(nl + 2)

        if (inData) {
          if (line === '.') {
            const body = dataLines.join('\r\n')
            messages.push(body)
            waiters.shift()?.(body)
            dataLines = []
            inData = false
            reply(socket, 250, 'OK')
            continue
          }
          dataLines.push(line.replace(/^\.\./, '.'))
          continue
        }

        const cmd = line.toUpperCase()
        if (cmd.startsWith('EHLO') || cmd.startsWith('HELO')) {
          reply(socket, 250, 'localhost')
        }
        else if (cmd.startsWith('MAIL FROM') || cmd.startsWith('RCPT TO') || cmd.startsWith('RSET')) {
          reply(socket, 250, 'OK')
        }
        else if (cmd.startsWith('DATA')) {
          inData = true
          dataLines = []
          reply(socket, 354, 'End data with <CR><LF>.<CR><LF>')
        }
        else if (cmd.startsWith('QUIT')) {
          reply(socket, 221, 'Bye')
          socket.end()
        }
        else if (cmd.startsWith('NOOP')) {
          reply(socket, 250, 'OK')
        }
        else if (line.length > 0) {
          reply(socket, 250, 'OK')
        }
      }
    })
  })

  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => resolve())
  })

  const addr = server.address()
  if (!addr || typeof addr === 'string')
    throw new Error('failed to bind SMTP capture server')

  return {
    host: '127.0.0.1',
    port: addr.port,
    messages,
    stop: () => new Promise((resolve) => {
      for (const t of timers)
        clearTimeout(t)
      server.close(() => resolve())
    }),
    // waitForMessage resolves with the NEXT message that arrives after the
    // call — never an already-buffered one. A helper that returns
    // `messages.at(-1)` hands back the wrong email the moment a spec sends
    // two (signup verification, then a reset) or delivery turns async.
    // Concurrent waits queue FIFO: each resolves with its own message.
    waitForMessage: (timeoutMs = 30_000) => new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        const idx = waiters.indexOf(resolve)
        if (idx !== -1)
          waiters.splice(idx, 1)
        reject(new Error(`timed out waiting for SMTP message after ${timeoutMs}ms`))
      }, timeoutMs)
      timers.push(timer)
      waiters.push((body) => {
        clearTimeout(timer)
        resolve(body)
      })
    }),
  }
}

/**
 * Stage the hub with a capture SMTP server for one test body: start the
 * server, point the hub at it, wait for email to report enabled, run the
 * body, and stop the server. One helper, so a new required staging step
 * applies to every SMTP spec at once.
 */
export async function withCaptureSmtp(
  server: { hubUrl: string, adminToken: string },
  body: (smtp: CaptureSmtpServer) => Promise<void>,
): Promise<void> {
  const smtp = await startCaptureSmtpServer()
  try {
    await configureCaptureSmtpViaAPI(server.hubUrl, server.adminToken, smtp)
    await waitForEmailEnabled(server.hubUrl)
    await body(smtp)
  }
  finally {
    await smtp.stop()
  }
}

/** Pull the self-service password-reset token out of a captured reset email body. */
export function extractPasswordResetToken(emailBody: string): string {
  const match = emailBody.match(/\/reset-password\?token=([0-9a-f]+)/i)
  if (!match?.[1])
    throw new Error('password reset token not found in captured email')
  return match[1]
}
