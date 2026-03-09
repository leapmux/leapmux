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
  OpenAgentResponse,
  RenameAgentResponse,
  RetryAgentMessageResponse,
  SendAgentMessageResponse,
  SendControlResponseResponse,
  UpdateAgentSettingsResponse,
} from '~/generated/leapmux/v1/agent_pb'
import type { InnerStreamMessage } from '~/generated/leapmux/v1/channel_pb'
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
  KeepWorktreeResponse,
  ReadGitFileResponse,
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
  WatchEventsResponse,
} from '~/generated/leapmux/v1/workspace_pb'
import type { ChannelTransport, KeyPinDecision } from '~/lib/channel'
import { create, fromBinary, toBinary, toJsonString } from '@bufbuild/protobuf'
import { createClient } from '@connectrpc/connect'
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
  OpenAgentRequestSchema,
  OpenAgentResponseSchema,
  RenameAgentRequestSchema,
  RenameAgentResponseSchema,
  RetryAgentMessageRequestSchema,
  RetryAgentMessageResponseSchema,
  SendAgentMessageRequestSchema,
  SendAgentMessageResponseSchema,
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
  KeepWorktreeRequestSchema,
  KeepWorktreeResponseSchema,
  ReadGitFileRequestSchema,
  ReadGitFileResponseSchema,
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
  WatchEventsRequestSchema,
  WatchEventsResponseSchema,
} from '~/generated/leapmux/v1/workspace_pb'
import { ChannelManager } from '~/lib/channel'
import { createLogger } from '~/lib/logger'

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
  async getWorkerPublicKey(workerId: string): Promise<Uint8Array> {
    const resp = await channelRpcClient.getWorkerPublicKey({ workerId })
    return resp.publicKey
  }

  async openChannel(workerId: string, handshakePayload: Uint8Array): Promise<{ channelId: string, handshakePayload: Uint8Array }> {
    const resp = await channelRpcClient.openChannel({ workerId, handshakePayload })
    return { channelId: resp.channelId, handshakePayload: resp.handshakePayload }
  }

  async closeChannel(channelId: string): Promise<void> {
    await channelRpcClient.closeChannel({ channelId })
  }

  createWebSocket(): WebSocket {
    const token = getToken()
    if (!token) {
      throw new Error('not authenticated')
    }
    const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const wsUrl = `${wsProtocol}//${window.location.host}/ws/channel?token=${encodeURIComponent(token)}`
    const ws = new WebSocket(wsUrl, ['channel-relay'])
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

export function retryAgentMessage(workerId: string, req: MessageInitShape<typeof RetryAgentMessageRequestSchema>): Promise<RetryAgentMessageResponse> {
  return callWorker(workerId, 'RetryAgentMessage', RetryAgentMessageRequestSchema, RetryAgentMessageResponseSchema, req)
}

export function deleteAgentMessage(workerId: string, req: MessageInitShape<typeof DeleteAgentMessageRequestSchema>): Promise<DeleteAgentMessageResponse> {
  return callWorker(workerId, 'DeleteAgentMessage', DeleteAgentMessageRequestSchema, DeleteAgentMessageResponseSchema, req)
}

export function updateAgentSettings(workerId: string, req: MessageInitShape<typeof UpdateAgentSettingsRequestSchema>): Promise<UpdateAgentSettingsResponse> {
  return callWorker(workerId, 'UpdateAgentSettings', UpdateAgentSettingsRequestSchema, UpdateAgentSettingsResponseSchema, req)
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

export function forceRemoveWorktree(workerId: string, req: MessageInitShape<typeof ForceRemoveWorktreeRequestSchema>): Promise<ForceRemoveWorktreeResponse> {
  return callWorker(workerId, 'ForceRemoveWorktree', ForceRemoveWorktreeRequestSchema, ForceRemoveWorktreeResponseSchema, req)
}

export function keepWorktree(workerId: string, req: MessageInitShape<typeof KeepWorktreeRequestSchema>): Promise<KeepWorktreeResponse> {
  return callWorker(workerId, 'KeepWorktree', KeepWorktreeRequestSchema, KeepWorktreeResponseSchema, req)
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
