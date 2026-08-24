import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  BOOT_SPLASH_LABEL,
  BOOT_SPLASH_STATIC_ID,
  BOOT_SPLASH_TEST_ID,
} from '~/lib/bootSplashTheme'

/**
 * The entry owns the boot handoff: it removes the static splash only after
 * `mount()` returns. When mount throws, the splash must stay in the document,
 * so the boot watchdog still owns that failure class. These tests evaluate
 * the real entry module with `@solidjs/start/client` (and the entry's other
 * side-effectful imports) mocked, so the module body runs in jsdom exactly as
 * it runs in the browser. A source-text order pin used to guard this; it
 * false-failed on a reformat and missed a removal moved into a `catch`, so
 * the behavior is pinned directly instead.
 */
describe('entry-client boot handoff', () => {
  beforeEach(() => {
    vi.resetModules()
    document.body.innerHTML = `<div id="app"><div id="${BOOT_SPLASH_STATIC_ID}" data-testid="${BOOT_SPLASH_TEST_ID}"><p>${BOOT_SPLASH_LABEL}</p></div></div>`
    vi.doMock('@solidjs/start/client', () => ({
      mount: vi.fn(),
      StartClient: () => null,
    }))
    vi.doMock('~/components/common/Toast', () => ({ showWarnToast: vi.fn() }))
    vi.doMock('~/lib/ignorableErrorEvents', () => ({ installIgnorableErrorSuppressor: vi.fn() }))
    vi.doMock('~/lib/installGlobalErrorSink', () => ({ installGlobalErrorSink: vi.fn() }))
    vi.doMock('~/lib/renderPipelineWarmup', () => ({ scheduleRenderPipelineWarmup: vi.fn() }))
  })

  afterEach(() => {
    vi.doUnmock('@solidjs/start/client')
    vi.doUnmock('~/components/common/Toast')
    vi.doUnmock('~/lib/ignorableErrorEvents')
    vi.doUnmock('~/lib/installGlobalErrorSink')
    vi.doUnmock('~/lib/renderPipelineWarmup')
    document.body.replaceChildren()
  })

  it('removes the static splash after mount', async () => {
    await import('~/entry-client')
    expect(document.getElementById(BOOT_SPLASH_STATIC_ID)).toBeNull()
  })

  it('keeps the static splash for the watchdog when mount throws', async () => {
    vi.doMock('@solidjs/start/client', () => ({
      mount: () => {
        throw new Error('entry graph fault')
      },
      StartClient: () => null,
    }))
    await expect(import('~/entry-client')).rejects.toThrow('entry graph fault')
    expect(document.getElementById(BOOT_SPLASH_STATIC_ID)).not.toBeNull()
  })
})
