import type { TunnelConfig } from './platformBridge'
import type { WorkerTrustScope } from '~/context/AuthContext'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { create, toBinary } from '@bufbuild/protobuf'
import { clearMocks, mockIPC, mockWindows } from '@tauri-apps/api/mocks'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { BrowserAuthKind, HeaderValuesSchema, ProxyHttpResponseSchema, RelayCloseSchema, RelayReadResponseSchema } from '~/generated/proto/leapmux/desktop/v1/frame_pb'
import { storageAccountFor, TEST_HUB } from '~/lib/browserStorage'
import { deferred, settle } from '~/test-support/async'
import { decodeProxyResponse as decodeProxyResponseEnvelope, desktopFetch, desktopHub, getCapabilities, observeWindowMode, openExternalUrl, parseDesktopBehaviorRefusals, parseLaunchVisibility, parseRelayClosePayload, platformBridge, readClipboardImage, resetPlatformRuntimeState, restoreWindowGeometry, windowExitFullscreen } from './platformBridge'
import { TauriRelayWebSocket } from './tauriRelaySocket'

// isMac() in ~/lib/shortcuts/platform caches the UA-detected platform on
// first call, so flipping navigator.userAgent between tests doesn't actually
// flip its return value. Mock it directly so each test controls the gate.
const isMacMock = vi.hoisted(() => vi.fn<() => boolean>())
vi.mock('~/lib/shortcuts/platform', () => ({
  isMac: isMacMock,
  getPlatform: () => (isMacMock()
    ? 'mac'
    : 'linux'),
}))

// Only `openExternalUrl` reaches a toast from this module, and its contract is
// the MESSAGE it reports -- not oat's DOM, which jsdom has no host for.
const showWarnToastWithLoggedCause = vi.hoisted(() => vi.fn())
vi.mock('~/components/common/Toast', () => ({ showWarnToastWithLoggedCause }))

function encodeProxyResponseEnvelope(response: {
  status: number
  headers: Record<string, string[]>
  body: ArrayBuffer
}): ArrayBuffer {
  const bytes = toBinary(ProxyHttpResponseSchema, create(ProxyHttpResponseSchema, {
    status: response.status,
    headers: Object.fromEntries(
      Object.entries(response.headers).map(([name, values]) => [
        name,
        create(HeaderValuesSchema, { values }),
      ]),
    ),
    body: new Uint8Array(response.body),
  }))
  return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer
}

function encodeRelayRead(
  cursor: bigint,
  item: { data: Uint8Array } | { close: { code: number, reason: string, wasClean: boolean } },
): ArrayBuffer {
  const value = 'data' in item
    ? { case: 'data' as const, value: item.data }
    : {
        case: 'close' as const,
        value: create(RelayCloseSchema, item.close),
      }
  const bytes = toBinary(RelayReadResponseSchema, create(RelayReadResponseSchema, {
    cursor,
    item: value,
  }))
  return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer
}

describe('desktopFetch', () => {
  beforeEach(() => {
    (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__ = {}
  })

  afterEach(() => {
    clearMocks()
    delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__
  })

  it('preserves repeated response header values', async () => {
    mockIPC((cmd) => {
      if (cmd === 'proxy_http') {
        return encodeProxyResponseEnvelope({
          status: 200,
          headers: {
            'x-repeated': ['one', 'two'],
          },
          body: new ArrayBuffer(0),
        })
      }
      throw new Error(`unmocked Tauri command: ${cmd}`)
    })

    const response = await desktopFetch('https://hub.example/test')
    expect(response.headers.get('x-repeated')).toBe('one, two')
  })

  it('accepts raw response bytes serialized by Tauri fallback IPC', async () => {
    const wire = encodeProxyResponseEnvelope({
      status: 200,
      headers: {
        'content-type': [
          'text/plain',
        ],
      },
      body: new TextEncoder().encode('fallback').buffer,
    })
    mockIPC((cmd) => {
      if (cmd === 'proxy_http')
        return Array.from(new Uint8Array(wire))
      throw new Error(`unmocked Tauri command: ${cmd}`)
    })

    const response = await desktopFetch('https://hub.example/test')

    expect(await response.text()).toBe('fallback')
  })

  it('constructs a bodyless response for status 204', async () => {
    mockIPC((cmd) => {
      if (cmd === 'proxy_http') {
        return encodeProxyResponseEnvelope({
          status: 204,
          headers: {},
          body: new ArrayBuffer(0),
        })
      }
      throw new Error(`unmocked Tauri command: ${cmd}`)
    })

    const response = await desktopFetch('https://hub.example/no-content')

    expect(response.status).toBe(204)
    expect(await response.text()).toBe('')
  })

  it('rejects with AbortError when the signal aborts while the call is in flight', async () => {
    const started = deferred<void>()
    const cancelled = deferred<void>()
    const reply = deferred<ArrayBuffer>()
    let proxyRequestId = ''
    let cancelledRequestId = ''
    ;(window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__ = {
      invoke: (cmd: string, args?: unknown, options?: { headers?: Record<string, string> }) => {
        if (cmd === 'proxy_http') {
          proxyRequestId = options?.headers?.['request-id'] ?? ''
          started.resolve()
          return reply.promise
        }
        if (cmd === 'cancel_proxy_http') {
          cancelledRequestId = (args as { requestId: string }).requestId
          cancelled.resolve()
          return Promise.resolve(null)
        }
        throw new Error(`unmocked Tauri command: ${cmd}`)
      },
    }

    const controller = new AbortController()
    const call = desktopFetch('https://hub.example/test', { signal: controller.signal })
    await started.promise
    const rejected = vi.fn()
    const observed = call.catch(rejected)
    controller.abort()
    try {
      await cancelled.promise
      // Drain the queued promise callbacks while the native reply is still held.
      await settle()
      expect(rejected).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ name: 'AbortError' }))
      expect(proxyRequestId).not.toBe('')
      expect(cancelledRequestId).toBe(proxyRequestId)
    }
    finally {
      reply.resolve(encodeProxyResponseEnvelope({
        status: 200,
        headers: {},
        body: new ArrayBuffer(0),
      }))
      await observed
    }
  })

  it('rejects with AbortError when the signal is already aborted', async () => {
    const invoked: string[] = []
    mockIPC((cmd) => {
      invoked.push(cmd)
      return encodeProxyResponseEnvelope({
        status: 200,
        headers: {},
        body: new ArrayBuffer(0),
      })
    })

    const controller = new AbortController()
    controller.abort()
    await expect(desktopFetch('https://hub.example/test', { signal: controller.signal }))
      .rejects
      .toMatchObject({ name: 'AbortError' })
    expect(invoked).toEqual([])
  })

  it('removes the abort listener once the call settles', async () => {
    mockIPC((cmd) => {
      if (cmd === 'proxy_http') {
        return encodeProxyResponseEnvelope({
          status: 200,
          headers: {},
          body: new ArrayBuffer(0),
        })
      }
      throw new Error(`unmocked Tauri command: ${cmd}`)
    })

    const controller = new AbortController()
    const removed = vi.spyOn(controller.signal, 'removeEventListener')
    await desktopFetch('https://hub.example/test', { signal: controller.signal })
    expect(removed).toHaveBeenCalledWith('abort', expect.any(Function))
  })

  it('sends the request body raw and the request metadata in invoke headers', async () => {
    // mockIPC drops the InvokeOptions, and the metadata rides there -- hand-roll
    // the internals invoke so the headers are observable.
    const calls: Array<{ cmd: string, args: unknown, options?: { headers?: Record<string, string> } | undefined }> = []
    ;(window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__ = {
      invoke: (cmd: string, args?: unknown, options?: { headers?: Record<string, string> }) => {
        calls.push({ cmd, args, options })
        return Promise.resolve(encodeProxyResponseEnvelope({
          status: 200,
          headers: {},
          body: new ArrayBuffer(0),
        }))
      },
    }

    const body = new Uint8Array([1, 2, 3])
    await desktopFetch('https://hub.example/rpc/Method?x=1', {
      method: 'POST',
      headers: { 'content-type': 'application/grpc+proto' },
      body,
    })

    expect(calls).toHaveLength(1)
    expect(calls[0]!.cmd).toBe('proxy_http')
    // The body crosses as the raw invoke body: no base64 anywhere.
    expect(calls[0]!.args).toBe(body)
    const headers = calls[0]!.options?.headers ?? {}
    expect(headers.method).toBe('POST')
    expect(headers.path).toBe('/rpc/Method?x=1')
    expect(JSON.parse(atob(headers['headers-b64']!))).toEqual({
      'content-type': 'application/grpc+proto',
    })
  })
})

describe('ProxyHttpResponse wire data', () => {
  it('round-trips meta and a multi-KB binary body through encode and decode', () => {
    const body = new Uint8Array(4096)
    for (let i = 0; i < body.length; i++)
      body[i] = (i * 7) % 256
    const resp = {
      status: 201,
      headers: {
        'x-repeated': ['one', 'two'],
        'content-type': ['application/grpc+proto'],
      },
      body: body.buffer as ArrayBuffer,
    }

    const decoded = decodeProxyResponseEnvelope(encodeProxyResponseEnvelope(resp))

    expect(decoded.status).toBe(201)
    expect(decoded.headers).toEqual(resp.headers)
    // Array.from: jsdom typed arrays of another realm defeat toEqual.
    expect(Array.from(new Uint8Array(decoded.body))).toEqual(Array.from(body))
  })

  it('round-trips an empty body', () => {
    const decoded = decodeProxyResponseEnvelope(encodeProxyResponseEnvelope({
      status: 204,
      headers: {},
      body: new ArrayBuffer(0),
    }))
    expect(decoded.status).toBe(204)
    expect(decoded.headers).toEqual({})
    expect(decoded.body.byteLength).toBe(0)
  })

  it('rejects a truncated envelope instead of slicing past the buffer', () => {
    const encoded = encodeProxyResponseEnvelope({
      status: 200,
      headers: {},
      body: new ArrayBuffer(0),
    })
    expect(() => decodeProxyResponseEnvelope(encoded.slice(0, 2))).toThrow(/premature EOF/)
    expect(() => decodeProxyResponseEnvelope(new Uint8Array([0x1A, 0x05, 0x01]))).toThrow(/premature EOF/)
  })
})

describe('readClipboardImage', () => {
  let originalGetContext: typeof HTMLCanvasElement.prototype.getContext
  let originalToBlob: typeof HTMLCanvasElement.prototype.toBlob
  let originalImageData: typeof globalThis.ImageData | undefined

  beforeEach(() => {
    delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__

    // jsdom doesn't ship ImageData / a 2d context / toBlob — stub the shapes
    // readClipboardImage relies on. The fake context's putImageData is a no-op
    // and toBlob invents a Blob with the requested MIME so the File the
    // function returns has the right type.
    originalGetContext = HTMLCanvasElement.prototype.getContext
    originalToBlob = HTMLCanvasElement.prototype.toBlob
    originalImageData = (globalThis as { ImageData?: typeof globalThis.ImageData }).ImageData

    HTMLCanvasElement.prototype.getContext = vi.fn(() => ({
      putImageData: vi.fn(),
    })) as unknown as typeof HTMLCanvasElement.prototype.getContext
    HTMLCanvasElement.prototype.toBlob = function (cb: BlobCallback, type?: string) {
      cb(new Blob([
        new Uint8Array([0x89, 0x50, 0x4E, 0x47]),
      ], { type: type ?? 'image/png' }))
    } as typeof HTMLCanvasElement.prototype.toBlob
    ;(globalThis as { ImageData: unknown }).ImageData = class {
      constructor(public data: Uint8ClampedArray, public width: number, public height: number) {}
    }
  })

  afterEach(() => {
    clearMocks()
    delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__
    HTMLCanvasElement.prototype.getContext = originalGetContext
    HTMLCanvasElement.prototype.toBlob = originalToBlob
    if (originalImageData)
      (globalThis as { ImageData: unknown }).ImageData = originalImageData
    else
      delete (globalThis as { ImageData?: unknown }).ImageData
  })

  it('returns null when not running in Tauri', async () => {
    expect(await readClipboardImage()).toBeNull()
  })

  it('returns a PNG File when the Tauri clipboard holds an image', async () => {
    mockIPC((cmd) => {
      if (cmd === 'plugin:clipboard-manager|read_image')
        return 1 // resource id
      if (cmd === 'plugin:image|rgba')
        return new Uint8Array([255, 0, 0, 255, 0, 255, 0, 255]) // 2 RGBA pixels
      if (cmd === 'plugin:image|size')
        return { width: 2, height: 1 }
      throw new Error(`unmocked Tauri command: ${cmd}`)
    })

    const file = await readClipboardImage()
    expect(file).not.toBeNull()
    expect(file?.type).toBe('image/png')
    expect(file?.size).toBeGreaterThan(0)
  })

  it('returns null on a zero-sized image', async () => {
    mockIPC((cmd) => {
      if (cmd === 'plugin:clipboard-manager|read_image')
        return 1
      if (cmd === 'plugin:image|rgba')
        return new Uint8Array()
      if (cmd === 'plugin:image|size')
        return { width: 0, height: 0 }
      throw new Error(`unmocked Tauri command: ${cmd}`)
    })

    expect(await readClipboardImage()).toBeNull()
  })

  it('returns null when the plugin throws', async () => {
    mockIPC(() => {
      throw new Error('clipboard read failed')
    })

    expect(await readClipboardImage()).toBeNull()
  })

  it('releases the Image resource after a successful read', async () => {
    const calls: string[] = []
    mockIPC((cmd) => {
      calls.push(cmd)
      if (cmd === 'plugin:clipboard-manager|read_image')
        return 1
      if (cmd === 'plugin:image|rgba')
        return new Uint8Array([255, 0, 0, 255])
      if (cmd === 'plugin:image|size')
        return { width: 1, height: 1 }
      if (cmd === 'plugin:resources|close')
        return undefined
      throw new Error(`unmocked Tauri command: ${cmd}`)
    })

    await readClipboardImage()
    expect(calls).toContain('plugin:resources|close')
  })

  it('releases the Image resource when decoding fails after acquisition', async () => {
    const calls: string[] = []
    mockIPC((cmd) => {
      calls.push(cmd)
      if (cmd === 'plugin:clipboard-manager|read_image')
        return 1
      if (cmd === 'plugin:image|rgba')
        throw new Error('rgba failed')
      if (cmd === 'plugin:resources|close')
        return undefined
      throw new Error(`unmocked Tauri command: ${cmd}`)
    })

    expect(await readClipboardImage()).toBeNull()
    expect(calls).toContain('plugin:resources|close')
  })
})

describe('cliPathStatus', () => {
  beforeEach(() => {
    (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__ = {}
    isMacMock.mockReturnValue(true)
  })

  afterEach(() => {
    clearMocks()
    delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__
    isMacMock.mockReset()
  })

  it('returns null when not running in Tauri', async () => {
    delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__
    expect(await platformBridge.cliPathStatus()).toBeNull()
  })

  it('returns null when running on a non-macOS platform', async () => {
    isMacMock.mockReturnValue(false)
    expect(await platformBridge.cliPathStatus()).toBeNull()
  })

  it('decodes STATE_OK (1) into { state: "ok", bundled }', async () => {
    mockIPC((cmd) => {
      if (cmd === 'cli_path_status') {
        return {
          state: 1,
          bundled: '/Applications/X.app/Contents/MacOS/leapmux',
          resolved: '',
          target: '',
          targetKind: 2,
        }
      }
      throw new Error(`unmocked: ${cmd}`)
    })
    const got = await platformBridge.cliPathStatus()
    expect(got).toEqual({ state: 'ok', bundled: '/Applications/X.app/Contents/MacOS/leapmux' })
  })

  it('decodes STATE_MISSING (2) and surfaces install target + targetKind', async () => {
    mockIPC((cmd) => {
      if (cmd === 'cli_path_status') {
        return {
          state: 2,
          bundled: '/b/leapmux',
          resolved: '',
          target: '/usr/local/bin/leapmux',
          targetKind: 1,
        }
      }
      throw new Error(`unmocked: ${cmd}`)
    })
    const got = await platformBridge.cliPathStatus()
    expect(got).toEqual({
      state: 'missing',
      bundled: '/b/leapmux',
      target: '/usr/local/bin/leapmux',
      targetKind: 'absent',
    })
  })

  it('decodes STATE_MISMATCH (3) and surfaces resolved + targetKind', async () => {
    mockIPC((cmd) => {
      if (cmd === 'cli_path_status') {
        return {
          state: 3,
          bundled: '/b/leapmux',
          resolved: '/opt/homebrew/bin/leapmux',
          target: '',
          targetKind: 3,
        }
      }
      throw new Error(`unmocked: ${cmd}`)
    })
    const got = await platformBridge.cliPathStatus()
    expect(got).toEqual({
      state: 'mismatch',
      bundled: '/b/leapmux',
      resolved: '/opt/homebrew/bin/leapmux',
      targetKind: 'regular_file',
    })
  })

  it('decodes each targetKind variant', async () => {
    const cases = [
      { code: 1, decoded: 'absent' },
      { code: 2, decoded: 'symlink' },
      { code: 3, decoded: 'regular_file' },
      { code: 0, decoded: 'unknown' },
      { code: 99, decoded: 'unknown' },
    ]
    for (const c of cases) {
      mockIPC((cmd) => {
        if (cmd === 'cli_path_status') {
          return {
            state: 2,
            bundled: '/b/leapmux',
            resolved: '',
            target: '/usr/local/bin/leapmux',
            targetKind: c.code,
          }
        }
        throw new Error(`unmocked: ${cmd}`)
      })
      const got = await platformBridge.cliPathStatus()
      expect(got).toMatchObject({ state: 'missing', targetKind: c.decoded })
    }
  })

  it('decodes STATE_UNAVAILABLE (4) and an unknown numeric value into "unavailable"', async () => {
    mockIPC((cmd) => {
      if (cmd === 'cli_path_status') {
        return {
          state: 4,
          bundled: '',
          resolved: '',
          target: '',
          targetKind: 0,
        }
      }
      throw new Error(`unmocked: ${cmd}`)
    })
    expect(await platformBridge.cliPathStatus()).toEqual({ state: 'unavailable' })

    mockIPC((cmd) => {
      if (cmd === 'cli_path_status') {
        return {
          state: 99,
          bundled: '',
          resolved: '',
          target: '',
          targetKind: 0,
        }
      }
      throw new Error(`unmocked: ${cmd}`)
    })
    expect(await platformBridge.cliPathStatus()).toEqual({ state: 'unavailable' })
  })
})

describe('cliInstallSymlink', () => {
  beforeEach(() => {
    (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__ = {}
  })

  afterEach(() => {
    clearMocks()
    delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__
  })

  it('decodes each tagged result variant', async () => {
    const cases = [
      {
        in: {
          result: 1,
          command: '',
          path: '',
          message: '',
        },
        out: { result: 'ok' },
      },
      {
        in: {
          result: 2,
          command: 'sudo ln -sf a b',
          path: '',
          message: '',
        },
        out: { result: 'needs_sudo', command: 'sudo ln -sf a b' },
      },
      {
        in: {
          result: 3,
          command: '',
          path: '/usr/local/bin/leapmux',
          message: '',
        },
        out: { result: 'already_exists_real_file', path: '/usr/local/bin/leapmux' },
      },
      {
        in: {
          result: 4,
          command: 'sudo mkdir -p /usr/local/bin && ...',
          path: '/usr/local/bin',
          message: '',
        },
        out: { result: 'parent_missing', path: '/usr/local/bin', command: 'sudo mkdir -p /usr/local/bin && ...' },
      },
      {
        in: {
          result: 5,
          command: '',
          path: '',
          message: 'disk full',
        },
        out: { result: 'io_error', message: 'disk full' },
      },
    ]
    for (const c of cases) {
      mockIPC((cmd) => {
        if (cmd === 'cli_install_symlink')
          return c.in
        throw new Error(`unmocked: ${cmd}`)
      })
      expect(await platformBridge.cliInstallSymlink()).toEqual(c.out)
    }
  })

  it('forwards the force flag to the Rust forwarder', async () => {
    const received: { force: boolean }[] = []
    mockIPC((cmd, args) => {
      if (cmd === 'cli_install_symlink') {
        received.push(args as { force: boolean })
        return {
          result: 1,
          command: '',
          path: '',
          message: '',
        }
      }
      throw new Error(`unmocked: ${cmd}`)
    })

    await platformBridge.cliInstallSymlink()
    await platformBridge.cliInstallSymlink(true)
    await platformBridge.cliInstallSymlink(false)
    expect(received).toEqual([
      { force: false },
      { force: true },
      { force: false },
    ])
  })

  it('falls back to an io_error for unknown result codes', async () => {
    mockIPC((cmd) => {
      if (cmd === 'cli_install_symlink') {
        return {
          result: 0,
          command: '',
          path: '',
          message: '',
        }
      }
      throw new Error(`unmocked: ${cmd}`)
    })
    expect(await platformBridge.cliInstallSymlink()).toEqual({
      result: 'io_error',
      message: 'unknown sidecar response',
    })
  })
})

describe('platformBridge tunnel operations', () => {
  const config: TunnelConfig = {
    workerId: 'worker',
    type: 'port_forward',
    targetAddr: '127.0.0.1',
    targetPort: 3000,
    bindAddr: '127.0.0.1',
    bindPort: 3010,
  }
  const info = { ...config, id: 'tunnel' }
  const list = {
    tunnels: [info],
    channelEpoch: 'outgoing-epoch',
  }
  let dispose: () => void
  let scope: WorkerTrustScope
  let account: AbortController

  function adopt(userId: string): WorkerTrustScope {
    account.abort()
    account = new AbortController()
    scope = {
      userId,
      storageAccount: storageAccountFor(TEST_HUB, userId),
      signal: account.signal,
      generation: scope.generation + 1,
    }
    return scope
  }

  beforeEach(() => {
    (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__ = {}
    account = new AbortController()
    scope = {
      userId: 'outgoing',
      storageAccount: storageAccountFor(TEST_HUB, 'outgoing'),
      signal: account.signal,
      generation: 1,
    }
    dispose = platformBridge.setTunnelTrustScope(() => scope)
  })

  afterEach(() => {
    dispose()
    clearMocks()
    delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__
  })

  it('resets the epoch returned by the sidecar for the outgoing account', async () => {
    const calls: unknown[] = []
    mockIPC((cmd, args) => {
      calls.push([cmd, args])
      if (cmd === 'list_tunnels')
        return list
      if (cmd === 'reset_tunnels')
        return undefined
      throw new Error(`unmocked: ${cmd}`)
    })
    await platformBridge.resetTunnels(scope)
    expect(calls).toEqual([
      [
        'list_tunnels',
        { expectedUserId: 'outgoing' },
      ],
      [
        'reset_tunnels',
        { channelEpoch: 'outgoing-epoch' },
      ],
    ])
  })

  it('blocks all operations during reset and coalesces explicit retries of its exact epoch', async () => {
    const firstReset = deferred<void>()
    const retryReset = deferred<void>()
    const resetStarted = deferred<void>()
    const retryStarted = deferred<void>()
    const calls: Array<[string, unknown]> = []
    let resets = 0
    const resetStatuses: Array<unknown | null> = []
    const stopThrowingStatus = platformBridge.onTunnelResetStatusChange((status) => {
      if (status === null)
        throw new Error('observer failed on reset success')
    })
    const stopResetStatus = platformBridge.onTunnelResetStatusChange(status => resetStatuses.push(status))
    mockIPC((cmd, args) => {
      calls.push([cmd, args])
      switch (cmd) {
        case 'list_tunnels':
          return {
            ...list,
            channelEpoch: resets < 2
              ? 'outgoing-epoch'
              : 'successor-epoch',
          }
        case 'reset_tunnels':
          resets++
          if (resets === 1) {
            resetStarted.resolve()
            return firstReset.promise
          }
          retryStarted.resolve()
          return retryReset.promise
        case 'create_tunnel':
          return { kind: 'created', info }
        case 'delete_tunnel':
          return undefined
        default:
          throw new Error(`unmocked: ${cmd}`)
      }
    })

    const reset = expect(platformBridge.resetTunnels(scope)).rejects.toMatchObject({
      cause: expect.objectContaining({ message: 'reply lost' }),
    })
    adopt('outgoing')
    const blockedList = expect(platformBridge.listTunnels(scope)).rejects.toThrow('finish the desktop tunnel reset')
    const blockedCreate = expect(platformBridge.createTunnel(config, scope, async () => 'key')).rejects.toThrow('finish the desktop tunnel reset')
    const blockedDelete = expect(platformBridge.deleteTunnel('tunnel', scope)).rejects.toThrow('finish the desktop tunnel reset')
    await resetStarted.promise
    expect(calls.map(([cmd]) => cmd)).toEqual(['list_tunnels', 'reset_tunnels'])
    firstReset.reject(new Error('reply lost'))
    await Promise.all([
      reset,
      blockedList,
      blockedCreate,
      blockedDelete,
    ])

    await expect(platformBridge.listTunnels(scope)).rejects.toThrow('finish the desktop tunnel reset')
    expect(resets).toBe(1)
    const create = platformBridge.createTunnel(config, scope, async () => 'key')
    const remove = platformBridge.deleteTunnel('tunnel', scope)
    await retryStarted.promise
    expect(calls).toEqual([
      [
        'list_tunnels',
        { expectedUserId: 'outgoing' },
      ],
      [
        'reset_tunnels',
        { channelEpoch: 'outgoing-epoch' },
      ],
      [
        'reset_tunnels',
        { channelEpoch: 'outgoing-epoch' },
      ],
    ])
    retryReset.resolve()
    await expect(create).resolves.toEqual({ kind: 'created', info })
    await remove
    stopThrowingStatus()
    stopResetStatus()
    expect(resetStatuses).toHaveLength(3)
    expect(resetStatuses[0]).toBeNull()
    expect(resetStatuses[1]).toBeInstanceOf(Error)
    expect(resetStatuses[2]).toBeNull()
    expect(calls).toContainEqual([
      'create_tunnel',
      {
        config: {
          ...config,
          expectedWorkerKeysHex: 'key',
          expectedUserId: 'outgoing',
          channelEpoch: 'successor-epoch',
        },
      },
    ])
    expect(calls).toContainEqual([
      'delete_tunnel',
      { tunnelId: 'tunnel', channelEpoch: 'successor-epoch', expectedUserId: 'outgoing' },
    ])
    expect(resets).toBe(2)
  })

  it('retries the outgoing list only if reset failed before capturing an epoch', async () => {
    const calls: Array<[string, unknown]> = []
    mockIPC((cmd, args) => {
      calls.push([cmd, args])
      if (cmd === 'list_tunnels') {
        if (calls.length === 1)
          throw new Error('desktop unavailable')
        return list
      }
      return undefined
    })
    await expect(platformBridge.resetTunnels(scope)).rejects.toMatchObject({
      cause: expect.objectContaining({ message: 'desktop unavailable' }),
    })
    adopt('successor')
    await platformBridge.retryTunnelReset()
    await platformBridge.deleteTunnel('tunnel', scope)
    expect(calls).toEqual([
      [
        'list_tunnels',
        { expectedUserId: 'outgoing' },
      ],
      [
        'list_tunnels',
        { expectedUserId: 'outgoing' },
      ],
      [
        'reset_tunnels',
        { channelEpoch: 'outgoing-epoch' },
      ],
      [
        'list_tunnels',
        { expectedUserId: 'successor' },
      ],
      [
        'delete_tunnel',
        { tunnelId: 'tunnel', channelEpoch: 'outgoing-epoch', expectedUserId: 'successor' },
      ],
    ])
  })

  it.each(['reply', 'rejection'] as const)('refuses a native list %s overtaken by account retirement', async (outcome) => {
    const nativeListing = deferred<typeof list>()
    const entered = deferred<void>()
    const nativeFailure = new Error('native list failed')
    let retireAfterScopeCheck = false
    dispose()
    dispose = platformBridge.setTunnelTrustScope(() => {
      const checked = scope
      if (retireAfterScopeCheck) {
        retireAfterScopeCheck = false
        queueMicrotask(() => adopt('outgoing'))
      }
      return checked
    })
    mockIPC(() => {
      entered.resolve()
      return nativeListing.promise.finally(() => {
        retireAfterScopeCheck = true
      })
    })

    const originalScope = scope
    const listing = expect(platformBridge.listTunnels(originalScope)).rejects.toMatchObject({ name: 'AbortError' })
    await entered.promise
    if (outcome === 'reply')
      nativeListing.resolve(list)
    else
      nativeListing.reject(nativeFailure)
    await listing
    expect(originalScope.signal.aborted).toBe(true)
  })

  it('serializes native snapshots without holding a later list across create trust', async () => {
    const heldListing = deferred<typeof list>()
    const firstReadStarted = deferred<void>()
    const trustStarted = deferred<void>()
    const trust = deferred<string>()
    const currentListing = { ...list, channelEpoch: 'replacement-epoch' }
    const calls: Array<[string, unknown]> = []
    let listings = 0
    mockIPC((cmd, args) => {
      calls.push([cmd, args])
      if (cmd === 'list_tunnels') {
        if (listings++ === 0) {
          firstReadStarted.resolve()
          return heldListing.promise
        }
        return currentListing
      }
      if (cmd === 'create_tunnel')
        return { kind: 'created', info }
      return undefined
    })

    const first = platformBridge.listTunnels(scope).catch((cause: unknown) => cause)
    await firstReadStarted.promise
    const create = platformBridge.createTunnel(config, scope, () => {
      trustStarted.resolve()
      return trust.promise
    })
    await settle()
    const beforeReply = [...calls]
    heldListing.resolve(list)
    const firstResult = await first
    await trustStarted.promise
    await expect(platformBridge.listTunnels(scope)).resolves.toEqual(currentListing)
    trust.resolve('key')
    await expect(create).resolves.toEqual({ kind: 'created', info })
    await platformBridge.resetTunnels(scope)

    expect(firstResult).toEqual(list)
    expect(beforeReply).toEqual([
      [
        'list_tunnels',
        { expectedUserId: 'outgoing' },
      ],
    ])
    expect(calls).toEqual([
      [
        'list_tunnels',
        { expectedUserId: 'outgoing' },
      ],
      [
        'list_tunnels',
        { expectedUserId: 'outgoing' },
      ],
      [
        'list_tunnels',
        { expectedUserId: 'outgoing' },
      ],
      [
        'create_tunnel',
        {
          config: expect.objectContaining({ channelEpoch: 'replacement-epoch' }),
        },
      ],
      [
        'reset_tunnels',
        { channelEpoch: 'replacement-epoch' },
      ],
    ])
  })

  it('lets reset and the successor proceed while a retired native list is held', async () => {
    const heldListing = deferred<typeof list>()
    const oldReadStarted = deferred<void>()
    const calls: Array<[string, unknown]> = []
    let outgoingReads = 0
    let nativeEpoch = list.channelEpoch
    mockIPC((cmd, args) => {
      calls.push([cmd, args])
      switch (cmd) {
        case 'list_tunnels': {
          const { expectedUserId } = args as { expectedUserId: string }
          if (expectedUserId === 'outgoing' && ++outgoingReads === 2) {
            oldReadStarted.resolve()
            return heldListing.promise
          }
          return {
            tunnels: expectedUserId === 'outgoing'
              ? [info]
              : [],
            channelEpoch: nativeEpoch,
          }
        }
        case 'reset_tunnels':
          if ((args as { channelEpoch: string }).channelEpoch === nativeEpoch)
            nativeEpoch = 'successor-epoch'
          return undefined
        default:
          throw new Error(`unmocked: ${cmd}`)
      }
    })

    await expect(platformBridge.listTunnels(scope)).resolves.toEqual(list)
    const oldListing = platformBridge.listTunnels(scope).catch((err: unknown) => err)
    await oldReadStarted.promise
    const queuedListing = platformBridge.listTunnels(scope).catch((err: unknown) => err)
    await settle()
    nativeEpoch = 'replacement-epoch'
    const reset = platformBridge.resetTunnels(scope)
    adopt('successor')
    const successorListing = platformBridge.listTunnels(scope)
    await reset
    const successorResult = await successorListing
    heldListing.resolve(list)
    const [oldResult, queuedResult] = await Promise.all([oldListing, queuedListing])
    await platformBridge.resetTunnels(scope)

    expect(successorResult).toEqual({ tunnels: [], channelEpoch: 'successor-epoch' })
    expect(oldResult).toMatchObject({ name: 'AbortError' })
    expect(queuedResult).toMatchObject({ name: 'AbortError' })
    expect(calls).toEqual([
      [
        'list_tunnels',
        { expectedUserId: 'outgoing' },
      ],
      [
        'list_tunnels',
        { expectedUserId: 'outgoing' },
      ],
      [
        'list_tunnels',
        { expectedUserId: 'outgoing' },
      ],
      [
        'reset_tunnels',
        { channelEpoch: 'replacement-epoch' },
      ],
      [
        'list_tunnels',
        { expectedUserId: 'successor' },
      ],
      [
        'reset_tunnels',
        { channelEpoch: 'successor-epoch' },
      ],
    ])
  })

  it('preserves a live native list failure and starts the next scoped read', async () => {
    const heldListing = deferred<typeof list>()
    const firstReadStarted = deferred<void>()
    const nativeFailure = new Error('native list failed')
    let calls = 0
    mockIPC(() => {
      if (++calls === 1) {
        firstReadStarted.resolve()
        return heldListing.promise
      }
      return list
    })

    const first = expect(platformBridge.listTunnels(scope)).rejects.toBe(nativeFailure)
    await firstReadStarted.promise
    const next = platformBridge.listTunnels(scope)
    heldListing.reject(nativeFailure)
    await first
    await expect(next).resolves.toEqual(list)
    expect(calls).toBe(2)
  })

  it('resolves trust after the epoch lookup and refuses dispatch when trust changes', async () => {
    const epoch = deferred<typeof list>()
    const entered = deferred<void>()
    const invocations: string[] = []
    mockIPC((cmd) => {
      invocations.push(cmd)
      entered.resolve()
      return epoch.promise
    })
    let approved = true
    const resolveTrust = vi.fn(async () => {
      if (!approved)
        throw new Error('verify the Worker key again')
      return 'approved-key'
    })
    const create = expect(platformBridge.createTunnel(config, scope, resolveTrust)).rejects.toThrow('verify the Worker key again')
    await entered.promise
    expect(resolveTrust).not.toHaveBeenCalled()
    approved = false
    epoch.resolve(list)
    await create
    expect(invocations).toEqual(['list_tunnels'])
  })

  it('refuses create if the epoch lookup outlives its identity, including same-user adoption', async () => {
    const epoch = deferred<typeof list>()
    const entered = deferred<void>()
    const invocations: string[] = []
    mockIPC((cmd) => {
      invocations.push(cmd)
      entered.resolve()
      return epoch.promise
    })
    const resolveTrust = vi.fn(async () => 'approved-key')
    const create = expect(platformBridge.createTunnel(config, scope, resolveTrust)).rejects.toMatchObject({ name: 'AbortError' })
    await entered.promise
    adopt('outgoing')
    epoch.resolve(list)
    await create
    expect(resolveTrust).not.toHaveBeenCalled()
    expect(invocations).toEqual(['list_tunnels'])
  })

  it('refuses native create when the final resolver returns no trust', async () => {
    const invocations: string[] = []
    mockIPC((cmd) => {
      invocations.push(cmd)
      return list
    })
    await expect(platformBridge.createTunnel(config, scope, async () => '')).rejects.toThrow('verified Worker key')
    expect(invocations).toEqual(['list_tunnels'])
  })

  it('replays deletion natively and derives bulk targets from the scoped list', async () => {
    const invocations: string[] = []
    const args: unknown[] = []
    mockIPC((cmd, value) => {
      invocations.push(cmd)
      args.push(value)
      return { ...list, tunnels: [] }
    })
    await platformBridge.deleteTunnel('removed', scope)
    await expect(platformBridge.deleteTunnels('w1', scope)).resolves.toEqual([])
    expect(invocations).toEqual(['list_tunnels', 'delete_tunnel', 'list_tunnels'])
    expect(args[1]).toEqual({
      tunnelId: 'removed',
      channelEpoch: list.channelEpoch,
      expectedUserId: scope.userId,
    })
  })

  it('keeps browser reset and list free of native invocations', async () => {
    delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__
    await expect(platformBridge.resetTunnels(scope)).resolves.toBeUndefined()
    await expect(platformBridge.listTunnels(scope)).resolves.toEqual({ tunnels: [], channelEpoch: '' })
  })
})

describe('restoreWindowGeometry', () => {
  let calls: Array<{ cmd: string, args: Record<string, unknown> }>

  // Record every IPC call and answer the window-state queries with `state`,
  // so the mode the save handler computes is controllable per test.
  // Returns the live state, so a test can move the window's mode part-way
  // through: the saver reads it on every resize, which is how "the user took
  // the mode back" is expressed.
  function installIPC(state: { fullscreen: boolean, maximized: boolean }) {
    mockIPC((cmd, args) => {
      calls.push({ cmd, args: (args ?? {}) as Record<string, unknown> })
      switch (cmd) {
        case 'plugin:window|is_fullscreen':
          return state.fullscreen
        case 'plugin:window|is_maximized':
          return state.maximized
        default:
          return null
      }
    })
    return state
  }

  const cmds = () => calls.map(c => c.cmd)
  const saved = () => calls.find(c => c.cmd === 'save_window_geometry')?.args

  beforeEach(() => {
    calls = []
    mockWindows('main')
  })

  afterEach(() => {
    clearMocks()
    vi.useRealTimers()
    delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__
  })

  it('sizes to the saved windowed geometry then enters fullscreen', async () => {
    installIPC({ fullscreen: true, maximized: false })
    await restoreWindowGeometry(1280, 800, 'fullscreen')

    expect(cmds()).toContain('plugin:window|set_size')
    expect(cmds()).toContain('plugin:window|show')
    expect(cmds()).not.toContain('plugin:window|maximize')
    const fs = calls.find(c => c.cmd === 'plugin:window|set_fullscreen')
    expect(fs?.args).toMatchObject({ value: true })
  })

  it('maximizes without entering fullscreen when mode is maximized', async () => {
    installIPC({ fullscreen: false, maximized: true })
    await restoreWindowGeometry(1280, 800, 'maximized')

    expect(cmds()).toContain('plugin:window|maximize')
    expect(cmds()).not.toContain('plugin:window|set_fullscreen')
  })

  it('never sets size when the saved windowed geometry is absent', async () => {
    installIPC({ fullscreen: false, maximized: false })
    await restoreWindowGeometry(0, 0, 'normal')

    expect(cmds()).not.toContain('plugin:window|set_size')
    expect(cmds()).toContain('plugin:window|show')
  })

  it('saves fullscreen with zeroed dims so the windowed size is preserved', async () => {
    vi.useFakeTimers()
    installIPC({ fullscreen: true, maximized: false })
    await restoreWindowGeometry(1280, 800, 'fullscreen')

    calls.length = 0
    window.dispatchEvent(new Event('resize'))
    await vi.advanceTimersByTimeAsync(600)

    expect(saved()).toMatchObject({ width: 0, height: 0, mode: 'fullscreen' })
  })

  it('saves the live viewport dimensions with normal mode when windowed', async () => {
    vi.useFakeTimers()
    installIPC({ fullscreen: false, maximized: false })
    await restoreWindowGeometry(1000, 700, 'normal')

    calls.length = 0
    window.dispatchEvent(new Event('resize'))
    await vi.advanceTimersByTimeAsync(600)

    expect(saved()).toMatchObject({
      width: window.innerWidth,
      height: window.innerHeight,
      mode: 'normal',
    })
  })

  it('saves maximized with zeroed dims so the windowed size is preserved', async () => {
    vi.useFakeTimers()
    installIPC({ fullscreen: false, maximized: true })
    await restoreWindowGeometry(1280, 800, 'maximized')

    calls.length = 0
    window.dispatchEvent(new Event('resize'))
    await vi.advanceTimersByTimeAsync(600)

    expect(saved()).toMatchObject({ width: 0, height: 0, mode: 'maximized' })
  })

  it('prefers fullscreen over maximized when the window reports both', async () => {
    // A native-fullscreen window can simultaneously report zoomed; fullscreen
    // must win, else exiting fullscreen would restore into a maximized state
    // the user never chose.
    vi.useFakeTimers()
    installIPC({ fullscreen: true, maximized: true })
    await restoreWindowGeometry(1280, 800, 'fullscreen')

    calls.length = 0
    window.dispatchEvent(new Event('resize'))
    await vi.advanceTimersByTimeAsync(600)

    expect(saved()).toMatchObject({ width: 0, height: 0, mode: 'fullscreen' })
  })

  it('replaces the resize saver on re-entry instead of stacking listeners', async () => {
    // The launcher can remount (switching connection mode returns to it), which
    // re-runs restoreWindowGeometry. Each re-entry must replace the prior saver,
    // not leak another resize listener -- otherwise one resize fans out into N
    // duplicate save_window_geometry IPC calls.
    vi.useFakeTimers()
    installIPC({ fullscreen: false, maximized: false })
    await restoreWindowGeometry(1000, 700, 'normal')
    await restoreWindowGeometry(1000, 700, 'normal')
    await restoreWindowGeometry(1000, 700, 'normal')

    calls.length = 0
    window.dispatchEvent(new Event('resize'))
    await vi.advanceTimersByTimeAsync(600)

    const saves = calls.filter(c => c.cmd === 'save_window_geometry')
    expect(saves).toHaveLength(1)
  })

  // The three launch states the Rust shell decides. Every test above is the
  // `normal` one; these two are what a login launch produces when the user
  // asked LeapMux to start out of the way.
  it('applies the saved size and mode but shows nothing for a hidden launch', async () => {
    installIPC({ fullscreen: false, maximized: true })
    await restoreWindowGeometry(1280, 800, 'maximized', 'hidden')

    // The size AND the maximized flag both land, so the first "Show LeapMux"
    // from the tray produces the window the user left behind. `maximize()`
    // sets a window-state flag an unmapped window keeps, and skipping it lost
    // the mode for every login launch, permanently -- the saver below then
    // overwrote the stored `maximized` with the `normal` it observed.
    expect(cmds()).toContain('plugin:window|set_size')
    expect(cmds()).toContain('plugin:window|maximize')
    expect(cmds()).not.toContain('plugin:window|show')
    expect(cmds()).not.toContain('plugin:window|set_fullscreen')
    expect(cmds()).not.toContain('plugin:window|minimize')
  })

  // Fullscreen is the one mode a launch that starts out of the way cannot
  // restore, because macOS performs the transition on a visible window only.
  // The saved preference must SURVIVE that, or the first resize after the user
  // reveals the window destroys it.
  it('keeps the saved fullscreen mode that a hidden launch could not apply', async () => {
    vi.useFakeTimers()
    installIPC({ fullscreen: false, maximized: false })
    await restoreWindowGeometry(1280, 800, 'fullscreen', 'hidden')
    expect(cmds()).not.toContain('plugin:window|set_fullscreen')

    calls.length = 0
    window.dispatchEvent(new Event('resize'))
    await vi.advanceTimersByTimeAsync(600)

    // The window reads as `normal` because the launch left it that way, not
    // because the user chose it, so the saved mode is what gets stored.
    expect(saved()).toMatchObject({ mode: 'fullscreen' })
  })

  it('lets the user take the mode back after a hidden launch', async () => {
    vi.useFakeTimers()
    const live = installIPC({ fullscreen: false, maximized: false })
    await restoreWindowGeometry(1280, 800, 'fullscreen', 'hidden')

    // The user reveals the window from the tray and maximizes it. From that
    // observation on, what the window reports is the truth.
    live.maximized = true
    calls.length = 0
    window.dispatchEvent(new Event('resize'))
    await vi.advanceTimersByTimeAsync(600)
    expect(saved()).toMatchObject({ mode: 'maximized' })

    live.maximized = false
    calls.length = 0
    window.dispatchEvent(new Event('resize'))
    await vi.advanceTimersByTimeAsync(600)
    expect(saved()).toMatchObject({ mode: 'normal' })
  })

  it('shows before it minimizes for a minimized launch', async () => {
    installIPC({ fullscreen: false, maximized: false })
    await restoreWindowGeometry(1280, 800, 'normal', 'minimized')

    // The window has to be mapped before it is minimized, or there is no
    // taskbar button or Dock tile to minimize into.
    expect(cmds()).toContain('plugin:window|minimize')
    expect(cmds().indexOf('plugin:window|show'))
      .toBeLessThan(cmds().indexOf('plugin:window|minimize'))
  })

  it('never enters fullscreen for a launch that starts out of the way', async () => {
    for (const launch of ['minimized', 'hidden'] as const) {
      calls = []
      installIPC({ fullscreen: false, maximized: false })
      await restoreWindowGeometry(1280, 800, 'fullscreen', launch)
      // Fullscreen takes over a macOS Space, which a window the user asked to
      // start minimized or hidden must never do.
      expect(cmds(), launch).not.toContain('plugin:window|set_fullscreen')
    }
  })

  it('treats an unstated launch as a normal one', async () => {
    installIPC({ fullscreen: false, maximized: true })
    await restoreWindowGeometry(1280, 800, 'maximized')

    expect(cmds()).toContain('plugin:window|show')
    expect(cmds()).toContain('plugin:window|maximize')
    expect(cmds()).not.toContain('plugin:window|minimize')
  })
})

describe('parseLaunchVisibility', () => {
  it('passes through the three states the shell reports', () => {
    expect(parseLaunchVisibility('normal')).toBe('normal')
    expect(parseLaunchVisibility('minimized')).toBe('minimized')
    expect(parseLaunchVisibility('hidden')).toBe('hidden')
  })

  // Failing towards a VISIBLE window is the only safe direction here: the
  // other way leaves an app the user cannot reach.
  it('narrows anything else to normal', () => {
    for (const raw of [
      undefined,
      null,
      '',
      'Hidden',
      'invisible',
      0,
      {},
      ['hidden'],
    ])
      expect(parseLaunchVisibility(raw), JSON.stringify(raw ?? null)).toBe('normal')
  })
})

describe('parseDesktopBehaviorRefusals', () => {
  it('narrows the two choices the operating system can refuse', () => {
    expect(parseDesktopBehaviorRefusals([
      { setting: 'trayEnabled', message: 'no library' },
    ]))
      .toEqual([
        { setting: 'trayEnabled', message: 'no library' },
      ])
    expect(parseDesktopBehaviorRefusals([
      { setting: 'startOnLogin', message: 'declined' },
    ]))
      .toEqual([
        { setting: 'startOnLogin', message: 'declined' },
      ])
  })

  // Both at once is the case the list exists for: a Linux desktop with no
  // status-icon library can also be one whose system declines a login item.
  it('keeps every refusal of one push', () => {
    expect(parseDesktopBehaviorRefusals([
      { setting: 'trayEnabled', message: 'no library' },
      { setting: 'startOnLogin', message: 'declined' },
    ])).toEqual([
      { setting: 'trayEnabled', message: 'no library' },
      { setting: 'startOnLogin', message: 'declined' },
    ])
  })

  // An entry the shape check refuses is DROPPED, not fatal to the list: one
  // unrecognized `setting` must not hide a real refusal reported beside it.
  it('drops an unrecognizable entry and keeps the rest', () => {
    expect(parseDesktopBehaviorRefusals([
      { setting: 'trayOnClose', message: 'not refusable' },
      { setting: 'startOnLogin', message: 'declined' },
    ])).toEqual([
      { setting: 'startOnLogin', message: 'declined' },
    ])
  })

  // Anything else belongs to no row. Printing a transport error beside a
  // toggle explains nothing, and a `setting` that points at a row which does
  // not exist would put the message nowhere while the caller believed it
  // reported one.
  it('reports nothing for anything that is not a refusal list', () => {
    for (const err of [
      undefined,
      null,
      'no library',
      new Error('ipc closed'),
      // The old single-object shape is not a list, so it reports nothing.
      { setting: 'trayEnabled', message: 'no library' },
      [undefined],
      [null],
      ['no library'],
      [
        { message: 'no setting' },
      ],
      [
        { setting: 'trayEnabled' },
      ],
      [
        { setting: 'trayEnabled', message: '' },
      ],
      [
        { setting: 'trayEnabled', message: 42 },
      ],
      [
        { setting: 'trayOnClose', message: 'not refusable' },
      ],
      [
        { setting: 'desktop.trayEnabled', message: 'a row id, not a field' },
      ],
    ])
      expect(parseDesktopBehaviorRefusals(err), JSON.stringify(err ?? null)).toEqual([])
  })
})

describe('platformBridge getStartupInfo', () => {
  const WIRE_BUILD = {
    version: '1.2.3',
    commit_hash: 'abc',
    commit_time: 't1',
    build_time: 't2',
    branch: 'main',
  }
  const WIRE_CONFIG = {
    mode: 'solo',
    hub_url: '',
    window_width: 0,
    window_height: 0,
    window_mode: 'normal',
  }

  beforeEach(() => {
    (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__ = {}
  })

  afterEach(() => {
    clearMocks()
    delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__
  })

  it('carries the launch decision the shell reported', async () => {
    mockIPC(() => ({ config: WIRE_CONFIG, build_info: WIRE_BUILD, launch_visibility: 'hidden' }))
    await expect(platformBridge.getStartupInfo()).resolves.toMatchObject({ launchVisibility: 'hidden' })
  })

  // The field is snake_case on the wire and camelCase in the app, and a shell
  // too old to report it sends nothing. Both must land on a visible window.
  it('reads a missing launch decision as normal', async () => {
    mockIPC(() => ({ config: WIRE_CONFIG, build_info: WIRE_BUILD }))
    await expect(platformBridge.getStartupInfo()).resolves.toMatchObject({ launchVisibility: 'normal' })
  })
})

describe('channel relay bridge', () => {
  beforeEach(() => {
    window.__TAURI_INTERNALS__ = {}
  })

  afterEach(() => {
    clearMocks()
    delete window.__TAURI_INTERNALS__
  })

  it('acknowledges a raw frame before reading its following close', async () => {
    const firstRead = deferred<ArrayBuffer>()
    mockIPC((cmd, args) => {
      if (cmd === 'open_channel_relay' || cmd === 'close_channel_relay')
        return null
      if (cmd === 'read_channel_relay') {
        const after = (args as { afterCursor: string }).afterCursor
        if (after === '0')
          return firstRead.promise
        if (after === '1') {
          return encodeRelayRead(2n, {
            close: { code: 1001, reason: 'going away', wasClean: true },
          })
        }
      }
      throw new Error(`unmocked Tauri command: ${cmd}`)
    })

    const socket = new TauriRelayWebSocket()
    const delivered: (ArrayBuffer | CloseEvent)[] = []
    socket.onmessage = event => delivered.push(event.data as ArrayBuffer)
    socket.onclose = event => delivered.push(event)
    const opened = deferred<void>()
    socket.onopen = () => opened.resolve()
    await opened.promise

    expect(delivered).toEqual([])
    const frame = new Uint8Array(1024).fill(255)
    firstRead.resolve(encodeRelayRead(1n, { data: frame }))
    await vi.waitFor(() => expect(delivered).toHaveLength(2))
    expect(Array.from(new Uint8Array(delivered[0] as ArrayBuffer))).toEqual(Array.from(frame))
    expect(delivered[1]).toMatchObject({ code: 1001, reason: 'going away', wasClean: true })
  })

  it('accepts a raw frame serialized by Tauri fallback IPC', async () => {
    const hold = deferred<ArrayBuffer>()
    mockIPC((cmd, args) => {
      if (cmd === 'open_channel_relay')
        return null
      if (cmd === 'read_channel_relay') {
        if ((args as { afterCursor: string }).afterCursor === '0') {
          return Array.from(new Uint8Array(encodeRelayRead(1n, {
            data: new Uint8Array([1, 2, 3]),
          })))
        }
        return hold.promise
      }
      throw new Error(`unmocked Tauri command: ${cmd}`)
    })
    const socket = new TauriRelayWebSocket()
    const opened = deferred<void>()
    const frame = deferred<ArrayBuffer>()
    socket.onopen = () => opened.resolve()
    socket.onmessage = event => frame.resolve(event.data as ArrayBuffer)
    await opened.promise

    expect(Array.from(new Uint8Array(await frame.promise))).toEqual([1, 2, 3])
  })

  it('skips IPC when the attempt ends before the Tauri import settles', async () => {
    const invoke = vi.fn()
    mockIPC(invoke)
    let abandoned = false
    const open = platformBridge.openUserEventsRelay(42, {
      isAbandoned: () => abandoned,
      onMessage: vi.fn(),
    })

    abandoned = true
    await expect(open).resolves.toBe(false)
    expect(invoke).not.toHaveBeenCalled()
  })

  it('closes the wrapper when the open is rejected before reaching Rust', async () => {
    mockIPC((cmd) => {
      throw new Error(`IPC refused the command: ${cmd}`)
    })
    const socket = new TauriRelayWebSocket()
    const failed = deferred<void>()
    socket.onclose = () => failed.resolve()
    await failed.promise

    expect(socket.readyState).toBe(WebSocket.CLOSED)
  })
})

// The relay ids the sidecar fences on are only useful if they reach it: an open or
// close that dropped its id would be indistinguishable from any other attempt's, and
// the sidecar would be back to inferring intent from goroutine scheduling order.
describe('userevents relay bridge', () => {
  beforeEach(() => {
    (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__ = {}
  })

  afterEach(() => {
    clearMocks()
    delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__
  })

  it('forwards the relay id with the user events subscription on open', async () => {
    const calls: Array<{ cmd: string, args: unknown }> = []
    mockIPC((cmd, args) => {
      if (cmd === 'read_userevents_relay')
        return new Promise(() => {})
      calls.push({ cmd, args })
      return null
    })

    await platformBridge.openUserEventsRelay(42, {
      isAbandoned: () => false,
      onMessage: vi.fn(),
    }, ['ws-1'])

    expect(calls).toEqual([
      {
        cmd: 'open_userevents_relay',
        args: {
          relayId: 42,
          workspaceIds: ['ws-1'],
          resumeHlc: null,
          resumeEpoch: null,
        },
      },
    ])
  })

  it('serializes a populated resume cursor as wire HLC + epoch string', async () => {
    // Pins the populated branch of openUserEventsRelay: resume.hlc must flow
    // through formatHlcWire (not the raw proto object) and resume.epoch must
    // be .toString()-ed (Tauri IPC carries no bigint). A regression that
    // drops the epoch arg, passes the proto object directly, or skips the
    // wire format would round-trip green through the null-cursor test above
    // but make the desktop sidecar reject the relay-open on every reconnect.
    const calls: Array<{ cmd: string, args: unknown }> = []
    mockIPC((cmd, args) => {
      if (cmd === 'read_userevents_relay')
        return new Promise(() => {})
      calls.push({ cmd, args })
      return null
    })

    await platformBridge.openUserEventsRelay(7, {
      isAbandoned: () => false,
      onMessage: vi.fn(),
    }, ['ws-1'], {
      hlc: { physical: 1754100000000n, logical: 3n, clientId: 'c-abc' },
      epoch: 7n,
    })

    expect(calls).toEqual([
      {
        cmd: 'open_userevents_relay',
        args: {
          relayId: 7,
          workspaceIds: ['ws-1'],
          resumeHlc: '1754100000000.3.c-abc',
          resumeEpoch: '7',
        },
      },
    ])
  })

  it('forwards the relay id on close', async () => {
    const calls: Array<{ cmd: string, args: unknown }> = []
    mockIPC((cmd, args) => {
      calls.push({ cmd, args })
      return null
    })

    await platformBridge.closeUserEventsRelay(42)

    expect(calls).toEqual([
      {
        cmd: 'close_userevents_relay',
        args: { relayId: 42 },
      },
    ])
  })
})

// The behavior tests above mock the IPC layer, so they bypass Tauri's
// permission allowlist entirely. A window command the frontend invokes but
// the Rust capabilities never grant fails only at runtime -- exactly the
// `set_fullscreen not allowed` class of bug. This guard drives the real
// restore path, captures the commands it emits, and asserts each is granted
// in capabilities/default.json so the two stay in sync.
describe('desktop window capabilities', () => {
  // `plugin:window|set_fullscreen` -> `core:window:allow-set-fullscreen`.
  function permissionFor(ipcCommand: string): string {
    const command = ipcCommand.replace('plugin:window|', '').replace(/_/g, '-')
    return `core:window:allow-${command}`
  }

  function grantedPermissions(): string[] {
    // import.meta.dirname is frontend/src/api; the capabilities live at the
    // repo root under desktop/rust.
    const path = resolve(
      (import.meta as { dirname: string }).dirname,
      '../../../desktop/rust/capabilities/default.json',
    )
    const config = JSON.parse(readFileSync(path, 'utf8')) as { permissions: string[] }
    return config.permissions
  }

  afterEach(() => {
    clearMocks()
    delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__
  })

  it('grants every window command the geometry-restore path invokes', async () => {
    const invoked = new Set<string>()
    mockWindows('main')
    mockIPC((cmd) => {
      if (cmd.startsWith('plugin:window|'))
        invoked.add(cmd)
      return null
    })

    // Both branches together exercise every mutating command on the restore
    // path: set_size + show (shared), maximize (maximized), set_fullscreen
    // (fullscreen). No resize is dispatched, so only mutating commands --
    // which must be granted explicitly -- are captured, not the is_* reads
    // that ride on core:default.
    await restoreWindowGeometry(1280, 800, 'fullscreen')
    await restoreWindowGeometry(1280, 800, 'maximized')

    const commands = [...invoked]
    const granted = grantedPermissions()
    expect(commands).toContain('plugin:window|set_fullscreen')
    for (const cmd of commands) {
      expect(granted, `${cmd} needs ${permissionFor(cmd)} in capabilities/default.json`)
        .toContain(permissionFor(cmd))
    }
  })
})

describe('observeWindowMode', () => {
  // Answer the window-state reads so the derived mode is controllable, and
  // give the event listener a real rid so onResized resolves cleanly.
  function installIPC(state: { fullscreen: boolean, maximized: boolean }) {
    mockIPC((cmd) => {
      switch (cmd) {
        case 'plugin:window|is_fullscreen':
          return state.fullscreen
        case 'plugin:window|is_maximized':
          return state.maximized
        case 'plugin:event|listen':
          return 1
        default:
          return null
      }
    })
  }

  beforeEach(() => {
    mockWindows('main')
  })

  afterEach(() => {
    clearMocks()
    delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__
  })

  it('pushes the derived mode once on attach', async () => {
    installIPC({ fullscreen: false, maximized: true })
    const onChange = vi.fn()

    const dispose = observeWindowMode(onChange)
    await vi.waitFor(() => expect(onChange).toHaveBeenCalledTimes(1))

    expect(onChange).toHaveBeenCalledWith('maximized')
    expect(() => dispose()).not.toThrow()
  })

  it('reports fullscreen even when the window also reports maximized', async () => {
    installIPC({ fullscreen: true, maximized: true })
    const onChange = vi.fn()

    observeWindowMode(onChange)
    await vi.waitFor(() => expect(onChange).toHaveBeenCalledTimes(1))

    expect(onChange).toHaveBeenCalledWith('fullscreen')
  })

  it('is inert outside the Tauri app (no window global)', () => {
    // No mockWindows here -> isTauriApp() is false; the observer must no-op
    // and hand back a disposer that is safe to call.
    delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__
    const onChange = vi.fn()

    const dispose = observeWindowMode(onChange)

    expect(onChange).not.toHaveBeenCalled()
    expect(() => dispose()).not.toThrow()
  })
})

describe('windowExitFullscreen', () => {
  afterEach(() => {
    clearMocks()
    delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__
  })

  it('invokes set_fullscreen(false) on the current window', async () => {
    // The CustomTitlebar tests mock this bridge, so they can't catch a
    // setFullscreen(true) copy-paste bug -- assert the actual IPC value here.
    const calls: Array<{ cmd: string, args: Record<string, unknown> }> = []
    mockWindows('main')
    mockIPC((cmd, args) => {
      calls.push({ cmd, args: (args ?? {}) as Record<string, unknown> })
      return null
    })

    await windowExitFullscreen()

    const fs = calls.find(c => c.cmd === 'plugin:window|set_fullscreen')
    expect(fs).toBeDefined()
    expect(fs!.args).toMatchObject({ value: false })
  })

  it('is inert outside the Tauri app (no window global)', async () => {
    delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__
    await expect(windowExitFullscreen()).resolves.toBeUndefined()
  })
})

describe('parseRelayClosePayload', () => {
  it('passes through a well-formed sidecar close payload', () => {
    expect(parseRelayClosePayload({ code: 1000, reason: 'bye', wasClean: true }))
      .toEqual({ code: 1000, reason: 'bye', wasClean: true })
  })

  it('defaults each field when the payload is missing or wrong-typed', () => {
    // A malformed close uses 1006 (abnormal closure) as its default code.
    expect(parseRelayClosePayload(null)).toEqual({ code: 1006, reason: '', wasClean: false })
    expect(parseRelayClosePayload({ code: '1000', reason: 42, wasClean: 'yes' }))
      .toEqual({ code: 1006, reason: '', wasClean: false })
  })

  it('does not treat a truthy-but-non-true wasClean as clean', () => {
    expect(parseRelayClosePayload({ wasClean: 1 }).wasClean).toBe(false)
  })
})

describe('openExternalUrl', () => {
  let openSpy: ReturnType<typeof vi.spyOn>

  beforeEach(() => {
    showWarnToastWithLoggedCause.mockClear()
    // jsdom's window.open is a stub that logs "not implemented"; replace it so
    // the browser branch is observable and quiet.
    openSpy = vi.spyOn(window, 'open').mockReturnValue(null)
  })

  afterEach(() => {
    openSpy.mockRestore()
    clearMocks()
    delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__
  })

  it('opens a new tab with no opener in the browser build', async () => {
    await openExternalUrl('https://example.test/path')

    expect(openSpy).toHaveBeenCalledWith('https://example.test/path', '_blank', 'noopener,noreferrer')
  })

  it('routes to the opener plugin under the desktop shell', async () => {
    (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__ = {}
    const invoked: { cmd: string, args: unknown }[] = []
    mockIPC((cmd, args) => {
      invoked.push({ cmd, args })
      return null
    })

    await openExternalUrl('https://example.test/path')

    // The same command the opener plugin's own click listener runs for an
    // `<a target="_blank">`, which is the whole reason no capability changes.
    expect(invoked.map(i => i.cmd)).toContain('plugin:opener|open_url')
    // And never the browser branch: window.open returns null in the webview.
    expect(openSpy).not.toHaveBeenCalled()
  })

  it('reports a refused open instead of rejecting', async () => {
    (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__ = {}
    mockIPC(() => {
      throw new Error('opener.open_url not allowed. Command not found')
    })

    // Must not reject: the caller fires this from an event handler with no
    // error sink, and a rejection there becomes "Something went wrong".
    await expect(openExternalUrl('https://example.test/')).resolves.toBeUndefined()
    expect(showWarnToastWithLoggedCause).toHaveBeenCalledWith('Could not open the link', expect.any(Error))
  })

  it.each([
    ['mailto:someone@example.test'],
    ['tel:+15550100'],
  ])('hands %s to the opener too, matching the plugin scope', async (url) => {
    // This gateway mirrors `allow-default-urls`, which is wider than the
    // TERMINAL policy: `classifyUntrustedLink` refuses both of these before a
    // click ever reaches here, and a first-party `mailto:` link must still work.
    (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__ = {}
    const invoked: string[] = []
    mockIPC((cmd) => {
      invoked.push(cmd)
      return null
    })

    await openExternalUrl(url)

    expect(invoked).toContain('plugin:opener|open_url')
  })

  it.each([
    ['file:///etc/passwd'],
    ['javascript:alert(1)'],
    ['vscode://file/etc/passwd'],
    ['not a url at all'],
  ])('refuses %s without reaching either backend', async (url) => {
    (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__ = {}
    const invoked: string[] = []
    mockIPC((cmd) => {
      invoked.push(cmd)
      return null
    })

    await openExternalUrl(url)

    expect(invoked).toEqual([])
    expect(openSpy).not.toHaveBeenCalled()
  })
})

describe('getCapabilities', () => {
  beforeEach(() => {
    (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__ = {}
    resetPlatformRuntimeState()
  })

  afterEach(() => {
    clearMocks()
    resetPlatformRuntimeState()
    delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__
  })

  // A capability the shell has not confirmed stays false, so no local-only
  // feature shows before the runtime state answers.
  it('reports every local capability false before the shell answers, under Tauri', () => {
    expect(getCapabilities()).toEqual({
      mode: 'tauri-desktop-distributed',
      tunnels: false,
      appControl: false,
      windowControl: false,
      systemPermissions: false,
      localSolo: false,
    })
  })
})

describe('platformBridge connectDistributed', () => {
  beforeEach(() => {
    (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__ = {}
    resetPlatformRuntimeState()
  })

  afterEach(() => {
    clearMocks()
    resetPlatformRuntimeState()
    delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__
  })

  it('reads the runtime state again after the command succeeds', async () => {
    let runtimeStateReads = 0
    const commands: string[] = []
    mockIPC((cmd) => {
      commands.push(cmd)
      if (cmd === 'connect_distributed')
        return undefined
      if (cmd === 'get_runtime_state') {
        runtimeStateReads++
        return runtimeStateReads === 1
          // The state before the connect: the launcher, not yet connected.
          ? {
              shellMode: 'launcher',
              connected: false,
              hubUrl: '',
              capabilities: getCapabilities(),
            }
          : {
              shellMode: 'distributed',
              connected: true,
              hubUrl: 'https://hub.example',
              capabilities: {
                mode: 'tauri-desktop-distributed',
                tunnels: false,
                appControl: true,
                windowControl: true,
                systemPermissions: true,
                localSolo: false,
              },
            }
      }
      return undefined
    })

    await platformBridge.getRuntimeState()
    expect(runtimeStateReads).toBe(1)

    await platformBridge.connectDistributed('https://hub.example')

    // The command alone would leave the pre-connect state cached; the refresh
    // is what makes the connected Hub observable here.
    expect(runtimeStateReads).toBe(2)
    expect(desktopHub()).toEqual({ kind: 'remote', url: 'https://hub.example' })

    // The first runtime-state read starts the sidecar log listener on a
    // floating import chain; let it land before the mock goes away.
    await vi.waitFor(() => expect(commands).toContain('plugin:event|listen'))
  })
})

describe('platformBridge startBrowserAuth', () => {
  beforeEach(() => {
    (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__ = {}
  })

  afterEach(() => {
    clearMocks()
    delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__
  })

  it('starts each ceremony kind through one command carrying the kind', async () => {
    const invoked: Array<[string, unknown]> = []
    mockIPC((cmd, args) => {
      invoked.push([cmd, args])
      return { ceremonyId: 'c-1', authorizeUrl: 'https://hub.example/authorize' }
    })

    await expect(platformBridge.startBrowserAuth('sign-in')).resolves.toEqual({
      ceremonyId: 'c-1',
      authorizeUrl: 'https://hub.example/authorize',
    })
    await expect(platformBridge.startBrowserAuth('step-up')).resolves.toEqual({
      ceremonyId: 'c-1',
      authorizeUrl: 'https://hub.example/authorize',
    })

    expect(invoked).toEqual([
      [
        'start_browser_auth',
        { kind: BrowserAuthKind.SIGN_IN },
      ],
      [
        'start_browser_auth',
        { kind: BrowserAuthKind.STEP_UP },
      ],
    ])
  })
})
