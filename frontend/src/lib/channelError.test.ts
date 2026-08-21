import { describe, expect, it } from 'vitest'
import { abortError, ChannelError, channelNotOpenError } from './channelError'

describe('channelError disconnected marker', () => {
  // The default carries the whole `transport` arm of isDisconnectError, so no
  // relay/session/open site has to remember to set it.
  it('marks every transport failure as a dropped link', () => {
    expect(new ChannelError('transport', 'channel disconnected').disconnected).toBe(true)
    expect(new ChannelError('transport', 'WebSocket open timed out after 10s').disconnected).toBe(true)
  })

  it('leaves the other sources unmarked, because the link was healthy', () => {
    expect(new ChannelError('client', 'message too large: 9 > 8').disconnected).toBe(false)
    expect(new ChannelError('rpc', 'agent not found').disconnected).toBe(false)
    expect(new ChannelError('stream', 'boom').disconnected).toBe(false)
  })

  it('lets a caller override the default in either direction', () => {
    expect(new ChannelError('client', 'gone', { disconnected: true }).disconnected).toBe(true)
    expect(new ChannelError('transport', 'odd', { disconnected: false }).disconnected).toBe(false)
  })

  it('defaults code to zero and fatal to false', () => {
    const err = new ChannelError('transport', 'lost')
    expect(err.code).toBe(0)
    expect(err.fatal).toBe(false)
  })

  it('carries the code and the fatal marker a caller passes', () => {
    const err = new ChannelError('rpc', 'unavailable', { code: 14, fatal: true })
    expect(err.code).toBe(14)
    expect(err.fatal).toBe(true)
  })
})

describe('channelNotOpenError', () => {
  // The point of the factory: this one client-side failure reports connection
  // state, so a background caller must be able to recognise it as a dropped link
  // without matching on the message text.
  it('reports a dropped link', () => {
    expect(channelNotOpenError().disconnected).toBe(true)
  })

  // Deliberately NOT 'transport'. isWorkerUnreachable retires a tab on a
  // transport failure plus a positive offline reading, and a missing channel is
  // no evidence at all that the worker went away.
  it('stays a client-source error, so it cannot retire a tab', () => {
    const err = channelNotOpenError()
    expect(err.source).toBe('client')
    expect(err.fatal).toBe(false)
    expect(err.message).toBe('channel not open')
  })
})

describe('abortError', () => {
  it('passes through an Error reason the aborter supplied', () => {
    const reason = new Error('the tab went away')
    const signal = AbortSignal.abort(reason)
    expect(abortError(signal, 'ListAgentMessages')).toBe(reason)
  })

  // A bare abort() gives a DOMException-shaped reason in a browser and a plain
  // string in some hosts, so the fallback names the method that was dropped.
  it('names the method when the reason is not an Error', () => {
    const signal = AbortSignal.abort('cancelled')
    const err = abortError(signal, 'ListAgentMessages')
    expect(err.message).toContain('ListAgentMessages')
    // An abort is the caller's own doing, not a lost link: announcing it as a
    // disconnect would suppress a toast the user is waiting for.
    expect(err).toBeInstanceOf(ChannelError)
    expect((err as ChannelError).disconnected).toBe(false)
  })
})
