import type { ChannelErrorSource } from './channelError'
import type { WorkerKeyBundle } from './workerKeyBundle'
import type { ChannelMessageFlags, EncryptionMode } from '~/generated/proto/leapmux/v1/channel_pb'
import { ChannelError } from './channelError'
import { formatErrorMessage } from './errors'

// ---------------------------------------------------------------------------
// The protocol between the main thread and the channel session worker
//
// The session worker owns the Noise session of every channel: the handshake,
// the cipher states, encryption, decryption, chunking, reassembly and the
// in-band rekey. The main thread keeps the socket and the RPC layer. It sends
// the commands below, and the worker answers with the events below.
//
// Both ends ship in one bundle and nothing persists a message, so the two ends
// never disagree about this protocol. This module imports only types from the
// protobuf schema and the key bundle, so that both bundles can load it.
// ---------------------------------------------------------------------------

/**
 * The first correlation id that the main thread allocates on a channel. No
 * request ever receives 0, so a zero-valued id stays distinguishable from an
 * allocated one; the Go tunnel client's allocator skips 0 for the same reason
 * (backend/tunnel/channel.go).
 */
export const FIRST_REQUEST_ID = 1

/**
 * A ChannelError as a structured clone carries it. A clone keeps no class, so
 * the worker posts these fields, and the main thread builds the ChannelError
 * again with `channelErrorOf`.
 */
export interface SessionError {
  source: ChannelErrorSource
  message: string
  code: number
  fatal: boolean
  disconnected: boolean
}

// ---- Commands: main thread -> session worker ----

/** Set the debug flag of the worker's logger to the main thread's. Posted first after a spawn, and on each change. */
export interface ConfigureCommand { type: 'configure', debug: boolean }

/** Build handshake message 1 for a worker's keys. Answered by `handshakeBegun` or `requestFailed`. */
export interface BeginHandshakeCommand { type: 'beginHandshake', requestId: number, mode: EncryptionMode, keys: WorkerKeyBundle }

/**
 * Complete a handshake with the Worker's message 2, and register the session of
 * `channelId`. Answered by `requestDone` or `requestFailed`.
 */
export interface FinishHandshakeCommand {
  type: 'finishHandshake'
  requestId: number
  /** The requestId of the BeginHandshakeCommand that began the handshake. */
  handshakeId: number
  channelId: string
  payload: Uint8Array
  /** The largest inbound message that the session reassembles (see maxReassembledMessageSize). */
  maxReassembledMessageSize: number
}

/** Zero the state of a handshake that will not finish. Answered by nothing. */
export interface AbandonHandshakeCommand { type: 'abandonHandshake', handshakeId: number }

/**
 * Encrypt one message, and post its frames as `wire` events. `startsRequest`
 * is true for the message that opens a request or a stream, which makes its
 * correlation id live until a `release`.
 */
export interface SendCommand { type: 'send', channelId: string, correlationId: number, plaintext: Uint8Array, startsRequest: boolean }

/** The request or the stream of `correlationId` ended on the main thread. */
export interface ReleaseCommand { type: 'release', channelId: string, correlationId: number }

/**
 * One inbound frame to decrypt. `correlationId` keeps the wire's uint64, because
 * the worker checks its range after the decrypt.
 */
export interface InboundCommand { type: 'inbound', channelId: string, correlationId: bigint, flags: ChannelMessageFlags, ciphertext: Uint8Array }

/**
 * Close the session past its hard ceiling, or rekey it when a rekey is due, then
 * answer. Answered by `requestDone` or `requestFailed`.
 */
export interface EnsureReadyCommand { type: 'ensureReady', requestId: number, channelId: string }

/** Rekey every session whose rekey is due, as the idle timer does. Posted after a wake. */
export interface CheckRekeyCommand { type: 'checkRekey' }

/** The main thread closed the channel: zero and drop its session. Answered by nothing. */
export interface CloseCommand { type: 'close', channelId: string }

/** Zero every session and handshake, then stop the worker. Answered by nothing. */
export interface ShutdownCommand { type: 'shutdown' }

export type SessionCommand
  = | ConfigureCommand | BeginHandshakeCommand | FinishHandshakeCommand | AbandonHandshakeCommand
    | SendCommand | ReleaseCommand | InboundCommand | EnsureReadyCommand | CheckRekeyCommand
    | CloseCommand | ShutdownCommand

// ---- Events: session worker -> main thread ----

/** Handshake message 1 of the BeginHandshakeCommand `requestId`. */
export interface HandshakeBegunEvent { type: 'handshakeBegun', requestId: number, message1: Uint8Array }

/** The request `requestId` succeeded. */
export interface RequestDoneEvent { type: 'requestDone', requestId: number }

/** The request `requestId` failed with `error`. */
export interface RequestFailedEvent { type: 'requestFailed', requestId: number, error: SessionError }

/** One framed ChannelMessage to put on the WebSocket, in the order of the events. */
export interface WireEvent { type: 'wire', channelId: string, frame: Uint8Array }

/** A complete, decrypted InnerMessage envelope of a response or a stream message. */
export interface DeliverEvent { type: 'deliver', channelId: string, correlationId: number, plaintext: Uint8Array }

/** An inbound chunked message of `correlationId` broke a reassembly limit. */
export interface ReassemblyFailedEvent { type: 'reassemblyFailed', channelId: string, correlationId: number, message: string }

/**
 * The worker ended the session of `channelId`, or has no session for it. The
 * causes are a decrypt failure, an encrypt failure, a failed rekey and the hard
 * ceiling. The main thread closes the channel with `error`.
 */
export interface SessionEndedEvent { type: 'sessionEnded', channelId: string, error: SessionError }

export type SessionEvent
  = | HandshakeBegunEvent | RequestDoneEvent | RequestFailedEvent | WireEvent
    | DeliverEvent | ReassemblyFailedEvent | SessionEndedEvent

/**
 * The SessionError that carries `err` across the thread boundary. A
 * ChannelError keeps its fields. Anything else is a transport failure that
 * carries the message of `err`.
 */
export function sessionErrorOf(err: unknown): SessionError {
  if (err instanceof ChannelError) {
    return {
      source: err.source,
      message: err.message,
      code: err.code,
      fatal: err.fatal,
      disconnected: err.disconnected,
    }
  }
  return {
    source: 'transport',
    message: formatErrorMessage(err),
    code: 0,
    fatal: false,
    disconnected: true,
  }
}

/** The ChannelError that `error` carries. */
export function channelErrorOf(error: SessionError): ChannelError {
  return new ChannelError(error.source, error.message, {
    code: error.code,
    fatal: error.fatal,
    disconnected: error.disconnected,
  })
}
