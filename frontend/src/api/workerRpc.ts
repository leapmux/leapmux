/**
 * Typed E2EE channel wrappers for Worker RPC calls.
 *
 * All workspace, agent, terminal, file, and git RPCs use encrypted
 * channel calls: Frontend -> Hub (relay) -> Worker, where Hub never
 * sees the plaintext content.
 */

import type { DescMessage, DescMethodUnary, MessageInitShape, MessageShape } from '@bufbuild/protobuf'
import type {
  ApproveAutoReviewDenialRequestSchema,
  ApproveAutoReviewDenialResponse,
  BeginQueuedAgentInputEditRequestSchema,
  BeginQueuedAgentInputEditResponse,
  CancelQueuedAgentInputEditRequestSchema,
  CancelQueuedAgentInputEditResponse,
  DeleteQueuedAgentInputRequestSchema,
  DeleteQueuedAgentInputResponse,
  EnqueueAgentInputRequestSchema,
  EnqueueAgentInputResponse,
  GetAgentMessageRequestSchema,
  GetAgentMessageResponse,
  GetAgentSpanMessagesRequestSchema,
  GetAgentSpanMessagesResponse,
  InterruptAgentRequestSchema,
  InterruptAgentResponse,
  ListAgentMessagesRequestSchema,
  ListAgentMessagesResponse,
  ListAgentSessionsRequestSchema,
  ListAgentSessionsResponse,
  ListAgentsRequestSchema,
  ListAgentsResponse,
  ListAvailableProvidersResponse,
  ListMessageMarksRequestSchema,

  ListMessageMarksResponse,
  MoveQueuedAgentInputRequestSchema,
  MoveQueuedAgentInputResponse,
  OpenAgentRequestSchema,
  OpenAgentResponse,
  PreemptQueuedAgentInputRequestSchema,
  PreemptQueuedAgentInputResponse,
  RenameAgentRequestSchema,
  RenameAgentResponse,
  RetryAgentTurnRequestSchema,
  RetryAgentTurnResponse,
  RetryQueuedAgentInputRequestSchema,
  RetryQueuedAgentInputResponse,
  SendControlResponseRequestSchema,
  SendControlResponseResponse,
  SetAgentInputQueuePausedRequestSchema,
  SetAgentInputQueuePausedResponse,
  SteerQueuedAgentInputRequestSchema,
  SteerQueuedAgentInputResponse,
  UpdateAgentGoalRequestSchema,
  UpdateAgentGoalResponse,
  UpdateAgentSettingsRequestSchema,
  UpdateAgentSettingsResponse,
  UpdateQueuedAgentInputRequestSchema,
  UpdateQueuedAgentInputResponse,
} from '~/generated/proto/leapmux/v1/agent_pb'
import type { EncryptionMode, InnerStreamMessage } from '~/generated/proto/leapmux/v1/channel_pb'
import type {
  ListDirectoryRequestSchema,
  ListDirectoryResponse,
  ListFilesystemRootsResponse,
  ReadFileRequestSchema,

  ReadFileResponse,
  StatFileRequestSchema,
  StatFileResponse,
} from '~/generated/proto/leapmux/v1/file_pb'
import type {
  CheckoutBranchRequestSchema,
  CheckoutBranchResponse,
  CreateBranchRequestSchema,
  CreateBranchResponse,
  DeleteBranchRequestSchema,
  DeleteBranchResponse,
  GetGitFileStatusRequestSchema,
  GetGitFileStatusResponse,
  GetGitInfoRequestSchema,
  GetGitInfoResponse,
  InspectBranchChangeRequestSchema,
  InspectBranchChangeResponse,
  InspectBranchDeletionRequestSchema,

  InspectBranchDeletionResponse,
  InspectLastTabCloseRequestSchema,
  InspectLastTabCloseResponse,
  InspectWorktreeRemovalRequestSchema,
  InspectWorktreeRemovalResponse,
  ListGitBranchesRequestSchema,
  ListGitBranchesResponse,
  ListGitWorktreesRequestSchema,
  ListGitWorktreesResponse,
  PushBranchRequestSchema,
  PushBranchResponse,
  ReadGitFileRequestSchema,
  ReadGitFileResponse,
} from '~/generated/proto/leapmux/v1/git_pb'
import type {
  InspectTerminalProcessesRequestSchema,
  InspectTerminalProcessesResponse,
  ListAvailableShellsRequestSchema,
  ListAvailableShellsResponse,
  ListTerminalsRequestSchema,
  ListTerminalsResponse,
  OpenTerminalRequestSchema,

  OpenTerminalResponse,
  RenameTerminalRequestSchema,
  RenameTerminalResponse,
  ResizeTerminalRequestSchema,
  ResizeTerminalResponse,
  RestartTerminalRequestSchema,
  RestartTerminalResponse,
  SendInputRequestSchema,
  SendInputResponse,
} from '~/generated/proto/leapmux/v1/terminal_pb'
import type { WatchEventsResponse } from '~/generated/proto/leapmux/v1/watch_pb'
import type {
  GetTabPayloadRequestSchema,
  GetTabPayloadResponse,
  RegisterTabPayloadRequestSchema,

  RegisterTabPayloadResponse,
} from '~/generated/proto/leapmux/v1/worker_private_pb'
import type { GetWorkerSystemInfoResponse } from '~/generated/proto/leapmux/v1/worker_system_pb'
import type {
  CleanupWorkspaceRequestSchema,
  CleanupWorkspaceResponse,
  CloseTabRequestSchema,
  CloseTabResponse,
} from '~/generated/proto/leapmux/v1/worker_tab_pb'
import type { ChannelSocket, ChannelTransport, KeyPinDecision, WorkerKeyBundle } from '~/lib/channel'
import { create, fromBinary, toBinary, toJsonString } from '@bufbuild/protobuf'
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
import { WatchEventsRequestSchema, WatchEventsResponseSchema, WorkerWatchService } from '~/generated/proto/leapmux/v1/watch_pb'
import { WorkerPrivateService } from '~/generated/proto/leapmux/v1/worker_private_pb'
import { WorkerSystemService } from '~/generated/proto/leapmux/v1/worker_system_pb'
import { WorkerTabService } from '~/generated/proto/leapmux/v1/worker_tab_pb'
import { ChannelManager, KeyPinStore } from '~/lib/channel'
import { ChannelError } from '~/lib/channelError'
import { emitDevEvent } from '~/lib/devInstrument'
import { createLogger } from '~/lib/logger'
import { procedureOf } from '~/lib/rpcProcedure'
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
        x25519PublicKey: resp.keys?.x25519 ?? new Uint8Array(),
        mlkemPublicKey: resp.keys?.mlkem ?? new Uint8Array(),
        slhdsaPublicKey: resp.keys?.slhdsa ?? new Uint8Array(),
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
async function callWorker<I extends DescMessage, O extends DescMessage>(
  workerId: string,
  method: DescMethodUnary<I, O>,
  req: MessageInitShape<I>,
  opts?: { timeoutMs?: number, signal?: AbortSignal },
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

// ---------------------------------------------------------------------------
// System Info (via E2EE channel to worker)
// ---------------------------------------------------------------------------

export function getWorkerSystemInfo(workerId: string): Promise<GetWorkerSystemInfoResponse> {
  return callWorker(workerId, WorkerSystemService.method.getWorkerSystemInfo, {})
}

// ---------------------------------------------------------------------------
// Workspace Cleanup (via E2EE channel to worker)
// ---------------------------------------------------------------------------

/** Closes one tab of any type. Partial failures come back in-band; see CloseTabResponse. */
export function closeTab(workerId: string, req: MessageInitShape<typeof CloseTabRequestSchema>): Promise<CloseTabResponse> {
  return callWorker(workerId, WorkerTabService.method.closeTab, req)
}

export function cleanupWorkspace(workerId: string, req: MessageInitShape<typeof CleanupWorkspaceRequestSchema>): Promise<CleanupWorkspaceResponse> {
  return callWorker(workerId, WorkerTabService.method.cleanupWorkspace, req)
}

// ---------------------------------------------------------------------------
// File-tab paths (E2EE-only — hub never sees the path)
// ---------------------------------------------------------------------------

export function registerTabPayload(
  workerId: string,
  req: MessageInitShape<typeof RegisterTabPayloadRequestSchema>,
): Promise<RegisterTabPayloadResponse> {
  return callWorker(workerId, WorkerPrivateService.method.registerTabPayload, req)
}

export function getTabPayload(
  workerId: string,
  req: MessageInitShape<typeof GetTabPayloadRequestSchema>,
): Promise<GetTabPayloadResponse> {
  return callWorker(workerId, WorkerPrivateService.method.getTabPayload, req)
}

// ---------------------------------------------------------------------------
// Agent
// ---------------------------------------------------------------------------

export function openAgent(workerId: string, req: MessageInitShape<typeof OpenAgentRequestSchema>): Promise<OpenAgentResponse> {
  return callWorker(workerId, WorkerAgentService.method.openAgent, req, {
    timeoutMs: apiLoadingTimeoutMs(),
  })
}

export function enqueueAgentInput(workerId: string, req: MessageInitShape<typeof EnqueueAgentInputRequestSchema>): Promise<EnqueueAgentInputResponse> {
  return callWorker(workerId, WorkerAgentInputService.method.enqueueAgentInput, req)
}

export function beginQueuedAgentInputEdit(workerId: string, req: MessageInitShape<typeof BeginQueuedAgentInputEditRequestSchema>): Promise<BeginQueuedAgentInputEditResponse> {
  return callWorker(workerId, WorkerAgentInputService.method.beginQueuedAgentInputEdit, req)
}

export function updateQueuedAgentInput(workerId: string, req: MessageInitShape<typeof UpdateQueuedAgentInputRequestSchema>): Promise<UpdateQueuedAgentInputResponse> {
  return callWorker(workerId, WorkerAgentInputService.method.updateQueuedAgentInput, req)
}

export function cancelQueuedAgentInputEdit(workerId: string, req: MessageInitShape<typeof CancelQueuedAgentInputEditRequestSchema>): Promise<CancelQueuedAgentInputEditResponse> {
  return callWorker(workerId, WorkerAgentInputService.method.cancelQueuedAgentInputEdit, req)
}

export function deleteQueuedAgentInput(workerId: string, req: MessageInitShape<typeof DeleteQueuedAgentInputRequestSchema>): Promise<DeleteQueuedAgentInputResponse> {
  return callWorker(workerId, WorkerAgentInputService.method.deleteQueuedAgentInput, req)
}

export function moveQueuedAgentInput(workerId: string, req: MessageInitShape<typeof MoveQueuedAgentInputRequestSchema>): Promise<MoveQueuedAgentInputResponse> {
  return callWorker(workerId, WorkerAgentInputService.method.moveQueuedAgentInput, req)
}

export function setAgentInputQueuePaused(workerId: string, req: MessageInitShape<typeof SetAgentInputQueuePausedRequestSchema>): Promise<SetAgentInputQueuePausedResponse> {
  return callWorker(workerId, WorkerAgentInputService.method.setAgentInputQueuePaused, req)
}

export function steerQueuedAgentInput(workerId: string, req: MessageInitShape<typeof SteerQueuedAgentInputRequestSchema>): Promise<SteerQueuedAgentInputResponse> {
  return callWorker(workerId, WorkerAgentInputService.method.steerQueuedAgentInput, req)
}

export function preemptQueuedAgentInput(workerId: string, req: MessageInitShape<typeof PreemptQueuedAgentInputRequestSchema>): Promise<PreemptQueuedAgentInputResponse> {
  return callWorker(workerId, WorkerAgentInputService.method.preemptQueuedAgentInput, req)
}

export function retryQueuedAgentInput(workerId: string, req: MessageInitShape<typeof RetryQueuedAgentInputRequestSchema>): Promise<RetryQueuedAgentInputResponse> {
  return callWorker(workerId, WorkerAgentInputService.method.retryQueuedAgentInput, req)
}

export function listAgents(workerId: string, req: MessageInitShape<typeof ListAgentsRequestSchema>): Promise<ListAgentsResponse> {
  return callWorker(workerId, WorkerAgentService.method.listAgents, req)
}

export function listAgentMessages(workerId: string, req: MessageInitShape<typeof ListAgentMessagesRequestSchema>, opts?: { signal?: AbortSignal }): Promise<ListAgentMessagesResponse> {
  return callWorker(workerId, WorkerAgentService.method.listAgentMessages, req, opts)
}

export function listMessageMarks(workerId: string, req: MessageInitShape<typeof ListMessageMarksRequestSchema>, opts?: { signal?: AbortSignal }): Promise<ListMessageMarksResponse> {
  return callWorker(workerId, WorkerAgentService.method.listMessageMarks, req, opts)
}

export function getAgentMessage(workerId: string, req: MessageInitShape<typeof GetAgentMessageRequestSchema>, opts?: { signal?: AbortSignal }): Promise<GetAgentMessageResponse> {
  return callWorker(workerId, WorkerAgentService.method.getAgentMessage, req, opts)
}

export function getAgentSpanMessages(workerId: string, req: MessageInitShape<typeof GetAgentSpanMessagesRequestSchema>, opts?: { signal?: AbortSignal }): Promise<GetAgentSpanMessagesResponse> {
  return callWorker(workerId, WorkerAgentService.method.getAgentSpanMessages, req, opts)
}

export function renameAgent(workerId: string, req: MessageInitShape<typeof RenameAgentRequestSchema>): Promise<RenameAgentResponse> {
  return callWorker(workerId, WorkerAgentService.method.renameAgent, req)
}

export function interruptAgent(workerId: string, req: MessageInitShape<typeof InterruptAgentRequestSchema>): Promise<InterruptAgentResponse> {
  return callWorker(workerId, WorkerAgentService.method.interruptAgent, req)
}

/**
 * Set, clear, pause or resume the agent's session goal.
 *
 * ONE call for all four actions rather than four wrappers: they share a
 * response, a scope and an idempotency story, and the worker refuses an action
 * the running agent does not support -- so a browser acting on a stale
 * capability list gets a refusal instead of a silent no-op.
 *
 * The browser writes no optimistic state. A side-band provider reports the
 * change, while a text-route provider updates state after queue delivery.
 */
export function updateAgentGoal(workerId: string, req: MessageInitShape<typeof UpdateAgentGoalRequestSchema>): Promise<UpdateAgentGoalResponse> {
  return callWorker(workerId, WorkerAgentService.method.updateAgentGoal, req)
}

/**
 * Record the reader's approval of one action that the agent's automatic reviewer
 * denied, so the agent may retry it once. The worker reads the denial from its own
 * stored row, so the request states only which row and which review.
 */
export function approveAutoReviewDenial(workerId: string, req: MessageInitShape<typeof ApproveAutoReviewDenialRequestSchema>): Promise<ApproveAutoReviewDenialResponse> {
  return callWorker(workerId, WorkerAgentService.method.approveAutoReviewDenial, req)
}

/**
 * Stop the running turn and send its input again on another model, in a new
 * provider thread that excludes the stopped attempt. File changes and other
 * actions that the stopped attempt already took stay.
 *
 * The worker refuses the call unless its running agent offers this retry for
 * this turn, so a browser that acts on a stale offer gets a refusal instead of
 * a turn that it never saw. The worker answers when it accepts the retry, and
 * it then stops the turn and starts the new one. The transcript reports a
 * failure of those later steps, so a resolved call does not mean that the new
 * turn started.
 */
export function retryAgentTurn(workerId: string, req: MessageInitShape<typeof RetryAgentTurnRequestSchema>): Promise<RetryAgentTurnResponse> {
  return callWorker(workerId, WorkerAgentService.method.retryAgentTurn, req)
}

export function sendControlResponse(workerId: string, req: MessageInitShape<typeof SendControlResponseRequestSchema>): Promise<SendControlResponseResponse> {
  return callWorker(workerId, WorkerAgentService.method.sendControlResponse, req)
}

export function updateAgentSettings(workerId: string, req: MessageInitShape<typeof UpdateAgentSettingsRequestSchema>): Promise<UpdateAgentSettingsResponse> {
  return callWorker(workerId, WorkerAgentService.method.updateAgentSettings, req)
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
  return callWorker(workerId, WorkerAgentService.method.listAgentSessions, req, opts)
}

export function listAvailableProviders(
  workerId: string,
  opts?: { signal?: AbortSignal },
): Promise<ListAvailableProvidersResponse> {
  return callWorker(workerId, WorkerAgentService.method.listAvailableProviders, {}, opts)
}

// ---------------------------------------------------------------------------
// Terminal
// ---------------------------------------------------------------------------

export function openTerminal(workerId: string, req: MessageInitShape<typeof OpenTerminalRequestSchema>): Promise<OpenTerminalResponse> {
  return callWorker(workerId, WorkerTerminalService.method.openTerminal, req)
}

export function restartTerminal(workerId: string, req: MessageInitShape<typeof RestartTerminalRequestSchema>): Promise<RestartTerminalResponse> {
  return callWorker(workerId, WorkerTerminalService.method.restartTerminal, req)
}

export function sendInput(workerId: string, req: MessageInitShape<typeof SendInputRequestSchema>, opts?: { timeoutMs?: number, signal?: AbortSignal }): Promise<SendInputResponse> {
  return callWorker(workerId, WorkerTerminalService.method.sendInput, req, opts)
}

export function resizeTerminal(workerId: string, req: MessageInitShape<typeof ResizeTerminalRequestSchema>): Promise<ResizeTerminalResponse> {
  return callWorker(workerId, WorkerTerminalService.method.resizeTerminal, req)
}

/** Explicit user rename. A PTY's OSC title reaches clients as a live overlay (TerminalTitleChanged) instead. */
export function renameTerminal(workerId: string, req: MessageInitShape<typeof RenameTerminalRequestSchema>): Promise<RenameTerminalResponse> {
  return callWorker(workerId, WorkerTerminalService.method.renameTerminal, req)
}

export function listTerminals(workerId: string, req: MessageInitShape<typeof ListTerminalsRequestSchema>): Promise<ListTerminalsResponse> {
  return callWorker(workerId, WorkerTerminalService.method.listTerminals, req)
}

/**
 * What is running inside each named terminal, so a close guard can state the
 * reason before it tears the tab down. Batched: closing a tile asks about every
 * terminal it holds in one round trip.
 */
export function inspectTerminalProcesses(workerId: string, req: MessageInitShape<typeof InspectTerminalProcessesRequestSchema>): Promise<InspectTerminalProcessesResponse> {
  return callWorker(workerId, WorkerTerminalService.method.inspectTerminalProcesses, req)
}

export function listAvailableShells(workerId: string, req: MessageInitShape<typeof ListAvailableShellsRequestSchema>): Promise<ListAvailableShellsResponse> {
  return callWorker(workerId, WorkerTerminalService.method.listAvailableShells, req)
}

// ---------------------------------------------------------------------------
// File
// ---------------------------------------------------------------------------

export function listDirectory(workerId: string, req: MessageInitShape<typeof ListDirectoryRequestSchema>): Promise<ListDirectoryResponse> {
  return callWorker(workerId, WorkerFileService.method.listDirectory, req)
}

/**
 * The roots of the worker's own filesystem: `["/"]` on POSIX, the drive roots
 * that carry a volume (`"C:\\"`, `"D:\\"`) on Windows.
 *
 * Lets the tree pick a top node and a drive menu fill itself, instead of the
 * browser guessing either from the shape of a path.
 *
 * Takes no request, like `getWorkerSystemInfo`: it asks about the machine, and
 * the channel already identifies it.
 */
export function listFilesystemRoots(workerId: string): Promise<ListFilesystemRootsResponse> {
  return callWorker(workerId, WorkerFileService.method.listFilesystemRoots, {})
}

export function readFile(workerId: string, req: MessageInitShape<typeof ReadFileRequestSchema>, opts?: { signal?: AbortSignal }): Promise<ReadFileResponse> {
  return callWorker(workerId, WorkerFileService.method.readFile, req, opts)
}

export function statFile(workerId: string, req: MessageInitShape<typeof StatFileRequestSchema>): Promise<StatFileResponse> {
  return callWorker(workerId, WorkerFileService.method.statFile, req)
}

// ---------------------------------------------------------------------------
// Git
// ---------------------------------------------------------------------------

export function getGitInfo(workerId: string, req: MessageInitShape<typeof GetGitInfoRequestSchema>, opts?: { signal?: AbortSignal }): Promise<GetGitInfoResponse> {
  return callWorker(workerId, WorkerGitService.method.getGitInfo, req, opts)
}

export function getGitFileStatus(workerId: string, req: MessageInitShape<typeof GetGitFileStatusRequestSchema>, opts?: { signal?: AbortSignal }): Promise<GetGitFileStatusResponse> {
  return callWorker(workerId, WorkerGitService.method.getGitFileStatus, req, opts)
}

export function readGitFile(workerId: string, req: MessageInitShape<typeof ReadGitFileRequestSchema>): Promise<ReadGitFileResponse> {
  return callWorker(workerId, WorkerGitService.method.readGitFile, req)
}

export function inspectLastTabClose(workerId: string, req: MessageInitShape<typeof InspectLastTabCloseRequestSchema>, opts?: { signal?: AbortSignal }): Promise<InspectLastTabCloseResponse> {
  return callWorker(workerId, WorkerGitService.method.inspectLastTabClose, req, opts)
}

export function pushBranch(workerId: string, req: MessageInitShape<typeof PushBranchRequestSchema>): Promise<PushBranchResponse> {
  return callWorker(workerId, WorkerGitService.method.pushBranch, req)
}

export function listGitBranches(workerId: string, req: MessageInitShape<typeof ListGitBranchesRequestSchema>): Promise<ListGitBranchesResponse> {
  return callWorker(workerId, WorkerGitService.method.listGitBranches, req)
}

export function listGitWorktrees(workerId: string, req: MessageInitShape<typeof ListGitWorktreesRequestSchema>): Promise<ListGitWorktreesResponse> {
  return callWorker(workerId, WorkerGitService.method.listGitWorktrees, req)
}

export function inspectBranchDeletion(workerId: string, req: MessageInitShape<typeof InspectBranchDeletionRequestSchema>, opts?: { signal?: AbortSignal }): Promise<InspectBranchDeletionResponse> {
  return callWorker(workerId, WorkerGitService.method.inspectBranchDeletion, req, opts)
}

export function inspectWorktreeRemoval(workerId: string, req: MessageInitShape<typeof InspectWorktreeRemovalRequestSchema>, opts?: { signal?: AbortSignal }): Promise<InspectWorktreeRemovalResponse> {
  return callWorker(workerId, WorkerGitService.method.inspectWorktreeRemoval, req, opts)
}

export function inspectBranchChange(workerId: string, req: MessageInitShape<typeof InspectBranchChangeRequestSchema>, opts?: { signal?: AbortSignal }): Promise<InspectBranchChangeResponse> {
  return callWorker(workerId, WorkerGitService.method.inspectBranchChange, req, opts)
}

export function checkoutBranch(workerId: string, req: MessageInitShape<typeof CheckoutBranchRequestSchema>): Promise<CheckoutBranchResponse> {
  return callWorker(workerId, WorkerGitService.method.checkoutBranch, req)
}

export function createBranch(workerId: string, req: MessageInitShape<typeof CreateBranchRequestSchema>): Promise<CreateBranchResponse> {
  return callWorker(workerId, WorkerGitService.method.createBranch, req)
}

export function deleteBranch(workerId: string, req: MessageInitShape<typeof DeleteBranchRequestSchema>): Promise<DeleteBranchResponse> {
  return callWorker(workerId, WorkerGitService.method.deleteBranch, req)
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

  const streamHandle = channelManager.stream(channelId, WorkerWatchService.method.watchEvents, payload)

  // Buffer messages that arrive before the consumer wires its callbacks.
  // See streamBuffer.ts for the full rationale.
  const buffered = bufferStreamHandle<InnerStreamMessage, WatchEventsResponse>(streamHandle, (msg) => {
    const resp = fromBinary(WatchEventsResponseSchema, msg.payload)
    log.debug('WatchEvents stream message', {
      response: toJsonString(WatchEventsResponseSchema, resp),
    })
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
