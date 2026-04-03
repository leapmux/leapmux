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
  ListAgentMessagesResponse,
  ListAgentsResponse,
  ListAvailableProvidersResponse,
  OpenAgentResponse,
  RenameAgentResponse,
  SendAgentMessageResponse,
  SendAgentRawMessageResponse,
  SendControlResponseResponse,
  UpdateAgentSettingsResponse,
} from '~/generated/leapmux/v1/agent_pb'
import type { EncryptionMode, InnerStreamMessage } from '~/generated/leapmux/v1/channel_pb'
import type {
  ListDirectoryResponse,
  ReadFileResponse,
  StatFileResponse,
} from '~/generated/leapmux/v1/file_pb'
import type {
  CheckWorktreeStatusResponse,
  ForceRemoveWorktreeResponse,
  GetGitFileStatusResponse,
  GetGitInfoResponse,
  InspectLastTabCloseResponse,
  KeepWorktreeResponse,
  ListGitBranchesResponse,
  ListGitWorktreesResponse,
  PushBranchForCloseResponse,
  ReadGitFileResponse,
  ScheduleWorktreeDeletionResponse,
} from '~/generated/leapmux/v1/git_pb'
import type {
  CloseTerminalResponse,
  ListAvailableShellsResponse,
  ListTerminalsResponse,
  OpenTerminalResponse,
  ResizeTerminalResponse,
  SendInputResponse,
  UpdateTerminalTitleResponse,
} from '~/generated/leapmux/v1/terminal_pb'
import type {
  GetWorkerSystemInfoResponse,
} from '~/generated/leapmux/v1/worker_pb'
import type {
  CleanupWorkspaceResponse,
  MoveTabWorkspaceResponse,
  WatchEventsResponse,
} from '~/generated/leapmux/v1/workspace_pb'
import type { ChannelTransport, KeyPinDecision, WorkerKeyBundle } from '~/lib/channel'
import { create, fromBinary, toBinary, toJsonString } from '@bufbuild/protobuf'
import { createClient } from '@connectrpc/connect'
import { arrayBufferToBase64, base64ToArrayBuffer, isWailsApp } from '~/api/desktopBridge'
import { getToken, transport } from '~/api/transport'
import {
  CloseAgentRequestSchema,
  CloseAgentResponseSchema,
  DeleteAgentMessageRequestSchema,
  DeleteAgentMessageResponseSchema,
  ListAgentMessagesRequestSchema,
  ListAgentMessagesResponseSchema,
  ListAgentsRequestSchema,
  ListAgentsResponseSchema,
  ListAvailableProvidersRequestSchema,
  ListAvailableProvidersResponseSchema,
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
} from '~/generated/leapmux/v1/agent_pb'
import { ChannelService } from '~/generated/leapmux/v1/channel_pb'
import {
  ListDirectoryRequestSchema,
  ListDirectoryResponseSchema,
  ReadFileRequestSchema,
  ReadFileResponseSchema,
  StatFileRequestSchema,
  StatFileResponseSchema,
} from '~/generated/leapmux/v1/file_pb'
import {
  CheckWorktreeStatusRequestSchema,
  CheckWorktreeStatusResponseSchema,
  ForceRemoveWorktreeRequestSchema,
  ForceRemoveWorktreeResponseSchema,
  GetGitFileStatusRequestSchema,
  GetGitFileStatusResponseSchema,
  GetGitInfoRequestSchema,
  GetGitInfoResponseSchema,
  InspectLastTabCloseRequestSchema,
  InspectLastTabCloseResponseSchema,
  KeepWorktreeRequestSchema,
  KeepWorktreeResponseSchema,
  ListGitBranchesRequestSchema,
  ListGitBranchesResponseSchema,
  ListGitWorktreesRequestSchema,
  ListGitWorktreesResponseSchema,
  PushBranchForCloseRequestSchema,
  PushBranchForCloseResponseSchema,
  ReadGitFileRequestSchema,
  ReadGitFileResponseSchema,
  ScheduleWorktreeDeletionRequestSchema,
  ScheduleWorktreeDeletionResponseSchema,
} from '~/generated/leapmux/v1/git_pb'
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
  SendInputRequestSchema,
  SendInputResponseSchema,
  UpdateTerminalTitleRequestSchema,
  UpdateTerminalTitleResponseSchema,
} from '~/generated/leapmux/v1/terminal_pb'
import {
  GetWorkerSystemInfoRequestSchema,
  GetWorkerSystemInfoResponseSchema,
} from '~/generated/leapmux/v1/worker_pb'
import {
  CleanupWorkspaceRequestSchema,
  CleanupWorkspaceResponseSchema,
  MoveTabWorkspaceRequestSchema,
  MoveTabWorkspaceResponseSchema,
  WatchEventsRequestSchema,
  WatchEventsResponseSchema,
} from '~/generated/leapmux/v1/workspace_pb'
import { ChannelManager } from '~/lib/channel'
import { createLogger } from '~/lib/logger'
import { isSoloMode } from '~/lib/systemInfo'

const log = createLogger('workerRpc')

// ---- Browser-specific channel transport ----

const channelRpcClient = createClient(ChannelService, transport)

// Module-level callbacks set by the UI layer (AppShell).
let confirmKeyPinFn: ((workerId: string, expectedFingerprint: string, actualFingerprint: string) => Promise<KeyPinDecision>) | null = null
let getUserIdFn: (() => string) | null = null

/** Register the key-pin confirmation callback (called by AppShell). */
export function setConfirmKeyPin(fn: (workerId: string, expectedFingerprint: string, actualFingerprint: string) => Promise<KeyPinDecision>): void {
  confirmKeyPinFn = fn
}

/** Register the user ID getter callback (called by AppShell). */
export function setGetUserId(fn: () => string): void {
  getUserIdFn = fn
}

class BrowserChannelTransport implements ChannelTransport {
  async getWorkerPublicKey(workerId: string): Promise<WorkerKeyBundle> {
    const resp = await channelRpcClient.getWorkerPublicKey({ workerId })
    return {
      x25519PublicKey: resp.publicKey,
      mlkemPublicKey: resp.mlkemPublicKey,
      slhdsaPublicKey: resp.slhdsaPublicKey,
    }
  }

  async getWorkerEncryptionMode(workerId: string): Promise<EncryptionMode> {
    const resp = await channelRpcClient.getWorkerEncryptionMode({ workerId })
    return resp.encryptionMode
  }

  async openChannel(workerId: string, handshakePayload: Uint8Array): Promise<{ channelId: string, handshakePayload: Uint8Array }> {
    const resp = await channelRpcClient.openChannel({ workerId, handshakePayload })
    return { channelId: resp.channelId, handshakePayload: resp.handshakePayload }
  }

  async closeChannel(channelId: string): Promise<void> {
    await channelRpcClient.closeChannel({ channelId })
  }

  createWebSocket(): WebSocket {
    if (isWailsApp()) {
      return new WailsWebSocket() as unknown as WebSocket
    }

    const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const wsUrl = `${wsProtocol}//${window.location.host}/ws/channel`
    const protocols = ['channel-relay']
    if (!isSoloMode()) {
      const token = getToken()
      if (!token) {
        throw new Error('not authenticated')
      }
      protocols.push(`auth.token.${token}`)
    }
    const ws = new WebSocket(wsUrl, protocols)
    ws.binaryType = 'arraybuffer'
    return ws
  }

  async confirmKeyPin(workerId: string, expectedFingerprint: string, actualFingerprint: string): Promise<KeyPinDecision> {
    if (!confirmKeyPinFn) {
      return 'reject'
    }
    return confirmKeyPinFn(workerId, expectedFingerprint, actualFingerprint)
  }

  getUserId(): string {
    if (!getUserIdFn) {
      throw new Error('getUserId not registered')
    }
    return getUserIdFn()
  }
}

/**
 * WailsWebSocket provides a WebSocket-like interface that bridges through
 * Wails Go bindings and events. Binary data is base64-encoded at the
 * boundary.
 */
type WSEventType = 'open' | 'close' | 'message' | 'error'
interface WSListener { handler: EventListener, once: boolean }

class WailsWebSocket {
  readyState: number = WebSocket.CONNECTING
  binaryType: BinaryType = 'arraybuffer'

  onopen: ((ev: Event) => void) | null = null
  onmessage: ((ev: MessageEvent) => void) | null = null
  onclose: ((ev: CloseEvent) => void) | null = null
  onerror: ((ev: Event) => void) | null = null

  private listeners = new Map<WSEventType, WSListener[]>()
  private sendQueue: Promise<void> = Promise.resolve()

  constructor() {
    const token = isSoloMode() ? '' : (getToken() ?? '')
    window.go!.main.App.OpenChannelRelay(token).then(() => {
      // Listen for messages from Go.
      window.runtime!.EventsOn('channel:message', (...args: unknown[]) => {
        const b64 = args[0] as string
        const ev = { data: base64ToArrayBuffer(b64) } as MessageEvent
        this.onmessage?.(ev)
        this.dispatch('message', ev)
      })
      // Listen for relay close.
      window.runtime!.EventsOn('channel:close', () => {
        this.readyState = WebSocket.CLOSED
        const ev = { code: 1000, reason: '', wasClean: true } as CloseEvent
        this.onclose?.(ev)
        this.dispatch('close', ev)
      })
      this.readyState = WebSocket.OPEN
      const ev = {} as Event
      this.onopen?.(ev)
      this.dispatch('open', ev)
    }).catch((err: unknown) => {
      this.readyState = WebSocket.CLOSED
      const ev = new ErrorEvent('error', { message: String(err) })
      this.onerror?.(ev)
      this.dispatch('error', ev)
    })
  }

  addEventListener(type: string, listener: EventListener, opts?: { once?: boolean }): void {
    const t = type as WSEventType
    let list = this.listeners.get(t)
    if (!list) {
      list = []
      this.listeners.set(t, list)
    }
    list.push({ handler: listener, once: opts?.once ?? false })
  }

  removeEventListener(type: string, listener: EventListener): void {
    const list = this.listeners.get(type as WSEventType)
    if (!list)
      return
    const idx = list.findIndex(l => l.handler === listener)
    if (idx >= 0)
      list.splice(idx, 1)
  }

  private dispatch(type: WSEventType, ev: Event): void {
    const list = this.listeners.get(type)
    if (!list)
      return
    // Iterate a copy since once-listeners mutate the array.
    for (const entry of [...list]) {
      entry.handler(ev)
      if (entry.once)
        this.removeEventListener(type, entry.handler)
    }
  }

  send(data: ArrayBuffer | Uint8Array): void {
    // Serialize sends through a promise chain to preserve ordering.
    // Wails binding calls spawn concurrent Go goroutines, so without
    // serialization, messages can arrive at the Hub out of order,
    // which breaks the Noise protocol's sequential nonce counter.
    const b64 = arrayBufferToBase64(data)
    this.sendQueue = this.sendQueue.then(
      () => window.go!.main.App.SendChannelMessage(b64),
    ).catch(() => { /* send errors handled by channel manager */ })
  }

  close(): void {
    window.go!.main.App.CloseChannelRelay()
    this.readyState = WebSocket.CLOSED
    window.runtime!.EventsOff('channel:message')
    window.runtime!.EventsOff('channel:close')
    const ev = { code: 1000, reason: '', wasClean: true } as CloseEvent
    this.onclose?.(ev)
    this.dispatch('close', ev)
  }
}

export const channelManager = new ChannelManager(new BrowserChannelTransport())

// ---------------------------------------------------------------------------
// Generic helper
// ---------------------------------------------------------------------------

function callWorker<
  ReqSchema extends GenMessage<any>,
  RespSchema extends GenMessage<any>,
>(
  workerId: string,
  method: string,
  reqSchema: ReqSchema,
  respSchema: RespSchema,
  req: MessageInitShape<ReqSchema>,
): Promise<MessageShape<RespSchema>> {
  return channelManager.callWorker(workerId, method, reqSchema, respSchema, req)
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

export function moveTabWorkspace(workerId: string, req: MessageInitShape<typeof MoveTabWorkspaceRequestSchema>): Promise<MoveTabWorkspaceResponse> {
  return callWorker(workerId, 'MoveTabWorkspace', MoveTabWorkspaceRequestSchema, MoveTabWorkspaceResponseSchema, req)
}

// ---------------------------------------------------------------------------
// Agent
// ---------------------------------------------------------------------------

export function openAgent(workerId: string, req: MessageInitShape<typeof OpenAgentRequestSchema>): Promise<OpenAgentResponse> {
  return callWorker(workerId, 'OpenAgent', OpenAgentRequestSchema, OpenAgentResponseSchema, req)
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

export function listAgentMessages(workerId: string, req: MessageInitShape<typeof ListAgentMessagesRequestSchema>): Promise<ListAgentMessagesResponse> {
  return callWorker(workerId, 'ListAgentMessages', ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, req)
}

export function renameAgent(workerId: string, req: MessageInitShape<typeof RenameAgentRequestSchema>): Promise<RenameAgentResponse> {
  return callWorker(workerId, 'RenameAgent', RenameAgentRequestSchema, RenameAgentResponseSchema, req)
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

export function listAvailableProviders(workerId: string): Promise<ListAvailableProvidersResponse> {
  return callWorker(workerId, 'ListAvailableProviders', ListAvailableProvidersRequestSchema, ListAvailableProvidersResponseSchema, {})
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

export function sendInput(workerId: string, req: MessageInitShape<typeof SendInputRequestSchema>): Promise<SendInputResponse> {
  return callWorker(workerId, 'SendInput', SendInputRequestSchema, SendInputResponseSchema, req)
}

export function resizeTerminal(workerId: string, req: MessageInitShape<typeof ResizeTerminalRequestSchema>): Promise<ResizeTerminalResponse> {
  return callWorker(workerId, 'ResizeTerminal', ResizeTerminalRequestSchema, ResizeTerminalResponseSchema, req)
}

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

export function getGitInfo(workerId: string, req: MessageInitShape<typeof GetGitInfoRequestSchema>): Promise<GetGitInfoResponse> {
  return callWorker(workerId, 'GetGitInfo', GetGitInfoRequestSchema, GetGitInfoResponseSchema, req)
}

export function getGitFileStatus(workerId: string, req: MessageInitShape<typeof GetGitFileStatusRequestSchema>): Promise<GetGitFileStatusResponse> {
  return callWorker(workerId, 'GetGitFileStatus', GetGitFileStatusRequestSchema, GetGitFileStatusResponseSchema, req)
}

export function readGitFile(workerId: string, req: MessageInitShape<typeof ReadGitFileRequestSchema>): Promise<ReadGitFileResponse> {
  return callWorker(workerId, 'ReadGitFile', ReadGitFileRequestSchema, ReadGitFileResponseSchema, req)
}

export function checkWorktreeStatus(workerId: string, req: MessageInitShape<typeof CheckWorktreeStatusRequestSchema>): Promise<CheckWorktreeStatusResponse> {
  return callWorker(workerId, 'CheckWorktreeStatus', CheckWorktreeStatusRequestSchema, CheckWorktreeStatusResponseSchema, req)
}

export function inspectLastTabClose(workerId: string, req: MessageInitShape<typeof InspectLastTabCloseRequestSchema>): Promise<InspectLastTabCloseResponse> {
  return callWorker(workerId, 'InspectLastTabClose', InspectLastTabCloseRequestSchema, InspectLastTabCloseResponseSchema, req)
}

export function pushBranchForClose(workerId: string, req: MessageInitShape<typeof PushBranchForCloseRequestSchema>): Promise<PushBranchForCloseResponse> {
  return callWorker(workerId, 'PushBranchForClose', PushBranchForCloseRequestSchema, PushBranchForCloseResponseSchema, req)
}

export function scheduleWorktreeDeletion(workerId: string, req: MessageInitShape<typeof ScheduleWorktreeDeletionRequestSchema>): Promise<ScheduleWorktreeDeletionResponse> {
  return callWorker(workerId, 'ScheduleWorktreeDeletion', ScheduleWorktreeDeletionRequestSchema, ScheduleWorktreeDeletionResponseSchema, req)
}

export function forceRemoveWorktree(workerId: string, req: MessageInitShape<typeof ForceRemoveWorktreeRequestSchema>): Promise<ForceRemoveWorktreeResponse> {
  return callWorker(workerId, 'ForceRemoveWorktree', ForceRemoveWorktreeRequestSchema, ForceRemoveWorktreeResponseSchema, req)
}

export function keepWorktree(workerId: string, req: MessageInitShape<typeof KeepWorktreeRequestSchema>): Promise<KeepWorktreeResponse> {
  return callWorker(workerId, 'KeepWorktree', KeepWorktreeRequestSchema, KeepWorktreeResponseSchema, req)
}

export function listGitBranches(workerId: string, req: MessageInitShape<typeof ListGitBranchesRequestSchema>): Promise<ListGitBranchesResponse> {
  return callWorker(workerId, 'ListGitBranches', ListGitBranchesRequestSchema, ListGitBranchesResponseSchema, req)
}

export function listGitWorktrees(workerId: string, req: MessageInitShape<typeof ListGitWorktreesRequestSchema>): Promise<ListGitWorktreesResponse> {
  return callWorker(workerId, 'ListGitWorktrees', ListGitWorktreesRequestSchema, ListGitWorktreesResponseSchema, req)
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
   * Remove the stream listener from the channel, preventing the old
   * callback from processing events after a stream restart.
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

  let eventCb: ((resp: WatchEventsResponse) => void) | null = null
  let endCb: (() => void) | null = null
  let errorCb: ((err: Error) => void) | null = null

  streamHandle.onMessage((msg: InnerStreamMessage) => {
    if (eventCb) {
      try {
        const resp = fromBinary(WatchEventsResponseSchema, msg.payload)
        log.debug('WatchEvents stream message', { response: toJsonString(WatchEventsResponseSchema, resp) })
        eventCb(resp)
      }
      catch (err) {
        errorCb?.(err instanceof Error ? err : new Error(String(err)))
      }
    }
  })

  streamHandle.onEnd(() => {
    endCb?.()
  })

  streamHandle.onError((err: Error) => {
    errorCb?.(err)
  })

  return {
    onEvent: (cb) => { eventCb = cb },
    onEnd: (cb) => { endCb = cb },
    onError: (cb) => { errorCb = cb },
    close: () => { channelManager.removeStreamListener(channelId, streamHandle.requestId) },
  }
}
