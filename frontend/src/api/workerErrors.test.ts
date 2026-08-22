import { Code, ConnectError } from '@connectrpc/connect'
import { describe, expect, it } from 'vitest'
import { isDisconnectError, isWorkerUnreachable } from '~/api/workerErrors'
import { ChannelError, channelNotOpenError } from '~/lib/channelError'

// isWorkerUnreachable backs the tab-close fallback for unreachable
// workers (useTabOperations.handleTabClose). The CONNECT-CODE half of the
// contract MUST stay in lockstep with the CLI's `isWorkerUnreachable` in
// `backend/internal/cli/remote/cmd/preflight.go` — drift between the two
// means one transport closes orphan tabs and the other doesn't. The
// ChannelError half is browser-only (the CLI has no E2EE channel of its own
// on that path) and has no CLI counterpart.

describe('isworkerunreachable', () => {
  it('matches NotFound on its own, since only the hub produces it about a worker', () => {
    // The Hub is answering about THIS worker id and no transport fault yields
    // NotFound, so it needs no corroboration.
    expect(isWorkerUnreachable(new ConnectError('worker gone', Code.NotFound), undefined)).toBe(true)
  })

  it('trusts Unavailable only as the hub\'s tagged verdict, or with the worker list agreeing', () => {
    // Unavailable is the Hub's own "worker is offline" verdict AND the code an
    // edge 503, a proxy hiccup, or the Hub restarting wears. Only the first is a
    // statement about the worker, so the Hub tags it (workerUnreachableMetaKey in
    // channel_service.go) and an untagged one must be corroborated -- otherwise a
    // hub blip retires a tab whose worker is up and holding unsaved work.
    const tagged = new ConnectError('worker is offline', Code.Unavailable, {
      'leapmux-worker-unreachable': '1',
    })
    expect(isWorkerUnreachable(tagged, undefined)).toBe(true)

    const untagged = new ConnectError('502 from the edge', Code.Unavailable)
    expect(isWorkerUnreachable(untagged, undefined)).toBe(false)
    expect(isWorkerUnreachable(untagged, true)).toBe(false)
    // The worker list agreeing is the other way to reach a verdict.
    expect(isWorkerUnreachable(untagged, false)).toBe(true)
  })

  it('requires the offline reading for Unauthenticated and PermissionDenied', () => {
    // Neither says anything about the worker: the caller's own session or bearer
    // expired, or an ACL refused it. A re-login prompt must not commit a tombstone.
    for (const code of [Code.Unauthenticated, Code.PermissionDenied]) {
      const err = new ConnectError('nope', code)
      expect(isWorkerUnreachable(err, undefined), `code=${code} unknown`).toBe(false)
      expect(isWorkerUnreachable(err, true), `code=${code} online`).toBe(false)
      expect(isWorkerUnreachable(err, false), `code=${code} offline`).toBe(true)
    }
  })

  it('does not match transient/internal codes', () => {
    const codes: Code[] = [Code.Internal, Code.DeadlineExceeded, Code.Unknown, Code.ResourceExhausted, Code.Aborted]
    for (const code of codes) {
      const err = new ConnectError('boom', code)
      expect(isWorkerUnreachable(err, undefined), `code=${code}`).toBe(false)
    }
  })

  // A registered-but-OFFLINE worker never reaches a connect code on this path:
  // the hub tears down the channels its stream was carrying, and the in-flight
  // (or next) call rejects with a transport ChannelError instead. Before this
  // arm the close was refused with "Failed to prepare tab close" and the user
  // had no way to retire the tab while the machine was asleep.
  it('matches a transport ChannelError when the worker is known offline', () => {
    expect(isWorkerUnreachable(new ChannelError('transport', 'channel closed by server'), false)).toBe(true)
    expect(isWorkerUnreachable(new ChannelError('transport', 'channel disconnected'), false)).toBe(true)
  })

  // The transport source is far broader than "the worker is offline": it also
  // covers our own hub WebSocket dropping, a WS-open timeout, an E2EE rekey
  // timeout, the session key passing its hard ceiling, a malformed hub reply,
  // and any non-ChannelError thrown on the send path. The caller uses a `true`
  // here to SKIP the uncommitted-work dialog and retire the tab, so anything
  // short of a positive offline reading must answer false.
  it('does not match a transport ChannelError against a live or unknown worker', () => {
    const err = new ChannelError('transport', 'session key past hard ceiling')
    expect(isWorkerUnreachable(err, true)).toBe(false)
    expect(isWorkerUnreachable(err, undefined)).toBe(false)
    // The parameter is required now, so 'forgot to pass it' is a compile error
    // rather than a silent false. An explicit undefined still reads as unknown.
    expect(isWorkerUnreachable(err, undefined)).toBe(false)
  })

  it('does not match the non-transport ChannelError sources', () => {
    // `client` is the per-RPC timeout and the over-size payload: the channel is
    // healthy and the agent is very likely alive, so tombstoning would be worse
    // than a retry. `rpc` / `stream` mean the worker ANSWERED — the opposite of
    // unreachable.
    // Offline is passed deliberately: a false here then proves the SOURCE gate
    // rejected these, not the liveness gate.
    expect(isWorkerUnreachable(new ChannelError('client', 'RPC call \'X\' timed out after 10s'), false)).toBe(false)
    expect(isWorkerUnreachable(new ChannelError('rpc', 'agent not found', { code: 5 }), false)).toBe(false)
    expect(isWorkerUnreachable(new ChannelError('stream', 'boom', { code: 13 }), false)).toBe(false)
  })

  it('returns false for non-connect errors', () => {
    expect(isWorkerUnreachable(new Error('bare error'), undefined)).toBe(false)
    expect(isWorkerUnreachable('string', undefined)).toBe(false)
    expect(isWorkerUnreachable(null, undefined)).toBe(false)
    expect(isWorkerUnreachable(undefined, undefined)).toBe(false)
    expect(isWorkerUnreachable({ code: Code.NotFound }, undefined)).toBe(false)
  })
})

// isDisconnectError decides whether a BACKGROUND failure is worth a toast.
// A false negative costs one redundant toast; a false positive silences a real
// failure that nothing is retrying, so the predicate stays narrow.
describe('isdisconnecterror', () => {
  it('matches every transport ChannelError, whichever leg produced it', () => {
    expect(isDisconnectError(new ChannelError('transport', 'channel disconnected'))).toBe(true)
    expect(isDisconnectError(new ChannelError('transport', 'channel closed by server'))).toBe(true)
    expect(isDisconnectError(new ChannelError('transport', 'session key past hard ceiling', { disconnected: false }))).toBe(false)
    expect(isDisconnectError(new ChannelError('transport', 'open channel: hub returned an empty authenticated user id', { disconnected: false }))).toBe(false)
  })

  // The pair the user actually saw: one drop produced "channel disconnected"
  // from the drained stream and "channel not open" from the call that raced it,
  // and both were rendered as toasts.
  it('matches a channel that is not there, though its source is client', () => {
    expect(isDisconnectError(channelNotOpenError())).toBe(true)
  })

  it('leaves the rest of the client source alone, because the link was up', () => {
    expect(isDisconnectError(new ChannelError('client', 'message too large: 9 > 8'))).toBe(false)
    expect(isDisconnectError(new ChannelError('client', 'RPC call \'X\' timed out after 10s'))).toBe(false)
    expect(isDisconnectError(new ChannelError('client', 'RPC call \'X\' aborted'))).toBe(false)
  })

  // A worker that ANSWERED is the opposite of a dropped link, and its refusal is
  // exactly what the user needs to read.
  it('never matches an answer from the worker', () => {
    expect(isDisconnectError(new ChannelError('rpc', 'agent not found', { code: 5 }))).toBe(false)
    expect(isDisconnectError(new ChannelError('stream', 'boom', { code: 13 }))).toBe(false)
  })

  // The hub leg is Connect HTTP, and Unavailable is its "nothing answered".
  it('matches a tagged worker-unreachable Unavailable and no other connect code', () => {
    const tagged = new ConnectError('worker is offline', Code.Unavailable, {
      'leapmux-worker-unreachable': '1',
    })
    expect(isDisconnectError(tagged)).toBe(true)
    expect(isDisconnectError(new ConnectError('502 from the edge', Code.Unavailable))).toBe(false)
    expect(isDisconnectError(new ConnectError('worker gone', Code.NotFound))).toBe(false)
    expect(isDisconnectError(new ConnectError('log in again', Code.Unauthenticated))).toBe(false)
    expect(isDisconnectError(new ConnectError('not yours', Code.PermissionDenied))).toBe(false)
    expect(isDisconnectError(new ConnectError('slow', Code.DeadlineExceeded))).toBe(false)
  })

  it('returns false for anything that is neither error shape', () => {
    expect(isDisconnectError(new Error('bare error'))).toBe(false)
    expect(isDisconnectError('channel not open')).toBe(false)
    expect(isDisconnectError(null)).toBe(false)
    expect(isDisconnectError(undefined)).toBe(false)
  })
})
