import { Buffer } from 'node:buffer'
import { IncomingMessage, ServerResponse } from 'node:http'
import { Socket } from 'node:net'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { holdOpen, readJSONBody, readMockBody, writeMockJSON } from './mockHttp'
import { MAX_MOCK_REQUEST_BYTES } from './mockRequestLimits'

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

describe('holdOpen', () => {
  it('keeps the exact delay and removes disconnect listeners when it completes', async () => {
    vi.useFakeTimers()
    const message = request([])
    const response = new ServerResponse(message)
    const result = holdOpen(message, response, 30)
    expect(message.listenerCount('aborted')).toBe(1)
    await vi.advanceTimersByTimeAsync(29)
    expect(response.listenerCount('close')).toBe(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(await result).toBe(true)
    expect(message.listenerCount('aborted')).toBe(0)
    expect(response.listenerCount('close')).toBe(0)
    expect(vi.getTimerCount()).toBe(0)
  })

  it.each(['request', 'response'])('cancels its timer when the %s disconnects', async (source) => {
    vi.useFakeTimers()
    const message = request([])
    const response = new ServerResponse(message)
    const result = holdOpen(message, response, 1_000_000)
    if (source === 'request')
      message.emit('aborted')
    else
      response.emit('close')
    expect(await result).toBe(false)
    expect(message.listenerCount('aborted')).toBe(0)
    expect(response.listenerCount('close')).toBe(0)
    expect(vi.getTimerCount()).toBe(0)
  })
})
