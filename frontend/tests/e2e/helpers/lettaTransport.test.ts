import { Buffer } from 'node:buffer'
import { runInNewContext } from 'node:vm'
import { zstdCompressSync, zstdDecompressSync } from 'node:zlib'
import { describe, expect, it, vi } from 'vitest'
import { lettaTransportSource } from './lettaTransport'

function transport() {
  const nativeFetch = vi.fn<typeof fetch>(async () => new Response('ok'))
  const runtime = { fetch: nativeFetch as typeof fetch }
  runInNewContext(lettaTransportSource('http://127.0.0.1:4567'), {
    globalThis: runtime,
    require: (name: string) => {
      if (name !== 'node:zlib')
        throw new Error(`Unexpected preload dependency: ${name}`)
      return { zstdDecompressSync }
    },
    Request,
    URL,
    Headers,
    Buffer,
  })
  return { nativeFetch, redirectedFetch: runtime.fetch }
}

describe('lettaTransportSource', () => {
  it('redirects and decompresses native model bytes while preserving method, headers and cancellation', async () => {
    const { nativeFetch, redirectedFetch } = transport()
    const controller = new AbortController()
    const body = JSON.stringify({ model: 'gpt-5.5', input: 'quoted 한글 input' })
    const compressed = zstdCompressSync(Buffer.from(body))
    await redirectedFetch('https://chatgpt.com/backend-api/codex/responses?trace=1', {
      method: 'POST',
      headers: { 'content-encoding': 'zstd', 'content-length': String(compressed.byteLength), 'authorization': 'Bearer mock', 'chatgpt-account-id': 'account' },
      body: compressed,
      signal: controller.signal,
    })
    const [input, init] = nativeFetch.mock.calls[0]!
    const forwarded = new Request(input, init)
    expect(forwarded.url).toBe('http://127.0.0.1:4567/backend-api/codex/responses?trace=1')
    expect(forwarded.method).toBe('POST')
    expect(await forwarded.text()).toBe(body)
    expect(forwarded.headers.get('authorization')).toBe('Bearer mock')
    expect(forwarded.headers.get('chatgpt-account-id')).toBe('account')
    expect(forwarded.headers.has('content-encoding')).toBe(false)
    expect(forwarded.headers.has('content-length')).toBe(false)
    controller.abort()
    expect(forwarded.signal.aborted).toBe(true)
  })

  it('preserves a Request input and its uncompressed body', async () => {
    const { nativeFetch, redirectedFetch } = transport()
    await redirectedFetch(new Request('https://chatgpt.com/backend-api/codex/responses', { method: 'POST', body: 'native body', headers: { 'x-probe': 'kept' } }))
    const [input, init] = nativeFetch.mock.calls[0]!
    const forwarded = new Request(input, init)
    expect(forwarded.method).toBe('POST')
    expect(forwarded.headers.get('x-probe')).toBe('kept')
    expect(await forwarded.text()).toBe('native body')
  })

  it('redirects the native usage endpoint and leaves local assets and model calls alone', async () => {
    const { nativeFetch, redirectedFetch } = transport()
    await redirectedFetch('https://chatgpt.com/backend-api/wham/usage')
    expect((nativeFetch.mock.calls[0]![0] as Request).url).toBe('http://127.0.0.1:4567/backend-api/wham/usage')
    for (const input of ['data:text/plain,asset', 'file:///native/asset.wasm', 'http://127.0.0.1:4567/v1/responses']) {
      await redirectedFetch(input)
      expect(nativeFetch.mock.calls.at(-1)).toEqual([input, undefined])
    }
  })

  it('refuses another remote endpoint without calling the original fetch', async () => {
    const { nativeFetch, redirectedFetch } = transport()
    await expect(redirectedFetch('https://example.invalid/model')).rejects.toThrow('unexpected remote origin')
    expect(nativeFetch).not.toHaveBeenCalled()
  })

  it('does not forward an aborted or malformed compressed request', async () => {
    const { nativeFetch, redirectedFetch } = transport()
    const controller = new AbortController()
    controller.abort()
    await expect(redirectedFetch('https://chatgpt.com/backend-api/wham/usage', { signal: controller.signal })).rejects.toMatchObject({ name: 'AbortError' })
    await expect(redirectedFetch('https://chatgpt.com/backend-api/codex/responses', { method: 'POST', headers: { 'content-encoding': 'zstd' }, body: 'not compressed' })).rejects.toMatchObject({ code: 'ZSTD_error_prefix_unknown' })
    expect(nativeFetch).not.toHaveBeenCalled()
  })

  it('rejects a nonlocal destination before generating a preload', () => {
    expect(() => lettaTransportSource('https://example.invalid')).toThrow('loopback HTTP origin')
  })
})
