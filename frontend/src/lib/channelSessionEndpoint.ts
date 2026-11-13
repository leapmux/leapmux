import type { ChannelSessionOpts, PendingHandshake, SessionChannel } from './channelSession'
import type { SessionCommand, SessionEvent } from './channelSessionProtocol'
import type { Session } from './noise'
import type { WorkerKeyBundle } from './workerKeyBundle'
import { IDLE_REKEY_INTERVAL_MS } from '~/generated/contracts/wire'
import { EncryptionMode } from '~/generated/proto/leapmux/v1/channel_pb'
import { assertNever } from './assertNever'
import { ownedBytes } from './bytes'
import { ChannelError } from './channelError'
import { ChannelSession, createSessionChannel, sessionGoneError } from './channelSession'
import { ChannelSessionInbound } from './channelSessionInbound'
import { sessionErrorOf } from './channelSessionProtocol'
import { formatErrorMessage } from './errors'
import { createLogger, setDebugEnabled } from './logger'

// ---------------------------------------------------------------------------
// Channel session endpoint
//
// The command router of the channel session worker. It holds the session of
// every channel of the tab, the handshakes that have not finished, and the idle
// rekey timer, and it turns each command of the main thread into events (see
// channelSessionProtocol.ts).
//
// A real Worker hosts it in production (channelSessionWorker.ts). The unit tests
// and the Node E2E harness host it behind a MessageChannel pair instead
// (channelSessionInProcess.ts).
//
// Command handling is not serialized: a command that awaits, such as an
// `ensureReady` that waits for a rekey, must let the inbound RekeyAck through.
// ---------------------------------------------------------------------------

const log = createLogger('channel')

export interface ChannelSessionEndpointOptions extends ChannelSessionOpts {
  /**
   * Idle rekey poll interval in ms (default IDLE_REKEY_INTERVAL_MS, matching Go).
   * Set to 0 to disable the idle timer (tests that only exercise send-path rekey).
   */
  idleRekeyIntervalMs?: number
}

export interface ChannelSessionEndpointDeps {
  /** Post one event to the main thread, transferring the buffers in `transfer`. */
  post: (event: SessionEvent, transfer: Transferable[]) => void
  /** Stop the worker that hosts this endpoint. */
  closeSelf: () => void
}

export interface ChannelSessionEndpoint {
  /** Act on one command of the main thread. Ignores every command after `dispose`. */
  handleCommand: (command: SessionCommand) => void
  /** Zero and drop every session and handshake, and stop the idle timer. Idempotent. */
  dispose: () => void
  /** The record of `channelId`'s session, or undefined. A test seam. */
  sessionState: (channelId: string) => SessionChannel | undefined
}

/** A handshake that began and has not finished, with what its finish needs. */
interface OpenHandshake {
  pending: PendingHandshake
  mode: EncryptionMode
  keys: WorkerKeyBundle
}

/** Zero both cipher states of a session that no channel will use. */
function destroySession(session: Session): void {
  session.send.destroy()
  session.receive.destroy()
}

/** The ChannelError that `err` is, or a transport failure that carries its message. */
function channelErrorFrom(err: unknown): ChannelError {
  return err instanceof ChannelError
    ? err
    : new ChannelError('transport', formatErrorMessage(err))
}

export function createChannelSessionEndpoint(
  deps: ChannelSessionEndpointDeps,
  options: ChannelSessionEndpointOptions = {},
): ChannelSessionEndpoint {
  const sessions = new Map<string, SessionChannel>()
  const handshakes = new Map<number, OpenHandshake>()
  const idleRekeyIntervalMs = options.idleRekeyIntervalMs ?? IDLE_REKEY_INTERVAL_MS
  let idleRekeyTimer: ReturnType<typeof setInterval> | null = null
  let disposed = false

  const session = new ChannelSession({
    postFrame: (channelId, frame) => {
      const owned = ownedBytes(frame)
      post({ type: 'wire', channelId, frame: owned }, [owned.buffer])
    },
    endSession,
  }, options)

  const inbound = new ChannelSessionInbound({
    session,
    postDeliver: (ch, correlationId, plaintext) => {
      const owned = ownedBytes(plaintext)
      post({
        type: 'deliver',
        channelId: ch.channelId,
        correlationId,
        plaintext: owned,
      }, [owned.buffer])
    },
    postReassemblyFailed: (ch, correlationId, message) => {
      post({
        type: 'reassemblyFailed',
        channelId: ch.channelId,
        correlationId,
        message,
      })
    },
    endSession,
  })

  /** Post `event`, unless the endpoint is disposed: nobody listens any more. */
  function post(event: SessionEvent, transfer: Transferable[] = []): void {
    if (disposed)
      return
    deps.post(event, transfer)
  }

  function postRequestFailed(requestId: number, err: unknown): void {
    post({
      type: 'requestFailed',
      requestId,
      error: sessionErrorOf(err),
    })
  }

  function startIdleRekeyTimer(): void {
    if (idleRekeyIntervalMs <= 0 || idleRekeyTimer !== null)
      return
    idleRekeyTimer = setInterval(checkRekey, idleRekeyIntervalMs)
  }

  function stopIdleRekeyTimerIfEmpty(): void {
    if (sessions.size > 0 || idleRekeyTimer === null)
      return
    clearInterval(idleRekeyTimer)
    idleRekeyTimer = null
  }

  /** Dispose `ch` and forget it, without telling the main thread. */
  function endWithoutEvent(ch: SessionChannel): void {
    session.dispose(ch)
    if (sessions.get(ch.channelId) === ch)
      sessions.delete(ch.channelId)
    stopIdleRekeyTimerIfEmpty()
  }

  /** End `ch` and tell the main thread, which closes the channel. */
  function endSession(ch: SessionChannel, error: ChannelError): void {
    if (ch.ended)
      return
    endWithoutEvent(ch)
    post({
      type: 'sessionEnded',
      channelId: ch.channelId,
      error: sessionErrorOf(error),
    })
  }

  /** Tell the main thread that it has a channel whose session the worker does not have. */
  function postSessionGone(channelId: string): void {
    post({
      type: 'sessionEnded',
      channelId,
      error: sessionErrorOf(sessionGoneError()),
    })
  }

  /** Rekey every session whose rekey is due. The idle timer and `checkRekey` run this. */
  function checkRekey(): void {
    for (const ch of sessions.values()) {
      if (!session.needsRekeyGate(ch))
        continue
      void session.ensureRekeyed(ch).catch((err: unknown) => {
        log.warn('wake/idle rekey failed', {
          channel_id: ch.channelId,
          error: formatErrorMessage(err),
        })
      })
    }
  }

  async function finishHandshake(command: Extract<SessionCommand, { type: 'finishHandshake' }>): Promise<void> {
    const open = handshakes.get(command.handshakeId)
    handshakes.delete(command.handshakeId)
    if (open === undefined) {
      postRequestFailed(command.requestId, new ChannelError('transport', 'handshake is gone'))
      return
    }
    if (sessions.has(command.channelId)) {
      open.pending.abandon()
      postRequestFailed(command.requestId, new ChannelError('transport', `channel session ${command.channelId} already exists`))
      return
    }
    let established: Session
    try {
      established = await open.pending.finish(command.payload)
    }
    catch (err) {
      postRequestFailed(command.requestId, err)
      return
    }
    // The endpoint shut down while this finish waited: no channel will ever use
    // these keys, and nobody listens for the answer.
    if (disposed) {
      destroySession(established)
      return
    }
    // Another finish registered this channel while this one waited.
    if (sessions.has(command.channelId)) {
      destroySession(established)
      postRequestFailed(command.requestId, new ChannelError('transport', `channel session ${command.channelId} already exists`))
      return
    }
    sessions.set(command.channelId, createSessionChannel({
      channelId: command.channelId,
      session: established,
      maxReassembledMessageSize: command.maxReassembledMessageSize,
      // Retain the worker's static ML-KEM key so a later in-band rekey can
      // encapsulate a fresh PQ ciphertext without a second
      // getWorkerHandshakeParams round trip. Empty in classic mode; its length
      // is the single source of truth for PQ-ness in the rekey path.
      workerMlkemPub: open.mode === EncryptionMode.CLASSIC
        ? new Uint8Array(0)
        : open.keys.mlkemPublicKey,
    }))
    startIdleRekeyTimer()
    post({ type: 'requestDone', requestId: command.requestId })
  }

  async function ensureReady(command: Extract<SessionCommand, { type: 'ensureReady' }>): Promise<void> {
    const ch = sessions.get(command.channelId)
    if (ch === undefined) {
      postSessionGone(command.channelId)
      postRequestFailed(command.requestId, sessionGoneError())
      return
    }
    try {
      await session.ensureReady(ch)
    }
    catch (err) {
      postRequestFailed(command.requestId, err)
      return
    }
    post({ type: 'requestDone', requestId: command.requestId })
  }

  /** Report `err`, which handling `command` threw, to the scope that `command` belongs to. */
  function reportFailure(command: SessionCommand, err: unknown): void {
    try {
      if ('requestId' in command) {
        postRequestFailed(command.requestId, err)
        return
      }
      if ('channelId' in command) {
        const ch = sessions.get(command.channelId)
        if (ch !== undefined) {
          endSession(ch, channelErrorFrom(err))
          return
        }
      }
      log.error('the channel session worker failed to handle a command', { command: command.type, error: err })
    }
    catch (reportErr) {
      log.error('the channel session worker failed to report a failure', { command: command.type, error: err, report_error: reportErr })
    }
  }

  function dispatch(command: SessionCommand): void {
    switch (command.type) {
      case 'configure':
        setDebugEnabled(command.debug)
        return
      case 'beginHandshake': {
        const pending = session.beginHandshake(command.mode, command.keys)
        try {
          const message1 = ownedBytes(pending.message1)
          post({ type: 'handshakeBegun', requestId: command.requestId, message1 }, [message1.buffer])
        }
        catch (err) {
          // The main thread never learns this handshake, so nothing would ever
          // finish or abandon it.
          pending.abandon()
          throw err
        }
        // Filed after the post: the answer reaches the main thread in a later
        // task, so its finish cannot arrive before this.
        handshakes.set(command.requestId, { pending, mode: command.mode, keys: command.keys })
        return
      }
      case 'finishHandshake':
        void finishHandshake(command).catch((err: unknown) => reportFailure(command, err))
        return
      case 'abandonHandshake': {
        const open = handshakes.get(command.handshakeId)
        handshakes.delete(command.handshakeId)
        open?.pending.abandon()
        return
      }
      case 'send': {
        const ch = sessions.get(command.channelId)
        // A send for a channel that has no session must still end the channel
        // on the main thread, or the request that it carries would never settle.
        if (ch === undefined)
          postSessionGone(command.channelId)
        else
          session.send(ch, command.plaintext, command.correlationId, command.startsRequest)
        return
      }
      case 'release': {
        const ch = sessions.get(command.channelId)
        if (ch !== undefined)
          session.release(ch, command.correlationId)
        return
      }
      case 'inbound': {
        const ch = sessions.get(command.channelId)
        if (ch !== undefined)
          inbound.handleInbound(ch, command.correlationId, command.flags, command.ciphertext)
        return
      }
      case 'ensureReady':
        void ensureReady(command).catch((err: unknown) => reportFailure(command, err))
        return
      case 'checkRekey':
        checkRekey()
        return
      case 'close': {
        // The main thread closed the channel already, so this posts no event.
        const ch = sessions.get(command.channelId)
        if (ch !== undefined)
          endWithoutEvent(ch)
        return
      }
      case 'shutdown':
        dispose()
        deps.closeSelf()
        return
      default:
        assertNever(command)
    }
  }

  function dispose(): void {
    if (disposed)
      return
    disposed = true
    for (const ch of sessions.values())
      session.dispose(ch)
    sessions.clear()
    for (const open of handshakes.values())
      open.pending.abandon()
    handshakes.clear()
    stopIdleRekeyTimerIfEmpty()
  }

  return {
    handleCommand(command) {
      if (disposed)
        return
      try {
        dispatch(command)
      }
      catch (err) {
        reportFailure(command, err)
      }
    },
    dispose,
    sessionState: channelId => sessions.get(channelId),
  }
}
