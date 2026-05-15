import { clearMocks, mockIPC } from '@tauri-apps/api/mocks'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { platformBridge, readClipboardImage } from './platformBridge'

// isMac() in ~/lib/shortcuts/platform caches the UA-detected platform on
// first call, so flipping navigator.userAgent between tests doesn't actually
// flip its return value. Mock it directly so each test controls the gate.
const isMacMock = vi.hoisted(() => vi.fn<() => boolean>())
vi.mock('~/lib/shortcuts/platform', () => ({
  isMac: isMacMock,
  getPlatform: () => (isMacMock() ? 'mac' : 'linux'),
}))

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
      cb(new Blob([new Uint8Array([0x89, 0x50, 0x4E, 0x47])], { type: type ?? 'image/png' }))
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
      if (cmd === 'cli_path_status')
        return { state: 1, bundled: '/Applications/X.app/Contents/MacOS/leapmux', resolved: '', target: '', targetKind: 2 }
      throw new Error(`unmocked: ${cmd}`)
    })
    const got = await platformBridge.cliPathStatus()
    expect(got).toEqual({ state: 'ok', bundled: '/Applications/X.app/Contents/MacOS/leapmux' })
  })

  it('decodes STATE_MISSING (2) and surfaces install target + targetKind', async () => {
    mockIPC((cmd) => {
      if (cmd === 'cli_path_status')
        return { state: 2, bundled: '/b/leapmux', resolved: '', target: '/usr/local/bin/leapmux', targetKind: 1 }
      throw new Error(`unmocked: ${cmd}`)
    })
    const got = await platformBridge.cliPathStatus()
    expect(got).toEqual({ state: 'missing', bundled: '/b/leapmux', target: '/usr/local/bin/leapmux', targetKind: 'absent' })
  })

  it('decodes STATE_MISMATCH (3) and surfaces resolved + targetKind', async () => {
    mockIPC((cmd) => {
      if (cmd === 'cli_path_status')
        return { state: 3, bundled: '/b/leapmux', resolved: '/opt/homebrew/bin/leapmux', target: '', targetKind: 3 }
      throw new Error(`unmocked: ${cmd}`)
    })
    const got = await platformBridge.cliPathStatus()
    expect(got).toEqual({ state: 'mismatch', bundled: '/b/leapmux', resolved: '/opt/homebrew/bin/leapmux', targetKind: 'regular_file' })
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
        if (cmd === 'cli_path_status')
          return { state: 2, bundled: '/b/leapmux', resolved: '', target: '/usr/local/bin/leapmux', targetKind: c.code }
        throw new Error(`unmocked: ${cmd}`)
      })
      const got = await platformBridge.cliPathStatus()
      expect(got).toMatchObject({ state: 'missing', targetKind: c.decoded })
    }
  })

  it('decodes STATE_UNAVAILABLE (4) and an unknown numeric value into "unavailable"', async () => {
    mockIPC((cmd) => {
      if (cmd === 'cli_path_status')
        return { state: 4, bundled: '', resolved: '', target: '', targetKind: 0 }
      throw new Error(`unmocked: ${cmd}`)
    })
    expect(await platformBridge.cliPathStatus()).toEqual({ state: 'unavailable' })

    mockIPC((cmd) => {
      if (cmd === 'cli_path_status')
        return { state: 99, bundled: '', resolved: '', target: '', targetKind: 0 }
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
      { in: { result: 1, command: '', path: '', message: '' }, out: { result: 'ok' } },
      { in: { result: 2, command: 'sudo ln -sf a b', path: '', message: '' }, out: { result: 'needs_sudo', command: 'sudo ln -sf a b' } },
      { in: { result: 3, command: '', path: '/usr/local/bin/leapmux', message: '' }, out: { result: 'already_exists_real_file', path: '/usr/local/bin/leapmux' } },
      { in: { result: 4, command: 'sudo mkdir -p /usr/local/bin && ...', path: '/usr/local/bin', message: '' }, out: { result: 'parent_missing', path: '/usr/local/bin', command: 'sudo mkdir -p /usr/local/bin && ...' } },
      { in: { result: 5, command: '', path: '', message: 'disk full' }, out: { result: 'io_error', message: 'disk full' } },
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
        return { result: 1, command: '', path: '', message: '' }
      }
      throw new Error(`unmocked: ${cmd}`)
    })

    await platformBridge.cliInstallSymlink()
    await platformBridge.cliInstallSymlink(true)
    await platformBridge.cliInstallSymlink(false)
    expect(received).toEqual([{ force: false }, { force: true }, { force: false }])
  })

  it('falls back to an io_error for unknown result codes', async () => {
    mockIPC((cmd) => {
      if (cmd === 'cli_install_symlink')
        return { result: 0, command: '', path: '', message: '' }
      throw new Error(`unmocked: ${cmd}`)
    })
    expect(await platformBridge.cliInstallSymlink()).toEqual({
      result: 'io_error',
      message: 'unknown sidecar response',
    })
  })
})
