import { describe, expect, it } from 'vitest'
import { ChannelError } from './channelError'
import { channelErrorOf, sessionErrorOf } from './channelSessionProtocol'

describe('sessionErrorOf', () => {
  it('keeps every field of a ChannelError', () => {
    const err = new ChannelError('rpc', 'queue full', { code: 3, fatal: true, disconnected: true })

    expect(sessionErrorOf(err)).toEqual({
      source: 'rpc',
      message: 'queue full',
      code: 3,
      fatal: true,
      disconnected: true,
    })
  })

  it('turns a plain Error into a transport error of a dropped link', () => {
    expect(sessionErrorOf(new Error('noise-hybrid: SLH-DSA signature verification failed'))).toEqual({
      source: 'transport',
      message: 'noise-hybrid: SLH-DSA signature verification failed',
      code: 0,
      fatal: false,
      disconnected: true,
    })
  })

  it('turns a thrown string into a transport error that carries the string', () => {
    expect(sessionErrorOf('boom')).toEqual({
      source: 'transport',
      message: 'boom',
      code: 0,
      fatal: false,
      disconnected: true,
    })
  })

  it('survives a structured clone unchanged', () => {
    const error = sessionErrorOf(new ChannelError('client', 'too large', { code: 9 }))

    expect(structuredClone(error)).toEqual(error)
  })
})

describe('channelErrorOf', () => {
  it('builds again each field of the ChannelError that sessionErrorOf carried', () => {
    const original = new ChannelError('stream', 'stream reset', { code: 7, fatal: true, disconnected: false })

    const rebuilt = channelErrorOf(structuredClone(sessionErrorOf(original)))

    expect(rebuilt).toBeInstanceOf(ChannelError)
    expect(rebuilt.source).toBe('stream')
    expect(rebuilt.message).toBe('stream reset')
    expect(rebuilt.code).toBe(7)
    expect(rebuilt.fatal).toBe(true)
    expect(rebuilt.disconnected).toBe(false)
  })
})
