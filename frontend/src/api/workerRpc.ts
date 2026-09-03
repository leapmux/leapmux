/**
 * Typed E2EE channel wrappers for Worker RPC calls.
 *
 * All workspace, agent, terminal, file, and git RPCs use encrypted
 * channel calls: Frontend -> Hub (relay) -> Worker, where Hub never
 * sees the plaintext content.
 */

import type { MessageInitShape, MessageShape } from '@bufbuild/protobuf'
import type { GenMessage } from '@bufbuild/protobuf/codegenv2'
import type {
  CloseAgentResponse,
  DeleteAgentMessageResponse,
  GetAgentMessageResponse,
  InterruptAgentResponse,
  ListAgentMessagesResponse,
  ListAgentSessionsResponse,
  ListAgentsResponse,
  ListAvailableProvidersResponse,
  ListMessageMarksResponse,
  OpenAgentResponse,
  RenameAgentResponse,
  SendAgentMessageResponse,
  SendAgentRawMessageResponse,
  SendControlResponseResponse,
  UpdateAgentSettingsResponse,
} from '~/generated/proto/leapmux/v1/agent_pb'
import type { EncryptionMode, InnerStreamMessage } from '~/generated/proto/leapmux/v1/channel_pb'
import type {
  ListDirectoryResponse,
  ReadFileResponse,
  StatFileResponse,
} from '~/generated/proto/leapmux/v1/file_pb'
import type {
  CheckoutBranchResponse,
  CreateBranchResponse,
  DeleteBranchResponse,
  GetGitFileStatusResponse,
  GetGitInfoResponse,
  InspectBranchChangeResponse,
  InspectBranchDeletionResponse,
  InspectLastTabCloseResponse,
  InspectWorktreeRemovalResponse,
  ListGitBranchesResponse,
  ListGitWorktreesResponse,
  PushBranchResponse,
  ReadGitFileResponse,
} from '~/generated/proto/leapmux/v1/git_pb'
import type {
  CloseTerminalResponse,
  ListAvailableShellsResponse,
  ListTerminalsResponse,
  OpenTerminalResponse,
  ResizeTerminalResponse,
  RestartTerminalResponse,
  SendInputResponse,
  UpdateTerminalTitleResponse,
} from '~/generated/proto/leapmux/v1/terminal_pb'
import type {
  GetWorkerSystemInfoResponse,
} from '~/generated/proto/leapmux/v1/worker_pb'
import type {
  GetTabPayloadResponse,
  RegisterTabPayloadResponse,
  RevokeTabPayloadResponse,
} from '~/generated/proto/leapmux/v1/worker_private_pb'
import type {
  CleanupWorkspaceResponse,
  WatchEventsResponse,
} from '~/generated/proto/leapmux/v1/workspace_pb'
import type { ChannelSocket, ChannelTransport, KeyPinDecision, WorkerKeyBundle } from '~/lib/channel'
import { create, fromBinary, toBinary, toJsonString } from '@bufbuild/protobuf'
import { Code, createClient } from '@connectrpc/connect'
import { getCapabilities, isTauriApp } from '~/api/platformBridge'
import { bufferStreamHandle } from '~/api/streamBuffer'
import { TauriRelayWebSocket } from '~/api/tauriRelaySocket'
import { apiLoadingTimeoutMs, transport } from '~/api/transport'
import { WS_CHANNEL_ROUTE, WS_SUBPROTOCOL_CHANNEL_RELAY } from '~/generated/contracts/wire'
import {
  CloseAgentRequestSchema,
  CloseAgentResponseSchema,
  DeleteAgentMessageRequestSchema,
  DeleteAgentMessageResponseSchema,
  GetAgentMessageRequestSchema,
  GetAgentMessageResponseSchema,
  InterruptAgentRequestSchema,
  InterruptAgentResponseSchema,
  ListAgentMessagesRequestSchema,
  ListAgentMessagesResponseSchema,
  ListAgentSessionsRequestSchema,
  ListAgentSessionsResponseSchema,
  ListAgentsRequestSchema,
  ListAgentsResponseSchema,
  ListAvailableProvidersRequestSchema,
  ListAvailableProvidersResponseSchema,
  ListMessageMarksRequestSchema,
  ListMessageMarksResponseSchema,
  OpenAgentRequestSchema,
  OpenAgentResponseSchema,
  RenameAgentRequestSchema,
  RenameAgentResponseSchema,
  SendAgentMessageRequestSchema,
  SendAgentMessageResponseSchema,
  SendAgentRawMessageRequestSchema,
  SendAgentRawMessageResponseSchema,
  SendControlResponseRequestSchema,
  SendControlResponseResponseSchema,
  UpdateAgentSettingsRequestSchema,
  UpdateAgentSettingsResponseSchema,
} from '~/generated/proto/leapmux/v1/agent_pb'
import { ChannelService } from '~/generated/proto/leapmux/v1/channel_pb'
import {
  ListDirectoryRequestSchema,
  ListDirectoryResponseSchema,
  ReadFileRequestSchema,
  ReadFileResponseSchema,
  StatFileRequestSchema,
  StatFileResponseSchema,
} from '~/generated/proto/leapmux/v1/file_pb'
import {
  CheckoutBranchRequestSchema,
  CheckoutBranchResponseSchema,
  CreateBranchRequestSchema,
  CreateBranchResponseSchema,
  DeleteBranchRequestSchema,
  DeleteBranchResponseSchema,
  GetGitFileStatusRequestSchema,
  GetGitFileStatusResponseSchema,
  GetGitInfoRequestSchema,
  GetGitInfoResponseSchema,
  InspectBranchChangeRequestSchema,
  InspectBranchChangeResponseSchema,
  InspectBranchDeletionRequestSchema,
  InspectBranchDeletionResponseSchema,
  InspectLastTabCloseRequestSchema,
  InspectLastTabCloseResponseSchema,
  InspectWorktreeRemovalRequestSchema,
  InspectWorktreeRemovalResponseSchema,
  ListGitBranchesRequestSchema,
  ListGitBranchesResponseSchema,
  ListGitWorktreesRequestSchema,
  ListGitWorktreesResponseSchema,
  PushBranchRequestSchema,
  PushBranchResponseSchema,
  ReadGitFileRequestSchema,
  ReadGitFileResponseSchema,
} from '~/generated/proto/leapmux/v1/git_pb'
import {
  CloseTerminalRequestSchema,
  CloseTerminalResponseSchema,
  ListAvailableShellsRequestSchema,
  ListAvailableShellsResponseSchema,
  ListTerminalsRequestSchema,
  ListTerminalsResponseSchema,
  OpenTerminalRequestSchema,
  OpenTerminalResponseSchema,
  ResizeTerminalRequestSchema,
  ResizeTerminalResponseSchema,
  RestartTerminalRequestSchema,
  RestartTerminalResponseSchema,
  SendInputRequestSchema,
  SendInputResponseSchema,
  UpdateTerminalTitleRequestSchema,
  UpdateTerminalTitleResponseSchema,
} from '~/generated/proto/leapmux/v1/terminal_pb'
import {
  GetWorkerSystemInfoRequestSchema,
  GetWorkerSystemInfoResponseSchema,
} from '~/generated/proto/leapmux/v1/worker_pb'
import {
  GetTabPayloadRequestSchema,
  GetTabPayloadResponseSchema,
  RegisterTabPayloadRequestSchema,
  RegisterTabPayloadResponseSchema,
  RevokeTabPayloadRequestSchema,
  RevokeTabPayloadResponseSchema,
} from '~/generated/proto/leapmux/v1/worker_private_pb'
import {
  CleanupWorkspaceRequestSchema,
  CleanupWorkspaceResponseSchema,
  WatchEventsRequestSchema,
  WatchEventsResponseSchema,
} from '~/generated/proto/leapmux/v1/workspace_pb'
import { ChannelManager, KeyPinStore } from '~/lib/channel'
import { ChannelError } from '~/lib/channelError'
import { emitDevEvent } from '~/lib/devInstrument'
import { createLogger } from '~/lib/logger'
import { sleep } from '~/lib/sleep'

const log = createLogger('workerRpc')

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
      keys: {
        x25519PublicKey: resp.publicKey,
        mlkemPublicKey: resp.mlkemPublicKey,
        slhdsaPublicKey: resp.slhdsaPublicKey,
      },
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

    const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const wsUrl = `${wsProtocol}//${window.location.host}${WS_CHANNEL_ROUTE}`
    const ws = new WebSocket(wsUrl, [WS_SUBPROTOCOL_CHANNEL_RELAY])
    ws.binaryType = 'arraybuffer'
    return ws
  }
}

export const channelManager = new ChannelManager(new BrowserChannelTransport(), {
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

/** Whether an error is the worker saying "I could not answer; ask again". */
function isWorkerUnavailable(err: unknown): err is ChannelError {
  // `source === 'rpc'` is the load-bearing half. It is set only from a
  // DECRYPTED InnerRpcResponse.errorCode, so the hub cannot forge it, and
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
 * A retry cannot double-apply a write: every site the worker answers
 * `Unavailable` from returns before it mutates anything.
 */
async function callWorker<
  ReqSchema extends GenMessage<any>,
  RespSchema extends GenMessage<any>,
>(
  workerId: string,
  method: string,
  reqSchema: ReqSchema,
  respSchema: RespSchema,
  req: MessageInitShape<ReqSchema>,
  opts?: { timeoutMs?: number, signal?: AbortSignal },
): Promise<MessageShape<RespSchema>> {
  for (let attempt = 0; ; attempt++) {
    emitDevEvent('leapmux:rpc-send', () => ({ method, at: performance.now() }))
    try {
      const resp = await channelManager.callWorker(workerId, method, reqSchema, respSchema, req, opts)
      emitDevEvent('leapmux:rpc-recv', () => ({ method, at: performance.now(), ok: true }))
      return resp
    }
    catch (err) {
      emitDevEvent('leapmux:rpc-recv', () => ({ method, at: performance.now(), ok: false }))
      if (attempt >= UNAVAILABLE_MAX_RETRIES || !isWorkerUnavailable(err) || opts?.signal?.aborted)
        throw err
      await sleep(UNAVAILABLE_RETRY_BASE_MS * (2 ** attempt), opts?.signal)
    }
  }
}

// ---------------------------------------------------------------------------
// System Info (via E2EE channel to worker)
// ---------------------------------------------------------------------------

export function getWorkerSystemInfo(workerId: string): Promise<GetWorkerSystemInfoResponse> {
  return callWorker(workerId, 'GetWorkerSystemInfo', GetWorkerSystemInfoRequestSchema, GetWorkerSystemInfoResponseSchema, {})
}

// ---------------------------------------------------------------------------
// Workspace Cleanup (via E2EE channel to worker)
// ---------------------------------------------------------------------------

export function cleanupWorkspace(workerId: string, req: MessageInitShape<typeof CleanupWorkspaceRequestSchema>): Promise<CleanupWorkspaceResponse> {
  return callWorker(workerId, 'CleanupWorkspace', CleanupWorkspaceRequestSchema, CleanupWorkspaceResponseSchema, req)
}

// ---------------------------------------------------------------------------
// File-tab paths (E2EE-only — hub never sees the path)
// ---------------------------------------------------------------------------

export function registerTabPayload(
  workerId: string,
  req: MessageInitShape<typeof RegisterTabPayloadRequestSchema>,
): Promise<RegisterTabPayloadResponse> {
  return callWorker(workerId, 'RegisterTabPayload', RegisterTabPayloadRequestSchema, RegisterTabPayloadResponseSchema, req)
}

export function getTabPayload(
  workerId: string,
  req: MessageInitShape<typeof GetTabPayloadRequestSchema>,
): Promise<GetTabPayloadResponse> {
  return callWorker(workerId, 'GetTabPayload', GetTabPayloadRequestSchema, GetTabPayloadResponseSchema, req)
}

export function revokeTabPayload(
  workerId: string,
  req: MessageInitShape<typeof RevokeTabPayloadRequestSchema>,
): Promise<RevokeTabPayloadResponse> {
  return callWorker(workerId, 'RevokeTabPayload', RevokeTabPayloadRequestSchema, RevokeTabPayloadResponseSchema, req)
}

// ---------------------------------------------------------------------------
// Agent
// ---------------------------------------------------------------------------

export function openAgent(workerId: string, req: MessageInitShape<typeof OpenAgentRequestSchema>): Promise<OpenAgentResponse> {
  return callWorker(workerId, 'OpenAgent', OpenAgentRequestSchema, OpenAgentResponseSchema, req, {
    timeoutMs: apiLoadingTimeoutMs(),
  })
}

export function closeAgent(workerId: string, req: MessageInitShape<typeof CloseAgentRequestSchema>): Promise<CloseAgentResponse> {
  return callWorker(workerId, 'CloseAgent', CloseAgentRequestSchema, CloseAgentResponseSchema, req)
}

export function sendAgentMessage(workerId: string, req: MessageInitShape<typeof SendAgentMessageRequestSchema>): Promise<SendAgentMessageResponse> {
  return callWorker(workerId, 'SendAgentMessage', SendAgentMessageRequestSchema, SendAgentMessageResponseSchema, req)
}

export function sendAgentRawMessage(workerId: string, req: MessageInitShape<typeof SendAgentRawMessageRequestSchema>): Promise<SendAgentRawMessageResponse> {
  return callWorker(workerId, 'SendAgentRawMessage', SendAgentRawMessageRequestSchema, SendAgentRawMessageResponseSchema, req)
}

export function listAgents(workerId: string, req: MessageInitShape<typeof ListAgentsRequestSchema>): Promise<ListAgentsResponse> {
  return callWorker(workerId, 'ListAgents', ListAgentsRequestSchema, ListAgentsResponseSchema, req)
}

export function listAgentMessages(workerId: string, req: MessageInitShape<typeof ListAgentMessagesRequestSchema>, opts?: { signal?: AbortSignal }): Promise<ListAgentMessagesResponse> {
  return callWorker(workerId, 'ListAgentMessages', ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, req, opts)
}

export function listMessageMarks(workerId: string, req: MessageInitShape<typeof ListMessageMarksRequestSchema>, opts?: { signal?: AbortSignal }): Promise<ListMessageMarksResponse> {
  return callWorker(workerId, 'ListMessageMarks', ListMessageMarksRequestSchema, ListMessageMarksResponseSchema, req, opts)
}

export function getAgentMessage(workerId: string, req: MessageInitShape<typeof GetAgentMessageRequestSchema>): Promise<GetAgentMessageResponse> {
  return callWorker(workerId, 'GetAgentMessage', GetAgentMessageRequestSchema, GetAgentMessageResponseSchema, req)
}

export function renameAgent(workerId: string, req: MessageInitShape<typeof RenameAgentRequestSchema>): Promise<RenameAgentResponse> {
  return callWorker(workerId, 'RenameAgent', RenameAgentRequestSchema, RenameAgentResponseSchema, req)
}

export function interruptAgent(workerId: string, req: MessageInitShape<typeof InterruptAgentRequestSchema>): Promise<InterruptAgentResponse> {
  return callWorker(workerId, 'InterruptAgent', InterruptAgentRequestSchema, InterruptAgentResponseSchema, req)
}

export function sendControlResponse(workerId: string, req: MessageInitShape<typeof SendControlResponseRequestSchema>): Promise<SendControlResponseResponse> {
  return callWorker(workerId, 'SendControlResponse', SendControlResponseRequestSchema, SendControlResponseResponseSchema, req)
}

export function deleteAgentMessage(workerId: string, req: MessageInitShape<typeof DeleteAgentMessageRequestSchema>): Promise<DeleteAgentMessageResponse> {
  return callWorker(workerId, 'DeleteAgentMessage', DeleteAgentMessageRequestSchema, DeleteAgentMessageResponseSchema, req)
}

export function updateAgentSettings(workerId: string, req: MessageInitShape<typeof UpdateAgentSettingsRequestSchema>): Promise<UpdateAgentSettingsResponse> {
  return callWorker(workerId, 'UpdateAgentSettings', UpdateAgentSettingsRequestSchema, UpdateAgentSettingsResponseSchema, req)
}

/**
 * The sessions this worker can offer to resume in one working directory.
 *
 * The worker merges its own records with the selected provider's on-disk
 * session store, so the answer changes with BOTH the provider and the
 * directory -- pass a fresh `signal` per call and let a later one supersede an
 * earlier.
 */
export function listAgentSessions(
  workerId: string,
  req: MessageInitShape<typeof ListAgentSessionsRequestSchema>,
  opts?: { signal?: AbortSignal },
): Promise<ListAgentSessionsResponse> {
  return callWorker(workerId, 'ListAgentSessions', ListAgentSessionsRequestSchema, ListAgentSessionsResponseSchema, req, opts)
}

export function listAvailableProviders(
  workerId: string,
  opts?: { signal?: AbortSignal },
): Promise<ListAvailableProvidersResponse> {
  return callWorker(workerId, 'ListAvailableProviders', ListAvailableProvidersRequestSchema, ListAvailableProvidersResponseSchema, {}, opts)
}

// ---------------------------------------------------------------------------
// Terminal
// ---------------------------------------------------------------------------

export function openTerminal(workerId: string, req: MessageInitShape<typeof OpenTerminalRequestSchema>): Promise<OpenTerminalResponse> {
  return callWorker(workerId, 'OpenTerminal', OpenTerminalRequestSchema, OpenTerminalResponseSchema, req)
}

export function closeTerminal(workerId: string, req: MessageInitShape<typeof CloseTerminalRequestSchema>): Promise<CloseTerminalResponse> {
  return callWorker(workerId, 'CloseTerminal', CloseTerminalRequestSchema, CloseTerminalResponseSchema, req)
}

export function restartTerminal(workerId: string, req: MessageInitShape<typeof RestartTerminalRequestSchema>): Promise<RestartTerminalResponse> {
  return callWorker(workerId, 'RestartTerminal', RestartTerminalRequestSchema, RestartTerminalResponseSchema, req)
}

export function sendInput(workerId: string, req: MessageInitShape<typeof SendInputRequestSchema>, opts?: { timeoutMs?: number, signal?: AbortSignal }): Promise<SendInputResponse> {
  return callWorker(workerId, 'SendInput', SendInputRequestSchema, SendInputResponseSchema, req, opts)
}

export function resizeTerminal(workerId: string, req: MessageInitShape<typeof ResizeTerminalRequestSchema>): Promise<ResizeTerminalResponse> {
  return callWorker(workerId, 'ResizeTerminal', ResizeTerminalRequestSchema, ResizeTerminalResponseSchema, req)
}

/** Explicit user rename — PTY OSC titles are persisted worker-side instead. */
export function updateTerminalTitle(workerId: string, req: MessageInitShape<typeof UpdateTerminalTitleRequestSchema>): Promise<UpdateTerminalTitleResponse> {
  return callWorker(workerId, 'UpdateTerminalTitle', UpdateTerminalTitleRequestSchema, UpdateTerminalTitleResponseSchema, req)
}

export function listTerminals(workerId: string, req: MessageInitShape<typeof ListTerminalsRequestSchema>): Promise<ListTerminalsResponse> {
  return callWorker(workerId, 'ListTerminals', ListTerminalsRequestSchema, ListTerminalsResponseSchema, req)
}

export function listAvailableShells(workerId: string, req: MessageInitShape<typeof ListAvailableShellsRequestSchema>): Promise<ListAvailableShellsResponse> {
  return callWorker(workerId, 'ListAvailableShells', ListAvailableShellsRequestSchema, ListAvailableShellsResponseSchema, req)
}

// ---------------------------------------------------------------------------
// File
// ---------------------------------------------------------------------------

export function listDirectory(workerId: string, req: MessageInitShape<typeof ListDirectoryRequestSchema>): Promise<ListDirectoryResponse> {
  return callWorker(workerId, 'ListDirectory', ListDirectoryRequestSchema, ListDirectoryResponseSchema, req)
}

export function readFile(workerId: string, req: MessageInitShape<typeof ReadFileRequestSchema>): Promise<ReadFileResponse> {
  return callWorker(workerId, 'ReadFile', ReadFileRequestSchema, ReadFileResponseSchema, req)
}

export function statFile(workerId: string, req: MessageInitShape<typeof StatFileRequestSchema>): Promise<StatFileResponse> {
  return callWorker(workerId, 'StatFile', StatFileRequestSchema, StatFileResponseSchema, req)
}

// ---------------------------------------------------------------------------
// Git
// ---------------------------------------------------------------------------

export function getGitInfo(workerId: string, req: MessageInitShape<typeof GetGitInfoRequestSchema>, opts?: { signal?: AbortSignal }): Promise<GetGitInfoResponse> {
  return callWorker(workerId, 'GetGitInfo', GetGitInfoRequestSchema, GetGitInfoResponseSchema, req, opts)
}

export function getGitFileStatus(workerId: string, req: MessageInitShape<typeof GetGitFileStatusRequestSchema>, opts?: { signal?: AbortSignal }): Promise<GetGitFileStatusResponse> {
  return callWorker(workerId, 'GetGitFileStatus', GetGitFileStatusRequestSchema, GetGitFileStatusResponseSchema, req, opts)
}

export function readGitFile(workerId: string, req: MessageInitShape<typeof ReadGitFileRequestSchema>): Promise<ReadGitFileResponse> {
  return callWorker(workerId, 'ReadGitFile', ReadGitFileRequestSchema, ReadGitFileResponseSchema, req)
}

export function inspectLastTabClose(workerId: string, req: MessageInitShape<typeof InspectLastTabCloseRequestSchema>, opts?: { signal?: AbortSignal }): Promise<InspectLastTabCloseResponse> {
  return callWorker(workerId, 'InspectLastTabClose', InspectLastTabCloseRequestSchema, InspectLastTabCloseResponseSchema, req, opts)
}

export function pushBranch(workerId: string, req: MessageInitShape<typeof PushBranchRequestSchema>): Promise<PushBranchResponse> {
  return callWorker(workerId, 'PushBranch', PushBranchRequestSchema, PushBranchResponseSchema, req)
}

export function listGitBranches(workerId: string, req: MessageInitShape<typeof ListGitBranchesRequestSchema>): Promise<ListGitBranchesResponse> {
  return callWorker(workerId, 'ListGitBranches', ListGitBranchesRequestSchema, ListGitBranchesResponseSchema, req)
}

export function listGitWorktrees(workerId: string, req: MessageInitShape<typeof ListGitWorktreesRequestSchema>): Promise<ListGitWorktreesResponse> {
  return callWorker(workerId, 'ListGitWorktrees', ListGitWorktreesRequestSchema, ListGitWorktreesResponseSchema, req)
}

export function inspectBranchDeletion(workerId: string, req: MessageInitShape<typeof InspectBranchDeletionRequestSchema>, opts?: { signal?: AbortSignal }): Promise<InspectBranchDeletionResponse> {
  return callWorker(workerId, 'InspectBranchDeletion', InspectBranchDeletionRequestSchema, InspectBranchDeletionResponseSchema, req, opts)
}

export function inspectWorktreeRemoval(workerId: string, req: MessageInitShape<typeof InspectWorktreeRemovalRequestSchema>, opts?: { signal?: AbortSignal }): Promise<InspectWorktreeRemovalResponse> {
  return callWorker(workerId, 'InspectWorktreeRemoval', InspectWorktreeRemovalRequestSchema, InspectWorktreeRemovalResponseSchema, req, opts)
}

export function inspectBranchChange(workerId: string, req: MessageInitShape<typeof InspectBranchChangeRequestSchema>, opts?: { signal?: AbortSignal }): Promise<InspectBranchChangeResponse> {
  return callWorker(workerId, 'InspectBranchChange', InspectBranchChangeRequestSchema, InspectBranchChangeResponseSchema, req, opts)
}

export function checkoutBranch(workerId: string, req: MessageInitShape<typeof CheckoutBranchRequestSchema>): Promise<CheckoutBranchResponse> {
  return callWorker(workerId, 'CheckoutBranch', CheckoutBranchRequestSchema, CheckoutBranchResponseSchema, req)
}

export function createBranch(workerId: string, req: MessageInitShape<typeof CreateBranchRequestSchema>): Promise<CreateBranchResponse> {
  return callWorker(workerId, 'CreateBranch', CreateBranchRequestSchema, CreateBranchResponseSchema, req)
}

export function deleteBranch(workerId: string, req: MessageInitShape<typeof DeleteBranchRequestSchema>): Promise<DeleteBranchResponse> {
  return callWorker(workerId, 'DeleteBranch', DeleteBranchRequestSchema, DeleteBranchResponseSchema, req)
}

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
  const msg = create(WatchEventsRequestSchema, request)
  const payload = toBinary(WatchEventsRequestSchema, msg)

  const streamHandle = channelManager.stream(channelId, 'WatchEvents', payload)

  // Buffer messages that arrive before the consumer wires its callbacks.
  // See streamBuffer.ts for the full rationale.
  const buffered = bufferStreamHandle<InnerStreamMessage, WatchEventsResponse>(streamHandle, (msg) => {
    const resp = fromBinary(WatchEventsResponseSchema, msg.payload)
    log.debug('WatchEvents stream message', { response: toJsonString(WatchEventsResponseSchema, resp) })
    return resp
  })

  return {
    onEvent: buffered.onEvent,
    onEnd: buffered.onEnd,
    onError: buffered.onError,
    update: (req) => {
      const next = create(WatchEventsRequestSchema, req)
      streamHandle.send(toBinary(WatchEventsRequestSchema, next))
    },
    close: () => { streamHandle.cancel() },
  }
}
