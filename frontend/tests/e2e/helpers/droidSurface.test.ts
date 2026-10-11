import { IncomingMessage, ServerResponse } from 'node:http'
import { Socket } from 'node:net'
import { describe, expect, it, vi } from 'vitest'
import { handleDroidHttp } from './droidSurface'

function exchange(method: string, pathname: string, authorization?: string) {
  const request = new IncomingMessage(new Socket())
  request.method = method
  if (authorization !== undefined)
    request.headers.authorization = authorization
  const response = new ServerResponse(request)
  const end = vi.spyOn(response, 'end').mockImplementation(() => response)
  const owned = handleDroidHttp(request, response, new URL(pathname, 'http://mock.invalid'), { modelKey: 'isolated-key' })
  return { owned, response, end }
}

describe('handleDroidHttp', () => {
  it('answers the exact native route with the isolated bearer credential', () => {
    const result = exchange('GET', '/v1/api/cli/whoami', 'Bearer isolated-key')
    expect(result.owned).toBe(true)
    expect(result.response.statusCode).toBe(200)
    expect(result.response.getHeader('content-type')).toBe('application/json')
    expect(JSON.parse(String(result.end.mock.calls[0]?.[0]))).toEqual({ userId: 'leapmux-e2e-user', orgId: 'leapmux-e2e-org' })
  })

  it.each([undefined, '', 'Bearer other-key', 'isolated-key', 'Bearer isolated-key '])('refuses an absent or different credential: %j', (authorization) => {
    const result = exchange('GET', '/v1/api/cli/whoami', authorization)
    expect(result.owned).toBe(true)
    expect(result.response.statusCode).toBe(401)
    expect(JSON.parse(String(result.end.mock.calls[0]?.[0]))).toEqual({ error: { message: 'The identity request requires the isolated Droid model key.' } })
  })

  it.each([
    { method: 'POST', path: '/v1/api/cli/whoami' },
    { method: 'GET', path: '/v1/api/cli/whoami/' },
    { method: 'GET', path: '/api/cli/whoami' },
    { method: 'GET', path: '/user' },
  ])('leaves $method $path to another handler', ({ method, path }) => {
    const result = exchange(method, path, 'Bearer isolated-key')
    expect(result.owned).toBe(false)
    expect(result.end).not.toHaveBeenCalled()
    expect(result.response.headersSent).toBe(false)
  })
})
