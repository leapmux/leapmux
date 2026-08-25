/**
 * Unit tests for the E2E capture SMTP server and its token extractor.
 *
 * The module under test lives at `tests/e2e/helpers/mail.ts`, not beside this
 * file, and it cannot carry its own `.test.ts`: `vitest.config.ts` excludes
 * `tests/e2e/**` so Playwright specs are never collected as unit tests, and
 * that exclusion covers the helpers too. A test placed there simply never
 * runs. `e2eServerOutput.test.ts` reaches across the same boundary for the
 * same reason.
 *
 * Both defects these tests pin cost a spec author a 2.5-minute timeout on the
 * reset page rather than a message that named the cause, which is the reason
 * the helper is worth unit-testing at all.
 */
import type { CaptureSmtpServer } from '../../tests/e2e/helpers/mail'
import { connect } from 'node:net'
import { afterEach, describe, expect, it } from 'vitest'
import { extractPasswordResetToken, startCaptureSmtpServer } from '../../tests/e2e/helpers/mail'

/** The width of `id.Generate()`, which mints the emailed reset token. */
const TOKEN_LENGTH = 48

/** A token as the hub mints it: mixed case and digits over a 62-symbol alphabet. */
const REALISTIC_TOKEN = 'qTz4Rk9BvMwL2xNpH7cJdY6eA1sUgF3iZoQ8rV5tXbCn0KwE'

/**
 * 48 chars whose first eight are hex digits. A `[0-9a-f]+` class captures
 * `deadbeef` and stops at the `Z`, so the length guard is what turns the old
 * defect into a message instead of a rejected token.
 */
const HEX_PREFIX_TOKEN = `deadbeefZ${'x'.repeat(39)}`

/** 48 chars with no hex digit at all: a `[0-9a-f]+` class matches nothing here. */
const NO_HEX_TOKEN = 'Z'.repeat(TOKEN_LENGTH)

/** The reset email as `Renderer.PasswordResetEmail` renders it: plain text, link on its own line. */
function resetEmailBody(token: string, hubURL = 'http://127.0.0.1:8080'): string {
  return [
    'You requested a password reset for your LeapMux account.',
    '',
    'Click the link below to choose a new password:',
    '',
    `    ${hubURL}/reset-password?token=${token}`,
    '',
    'The link expires in one hour. If you did not request this, you can ignore this email.',
    '',
    '-- ',
    `This is an automated message from your LeapMux hub at ${hubURL}.`,
    'Please do not reply.',
  ].join('\r\n')
}

describe('extractPasswordResetToken', () => {
  it('returns the whole token from a rendered reset email', () => {
    expect(extractPasswordResetToken(resetEmailBody(REALISTIC_TOKEN))).toBe(REALISTIC_TOKEN)
  })

  it('keeps every character after a hex-only prefix', () => {
    // The truncating half of the old defect: a hex class returned `deadbeef`,
    // the reset page refused it, and the spec failed on a missing button one
    // page later.
    const token = extractPasswordResetToken(resetEmailBody(HEX_PREFIX_TOKEN))
    expect(token).toBe(HEX_PREFIX_TOKEN)
    expect(token).toHaveLength(TOKEN_LENGTH)
  })

  it('reads a token that holds no hex digit at all', () => {
    // The other half: a hex class matched nothing, so extraction threw
    // "not found" on a body that plainly held the link.
    expect(extractPasswordResetToken(resetEmailBody(NO_HEX_TOKEN))).toBe(NO_HEX_TOKEN)
  })

  it('stops at the first character outside the mint alphabet', () => {
    const body = `Reset here: http://h/reset-password?token=${REALISTIC_TOKEN}. Ignore if not you.`
    expect(extractPasswordResetToken(body)).toBe(REALISTIC_TOKEN)
  })

  it('throws when the body carries no reset link', () => {
    const body = 'Your LeapMux verification code is 123456.\r\n'
    expect(() => extractPasswordResetToken(body)).toThrow(/token not found in captured email/)
  })

  it('names the body in the not-found error, so the failure is diagnosable', () => {
    expect(() => extractPasswordResetToken('nothing here')).toThrow(/nothing here/)
  })

  it('throws when the extracted token is short', () => {
    const short = REALISTIC_TOKEN.slice(0, TOKEN_LENGTH - 1)
    expect(() => extractPasswordResetToken(resetEmailBody(short)))
      .toThrow(new RegExp(`is ${TOKEN_LENGTH - 1} characters, want ${TOKEN_LENGTH}`))
  })

  it('throws when the extracted token is long', () => {
    const long = `${REALISTIC_TOKEN}Q`
    expect(() => extractPasswordResetToken(resetEmailBody(long)))
      .toThrow(new RegExp(`is ${TOKEN_LENGTH + 1} characters, want ${TOKEN_LENGTH}`))
  })

  it('rejects an empty body rather than returning an empty token', () => {
    expect(() => extractPasswordResetToken('')).toThrow(/token not found in captured email/)
  })
})

describe('startCaptureSmtpServer', () => {
  const running: CaptureSmtpServer[] = []

  afterEach(async () => {
    await Promise.all(running.splice(0).map(smtp => smtp.stop()))
  })

  /** Start a capture server and register it for teardown. */
  async function startServer(): Promise<CaptureSmtpServer> {
    const smtp = await startCaptureSmtpServer()
    running.push(smtp)
    return smtp
  }

  /**
   * Write raw bytes to the capture server and wait for it to close the socket.
   *
   * The conversation is pipelined in one write: the server parses whole lines
   * out of its own buffer, so it never needs the client to wait for 354.
   */
  function writeRaw(smtp: CaptureSmtpServer, chunks: string[]): Promise<void> {
    return new Promise((resolve, reject) => {
      const socket = connect(smtp.port, smtp.host, () => {
        void (async () => {
          for (const chunk of chunks) {
            socket.write(chunk)
            // Give the event loop a turn, so each chunk reaches the server as
            // its own `data` event. Coalescing does not fail the assertions;
            // it only makes the split case test less.
            await new Promise(tick => setTimeout(tick, 5))
          }
        })()
      })
      // Drain the replies although no assertion reads them. A socket with no
      // reader stays paused, so the server's buffered `250`s hold the stream
      // short of `end`, the FIN that follows QUIT never becomes `close`, and
      // this promise never settles.
      socket.resume()
      socket.on('error', reject)
      socket.on('close', () => resolve())
    })
  }

  /** Deliver one message whose DATA section is exactly `lines`. */
  function deliver(smtp: CaptureSmtpServer, lines: string[]): Promise<void> {
    return writeRaw(smtp, [
      `EHLO test\r\nMAIL FROM:<a@test>\r\nRCPT TO:<b@test>\r\nDATA\r\n${lines.join('\r\n')}\r\n.\r\nQUIT\r\n`,
    ])
  }

  it('hands the next message to a waiting caller', async () => {
    const smtp = await startServer()
    const waiting = smtp.waitForMessage()

    await deliver(smtp, ['Subject: hello', '', 'body line'])

    expect(await waiting).toBe('Subject: hello\r\n\r\nbody line')
    expect(smtp.messages).toHaveLength(1)
  })

  it('waits for the NEXT message rather than replaying a buffered one', async () => {
    const smtp = await startServer()
    await deliver(smtp, ['first'])
    expect(smtp.messages).toEqual(['first'])

    // A helper that answered `messages.at(-1)` would resolve with "first"
    // here, which is the wrong email for a spec that sends a verification
    // mail and then a reset.
    const waiting = smtp.waitForMessage()
    await deliver(smtp, ['second'])

    expect(await waiting).toBe('second')
    expect(smtp.messages).toEqual(['first', 'second'])
  })

  it('queues concurrent waiters FIFO, one message each', async () => {
    const smtp = await startServer()
    const first = smtp.waitForMessage()
    const second = smtp.waitForMessage()

    await deliver(smtp, ['one'])
    await deliver(smtp, ['two'])

    expect(await first).toBe('one')
    expect(await second).toBe('two')
  })

  it('keeps a timed-out waiter from eating the next message', async () => {
    const smtp = await startServer()
    // The regression: the timeout path removed `resolve`, which is never the
    // queued function, so the dead waiter stayed at the head of the FIFO. It
    // then consumed this delivery and `live` hung until its own timeout.
    const expired = smtp.waitForMessage(20)
    const live = smtp.waitForMessage()

    await expect(expired).rejects.toThrow(/timed out waiting for SMTP message after 20ms/)

    await deliver(smtp, ['for the live waiter'])

    expect(await live).toBe('for the live waiter')
  })

  it('leaves the queue empty after a lone waiter times out', async () => {
    const smtp = await startServer()
    await expect(smtp.waitForMessage(20)).rejects.toThrow(/timed out/)

    // A leaked waiter would take this message instead of the new caller.
    const waiting = smtp.waitForMessage()
    await deliver(smtp, ['after the timeout'])

    expect(await waiting).toBe('after the timeout')
  })

  it('unstuffs a leading dot and joins a line split across two chunks', async () => {
    const smtp = await startServer()
    const waiting = smtp.waitForMessage()

    // `..hidden` is how a client transmits a body line that starts with a
    // dot, and the split lands mid-line so the server must carry the
    // remainder in its buffer.
    await writeRaw(smtp, [
      'EHLO test\r\nDATA\r\n..hidden\r\nsplit-',
      'across\r\n.\r\nQUIT\r\n',
    ])

    expect(await waiting).toBe('.hidden\r\nsplit-across')
  })

  it('stops listening once stopped', async () => {
    const smtp = await startCaptureSmtpServer()
    const { port, host } = smtp
    await smtp.stop()

    await expect(new Promise((resolve, reject) => {
      const socket = connect(port, host, () => resolve('connected'))
      socket.on('error', reject)
    })).rejects.toThrow(/ECONNREFUSED/)
  })
})
