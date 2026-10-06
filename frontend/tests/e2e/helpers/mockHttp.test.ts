import { Buffer } from 'node:buffer'
import { IncomingMessage, ServerResponse } from 'node:http'
import { Socket } from 'node:net'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MAX_MOCK_REQUEST_BYTES, readJSONBody, readMockBody, waitUnlessDisconnected, writeMockJSON, writeResponseHeaders } from './mockHttp'

function request(chunks: readonly Buffer[]): IncomingMessage {
  const message = new IncomingMessage(new Socket())
  for (const chunk of chunks)
    message.push(chunk)
  message.push(null)
  return message
}

afterEach(() => vi.useRealTimers())

describe('readMockBody', () => {
  it('preserves binary bytes across chunks and accepts an empty body', async () => {
    const bytes = Buffer.from([0x00, 0xFF, 0xA0, 0xBF, 0xC0, 0xFE])
    expect(await readMockBody(request([bytes.subarray(0, 1), bytes.subarray(1, 4), bytes.subarray(4)]))).toEqual(bytes)
    expect(await readMockBody(request([]))).toEqual(Buffer.alloc(0))
  })

  it('accepts the exact byte limit and rejects the next byte', async () => {
    const bytes = Buffer.alloc(MAX_MOCK_REQUEST_BYTES)
    expect((await readMockBody(request([bytes]))).byteLength).toBe(MAX_MOCK_REQUEST_BYTES)
    await expect(readMockBody(request([bytes, Buffer.from([0])]))).rejects.toThrow(`The request body exceeds ${MAX_MOCK_REQUEST_BYTES} bytes.`)
  })

  it('preserves a transport read error', async () => {
    const message = new IncomingMessage(new Socket())
    const result = readMockBody(message)
    const failure = new Error('The binary request transport failed.')
    message.destroy(failure)
    await expect(result).rejects.toBe(failure)
  })
})

describe('readJSONBody', () => {
  it('decodes UTF-8 characters split across byte chunks without changing JSON values', async () => {
    const body = { text: '실제 내용 🧪', zero: 0, empty: '', absent: null, list: [false, 1] }
    const bytes = Buffer.from(JSON.stringify(body))
    const chunks = Array.from(bytes, byte => Buffer.from([byte]))
    expect(await readJSONBody(request(chunks))).toEqual(body)
  })

  it.each(['null', '0', 'false', '""', '[]'])('retains the JSON boundary %s', async (text) => {
    expect(await readJSONBody(request([Buffer.from(text)]))).toEqual(JSON.parse(text))
  })

  it('rejects an empty body with the existing error', async () => {
    await expect(readJSONBody(request([]))).rejects.toThrow('The request body is empty.')
  })

  it('retains the JSON parser error as the cause', async () => {
    const result = readJSONBody(request([Buffer.from('{broken')]))
    await expect(result).rejects.toThrow('The request body is not valid JSON.')
    await expect(result).rejects.toHaveProperty('cause', expect.any(SyntaxError))
  })

  it('accepts the exact byte limit and refuses the next byte', async () => {
    const exact = Buffer.alloc(MAX_MOCK_REQUEST_BYTES, 32)
    exact.write('0')
    expect(await readJSONBody(request([exact]))).toBe(0)
    await expect(readJSONBody(request([exact, Buffer.from(' ')]))).rejects.toThrow(`The request body exceeds ${MAX_MOCK_REQUEST_BYTES} bytes.`)
  })

  it('propagates a transport read failure', async () => {
    const message = new IncomingMessage(new Socket())
    const result = readJSONBody(message)
    const failure = new Error('Actual request transport failure.')
    message.destroy(failure)
    await expect(result).rejects.toBe(failure)
  })
})

describe('writeMockJSON', () => {
  it.each([null, 0, '', { text: '실제 내용 🧪', count: 0 }])('retains the body, status, and explicit response headers: %j', (body) => {
    const response = new ServerResponse(request([]))
    const end = vi.spyOn(response, 'end').mockImplementation(() => response)
    writeMockJSON(response, 409, body, { 'x-native-receipt': 'actual-value' })
    expect(response.statusCode).toBe(409)
    expect(response.getHeader('content-type')).toBe('application/json')
    expect(response.getHeader('x-native-receipt')).toBe('actual-value')
    expect(end).toHaveBeenCalledWith(JSON.stringify(body))
  })
})

describe('writeResponseHeaders', () => {
  it('keeps each delivered header readable through the response API, and skips an undefined one', () => {
    const response = new ServerResponse(request([]))
    writeResponseHeaders(response, 429, { 'retry-after': '7', 'x-native-limit': ['first', 'second'], 'x-absent': undefined })
    expect(response.statusCode).toBe(429)
    expect(response.getHeader('retry-after')).toBe('7')
    expect(response.getHeader('x-native-limit')).toEqual(['first', 'second'])
    expect(response.hasHeader('x-absent')).toBe(false)
    expect(response.headersSent).toBe(true)
  })
})

describe('waitUnlessDisconnected', () => {
  it('keeps the exact delay and removes disconnect listeners when it completes', async () => {
    vi.useFakeTimers()
    const message = request([])
    const response = new ServerResponse(message)
    const signal = new AbortController().signal
    const result = waitUnlessDisconnected(30, { request: message, response, signal })
    expect(message.listenerCount('aborted')).toBe(1)
    await vi.advanceTimersByTimeAsync(29)
    expect(response.listenerCount('close')).toBe(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(await result).toBe(true)
    expect(message.listenerCount('aborted')).toBe(0)
    expect(response.listenerCount('close')).toBe(0)
    expect(vi.getTimerCount()).toBe(0)
  })

  it.each(['request', 'response', 'signal'])('cancels its timer when the %s ends the exchange', async (source) => {
    vi.useFakeTimers()
    const message = request([])
    const response = new ServerResponse(message)
    const controller = new AbortController()
    const result = waitUnlessDisconnected(1_000_000, { request: message, response, signal: controller.signal })
    if (source === 'request')
      message.emit('aborted')
    else if (source === 'response')
      response.emit('close')
    else
      controller.abort()
    expect(await result).toBe(false)
    expect(message.listenerCount('aborted')).toBe(0)
    expect(response.listenerCount('close')).toBe(0)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('waits on the signal alone for a turn without an HTTP exchange', async () => {
    vi.useFakeTimers()
    const controller = new AbortController()
    const result = waitUnlessDisconnected(1_000, { signal: controller.signal })
    await vi.advanceTimersByTimeAsync(1_000)
    expect(await result).toBe(true)
  })

  it('returns false at once for an exchange that already ended, and starts no timer', async () => {
    vi.useFakeTimers()
    const controller = new AbortController()
    controller.abort()
    expect(await waitUnlessDisconnected(1_000, { signal: controller.signal })).toBe(false)
    const message = request([])
    const response = new ServerResponse(message)
    response.destroy()
    expect(await waitUnlessDisconnected(1_000, { request: message, response })).toBe(false)
    expect(vi.getTimerCount()).toBe(0)
  })

  it.each([0, -5])('returns true at once for the delay %s, and starts no timer', async (delayMs) => {
    vi.useFakeTimers()
    const message = request([])
    const response = new ServerResponse(message)
    expect(await waitUnlessDisconnected(delayMs, { request: message, response })).toBe(true)
    expect(vi.getTimerCount()).toBe(0)
    expect(response.listenerCount('close')).toBe(0)
  })
})
