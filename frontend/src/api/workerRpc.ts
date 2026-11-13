/**
 * Typed E2EE channel wrappers for Worker RPC calls.
 *
 * All workspace, agent, terminal, file, and git RPCs use encrypted
 * channel calls: Frontend -> Hub (relay) -> Worker, where Hub never
 * sees the plaintext content.
 */

import type { DescMessage, DescMethod, DescMethodUnary, DescService, Message, MessageInitShape, MessageShape } from '@bufbuild/protobuf'
import type { EncryptionMode } from '~/generated/proto/leapmux/v1/channel_pb'
import type { WatchEventsRequestSchema, WatchEventsResponse } from '~/generated/proto/leapmux/v1/watch_pb'
import type { ChannelSocket, ChannelTransport, KeyPinDecision, WorkerKeyBundle } from '~/lib/channel'
import { Code, createClient } from '@connectrpc/connect'
import { getCapabilities, isTauriApp } from '~/api/platformBridge'
import { bufferStreamHandle } from '~/api/streamBuffer'
import { TauriRelayWebSocket } from '~/api/tauriRelaySocket'
import { apiLoadingTimeoutMs, transport } from '~/api/transport'
import { WS_CHANNEL_ROUTE, WS_SUBPROTOCOL_CHANNEL_RELAY } from '~/generated/contracts/wire'
import { WorkerAgentInputService, WorkerAgentService } from '~/generated/proto/leapmux/v1/agent_pb'
import { ChannelService } from '~/generated/proto/leapmux/v1/channel_pb'
import { WorkerFileService } from '~/generated/proto/leapmux/v1/file_pb'
import { WorkerGitService } from '~/generated/proto/leapmux/v1/git_pb'
import { WorkerTerminalService } from '~/generated/proto/leapmux/v1/terminal_pb'
import { WorkerWatchService } from '~/generated/proto/leapmux/v1/watch_pb'
import { WorkerPrivateService } from '~/generated/proto/leapmux/v1/worker_private_pb'
import { WorkerSystemService } from '~/generated/proto/leapmux/v1/worker_system_pb'
import { WorkerTabService } from '~/generated/proto/leapmux/v1/worker_tab_pb'
import { ChannelManager, KeyPinStore } from '~/lib/channel'
import { ChannelError } from '~/lib/channelError'
import { spawnChannelSessionWorker } from '~/lib/channelSessionClient'
import { emitDevEvent } from '~/lib/devInstrument'
import { procedureOf } from '~/lib/rpcProcedure'
import { sleep } from '~/lib/sleep'
import { workerKeyBundleFromWire } from '~/lib/workerKeyBundle'

// ---- Browser-specific channel transport ----

const channelRpcClient = createClient(ChannelService, transport)

// Module-level TOFU pin store. AppShell registers the mismatch prompt after mount
// via setConfirmKeyPin; until then mismatches fail closed (reject).
const keyPinStore = new KeyPinStore({
  confirmKeyPin: async () => 'reject',
})

/** Register the key-pin confirmation callback (called by AppShell). */
export function setConfirmKeyPin(fn: (workerId: string, expectedFingerprint: string, actualFingerprint: string) => Promise<KeyPinDecision>): () => void {
  return keyPinStore.setConfirmKeyPin(fn)
}

// The identity this page believes it is authenticated as. Set by AppShell, which
// owns the auth context; the singleton below is constructed at import time, long
// before that context exists, so it cannot read the store itself.
let expectedUserIdFn: (() => string | undefined) | null = null

/**
 * Register the expected-identity provider (called by AppShell).
 *
 * This is a CROSS-CHECK against the Hub's answer, never a source of identity: the
 * Hub authenticates every channel open and names the user, and that answer is
 * always authoritative. What this catches is the page and the Hub disagreeing —
 * see `expectedUserId` in `~/lib/channel`.
 */
export function setExpectedUserId(fn: () => string | undefined): void {
  expectedUserIdFn = fn
}

class BrowserChannelTransport implements ChannelTransport {
  async getWorkerHandshakeParams(workerId: string): Promise<{ keys: WorkerKeyBundle, encryptionMode: EncryptionMode }> {
    const resp = await channelRpcClient.getWorkerHandshakeParams({ workerId })
    return {
      // Refuses an absent key set and a key of the wrong size, before the
      // caller makes any pin decision.
      keys: workerKeyBundleFromWire(workerId, resp.keys),
      encryptionMode: resp.encryptionMode,
    }
  }

  async openChannel(workerId: string, handshakePayload: Uint8Array): Promise<{ channelId: string, handshakePayload: Uint8Array, userId: string, maxMessageSize: number }> {
    const resp = await channelRpcClient.openChannel({ workerId, handshakePayload })
    return {
      channelId: resp.channelId,
      handshakePayload: resp.handshakePayload,
      userId: resp.userId,
      maxMessageSize: Number(resp.maxMessageSize),
    }
  }

  async closeChannel(channelId: string): Promise<void> {
    await channelRpcClient.closeChannel({ channelId })
  }

  createWebSocket(): ChannelSocket {
    const capabilities = getCapabilities()
    if (isTauriApp() && capabilities.hubTransport === 'proxy') {
      return new TauriRelayWebSocket()
    }

    const wsProtocol = window.location.protocol === 'https:'
      ? 'wss:'
      : 'ws:'
    const wsUrl = `${wsProtocol}//${window.location.host}${WS_CHANNEL_ROUTE}`
    const ws = new WebSocket(wsUrl, [WS_SUBPROTOCOL_CHANNEL_RELAY])
    ws.binaryType = 'arraybuffer'
    return ws
  }
}

export const channelManager = new ChannelManager(new BrowserChannelTransport(), {
  // The manager spawns the worker lazily, at the first open, never at import.
  spawnSessionWorker: spawnChannelSessionWorker,
  rpcTimeoutFn: apiLoadingTimeoutMs,
  expectedUserId: () => expectedUserIdFn?.(),
  keyPins: keyPinStore,
})

// ---------------------------------------------------------------------------
// Generic helper
// ---------------------------------------------------------------------------

/**
 * How many extra attempts an `Unavailable` reply earns, and the base of the
 * exponential backoff between them.
 *
 * Exported so a test can reason about the worst-case wall time rather than
 * hard-coding it: 200 + 400 + 800 = 1.4s of delay on top of the calls.
 */
export const UNAVAILABLE_MAX_RETRIES = 3
export const UNAVAILABLE_RETRY_BASE_MS = 200

/** What every worker RPC takes besides the request. */
interface WorkerCallOptions {
  /** Limits each attempt, not the whole sequence of retries. See `callWorker`. */
  timeoutMs?: number
  /** Stops the call, including during a retry backoff. */
  signal?: AbortSignal
}

/** Whether an error is the worker saying "I could not answer; ask again". */
function isWorkerUnavailable(err: unknown): err is ChannelError {
  // `source === 'rpc'` is the load-bearing half. It is set only from a
  // DECRYPTED InnerRpcResponse.error.code, so the hub cannot forge it, and
  // it separates the worker's own refusal from a transport failure that
  // happens to carry the same numeric code.
  return err instanceof ChannelError && err.source === 'rpc' && err.code === Code.Unavailable
}

/**
 * Every worker RPC goes through here, and every one of them retries an
 * `Unavailable` reply with a bounded exponential backoff.
 *
 * The worker declares that code to mean "the caller should retry": it
 * answers with it when a provider scan did not finish, when a subagent
 * registry is not loaded yet, and when a steering message arrives before
 * the agent can take it. Handling that in one place is the point — a retry
 * hand-rolled in one component hook covered exactly one of those sites and
 * left the other three surfacing as hard failures.
 *
 * The retry respects both bounds a caller sets. An aborted `signal` stops
 * it immediately, including mid-backoff. `timeoutMs` bounds each ATTEMPT,
 * not the sequence, so a caller that sets one should expect the worst case
 * to be roughly four attempts plus 1.4s of backoff.
 *
 * Every retry sends the same request. Tab creation carries a stable identity,
 * so the Worker can resume a pending operation or return its committed result.
 */
async function callWorker<I extends DescMessage, O extends DescMessage>(
  workerId: string,
  method: DescMethodUnary<I, O>,
  req: MessageInitShape<I>,
  opts?: WorkerCallOptions,
): Promise<MessageShape<O>> {
  const procedure = procedureOf(method)
  for (let attempt = 0; ; attempt++) {
    emitDevEvent('leapmux:rpc-send', () => ({
      method: procedure,
      at: performance.now(),
    }))
    try {
      const resp = await channelManager.callWorker(workerId, method, req, opts)
      emitDevEvent('leapmux:rpc-recv', () => ({
        method: procedure,
        at: performance.now(),
        ok: true,
      }))
      return resp
    }
    catch (err) {
      emitDevEvent('leapmux:rpc-recv', () => ({
        method: procedure,
        at: performance.now(),
        ok: false,
      }))
      if (attempt >= UNAVAILABLE_MAX_RETRIES || !isWorkerUnavailable(err) || opts?.signal?.aborted)
        throw err
      await sleep(UNAVAILABLE_RETRY_BASE_MS * (2 ** attempt), opts?.signal)
    }
  }
}

/** Whether a message declares no field, so that an init of it says nothing. */
type HasNoField<M extends DescMessage> = [Exclude<keyof MessageShape<M>, keyof Message>] extends [never] ? true : false

/**
 * One unary worker RPC as a function. A request message with no field asks
 * about the machine, and the channel already identifies the machine, so the
 * caller may leave the request out.
 */
type WorkerMethodCall<M> = M extends DescMethodUnary<infer I, infer O>
  ? HasNoField<I> extends true
    ? (workerId: string, req?: MessageInitShape<I>, opts?: WorkerCallOptions) => Promise<MessageShape<O>>
    : (workerId: string, req: MessageInitShape<I>, opts?: WorkerCallOptions) => Promise<MessageShape<O>>
  : never

/**
 * The unary methods of a worker service, keyed by their local names.
 *
 * A streaming method has no place here, because a stream has a handle rather
 * than one reply: `watchEventsViaChannel` and `ChannelManager.stream` serve
 * those.
 */
export type WorkerServiceClient<S extends DescService> = {
  [K in keyof S['method'] as S['method'][K] extends DescMethodUnary ? K : never]: WorkerMethodCall<S['method'][K]>
}

function isUnary(method: DescMethod): method is DescMethodUnary {
  return method.methodKind === 'unary'
}

/**
 * The client of one worker service, derived from its descriptor, so a new RPC
 * needs no line here. Every method goes through `callWorker`, and takes the
 * same options, so any caller can pass an abort signal or a timeout.
 *
 * Each method is an own property that uses no `this`, so a caller can pass a
 * method on as a function, and a test can spy on one.
 */
function createWorkerServiceClient<S extends DescService>(service: S): WorkerServiceClient<S> {
  const client: Record<string, WorkerMethodCall<DescMethodUnary>> = {}
  for (const method of service.methods) {
    if (isUnary(method))
      client[method.localName] = (workerId, req, opts) => callWorker(workerId, method, req ?? {}, opts)
  }
  // TypeScript cannot follow a loop over the runtime list to the mapped type.
  return client as WorkerServiceClient<S>
}

/**
 * Agents and their transcripts, through the E2EE channel to the worker.
 *
 * - `openAgent` starts the agent's process. The call waits as long as the
 *   loading UI: the channel manager's default timeout is `apiLoadingTimeoutMs`.
 * - `updateAgentGoal` sets, clears, pauses or resumes the agent's session goal.
 *   It is ONE call for all four actions rather than four: they share a
 *   response, a scope and an idempotency story, and the worker refuses an
 *   action the running agent does not support -- so a browser acting on a stale
 *   capability list gets a refusal instead of a silent no-op. The browser
 *   writes no optimistic state. A side-band provider reports the change, while
 *   a text-route provider updates state after queue delivery.
 * - `approveAutoReviewDenial` records the reader's approval of one action that
 *   the agent's automatic reviewer denied, so the agent may retry it once. The
 *   worker reads the denial from its own stored row, so the request states only
 *   which row and which review.
 * - `retryAgentTurn` stops the running turn and sends its input again on
 *   another model, in a new provider thread that excludes the stopped attempt.
 *   File changes and other actions that the stopped attempt already took stay.
 *   The worker refuses the call unless its running agent offers this retry for
 *   this turn, so a browser that acts on a stale offer gets a refusal instead
 *   of a turn that it never saw. The worker answers when it accepts the retry,
 *   and it then stops the turn and starts the new one. The transcript reports a
 *   failure of those later steps, so a resolved call does not mean that the new
 *   turn started.
 * - `listAgentSessions` lists the sessions this worker can offer to resume in
 *   one working directory. The worker merges its own records with the selected
 *   provider's on-disk session store, so the answer changes with BOTH the
 *   provider and the directory -- pass a fresh `signal` per call and let a later
 *   one supersede an earlier.
 */
export const workerAgent = createWorkerServiceClient(WorkerAgentService)

/** The input queue of an agent. */
export const workerAgentInput = createWorkerServiceClient(WorkerAgentInputService)

/**
 * The worker's file system.
 *
 * `listFilesystemRoots` answers with the roots of the worker's own filesystem:
 * `["/"]` on POSIX, the drive roots that carry a volume (`"C:\\"`, `"D:\\"`) on
 * Windows. It lets the tree pick a top node and a drive menu fill itself,
 * instead of the browser guessing either from the shape of a path.
 */
export const workerFile = createWorkerServiceClient(WorkerFileService)

/** Git state and branch operations on the worker. */
export const workerGit = createWorkerServiceClient(WorkerGitService)

/** Tab payloads, which only the E2EE channel carries: the hub never sees a path. */
export const workerPrivate = createWorkerServiceClient(WorkerPrivateService)

/** The worker's own machine. */
export const workerSystem = createWorkerServiceClient(WorkerSystemService)

/**
 * Tabs of any type, and the worktree cleanup of a workspace.
 *
 * `closeTab` closes one tab of any type. Partial failures come back in-band; see
 * CloseTabResponse.
 */
export const workerTab = createWorkerServiceClient(WorkerTabService)

/**
 * Terminals.
 *
 * - `renameTerminal` is the explicit user rename. A PTY's OSC title reaches
 *   clients as a live overlay (TerminalTitleChanged) instead.
 * - `inspectTerminalProcesses` states what is running inside each terminal that
 *   the request lists, so a close guard can state the reason before it tears
 *   the tab down. It is batched: closing a tile asks about every terminal it
 *   holds in one round trip.
 */
export const workerTerminal = createWorkerServiceClient(WorkerTerminalService)

// ---------------------------------------------------------------------------
// Event Streaming (WatchEvents via E2EE channel)
// ---------------------------------------------------------------------------

export interface WatchEventsHandle {
  /** Callback for each WatchEventsResponse received from the Worker. */
  onEvent: (cb: (resp: WatchEventsResponse) => void) => void
  /** Callback for when the stream ends (channel closed or Worker stopped). */
  onEnd: (cb: () => void) => void
  /** Callback for stream errors. */
  onError: (cb: (err: Error) => void) => void
  /**
   * Revise the channel's watch interest in place (InnerStreamRequest).
   * Synchronous enqueue — never awaits channel open.
   */
  update: (request: MessageInitShape<typeof WatchEventsRequestSchema>) => void
  /**
   * Cancel the stream on the wire and drop the local listener.
   */
  close: () => void
}

/**
 * Opens a WatchEvents stream to the Worker via the E2EE channel.
 * Unlike the old Hub WebSocket approach, this goes directly through the
 * encrypted channel: Frontend -> Hub (relay) -> Worker.
 */
export async function watchEventsViaChannel(
  workerId: string,
  request: MessageInitShape<typeof WatchEventsRequestSchema>,
): Promise<WatchEventsHandle> {
  const channelId = await channelManager.getOrOpenChannel(workerId)
  const streamHandle = channelManager.stream(channelId, WorkerWatchService.method.watchEvents, request)

  // Buffer messages that arrive before the consumer wires its callbacks.
  // See streamBuffer.ts for the full rationale.
  const buffered = bufferStreamHandle(streamHandle)

  return {
    onEvent: buffered.onEvent,
    onEnd: buffered.onEnd,
    onError: buffered.onError,
    update: req => streamHandle.send(req),
    close: () => { streamHandle.cancel() },
  }
}
