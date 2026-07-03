import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { clearMocks, mockIPC, mockWindows } from '@tauri-apps/api/mocks'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { observeWindowMode, platformBridge, readClipboardImage, restoreWindowGeometry, windowExitFullscreen } from './platformBridge'

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

describe('restoreWindowGeometry', () => {
  let calls: Array<{ cmd: string, args: Record<string, unknown> }>

  // Record every IPC call and answer the window-state queries with `state`,
  // so the mode the save handler computes is controllable per test.
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
