import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('./markdownWorkerClient', () => ({
  renderMarkdownInWorker: vi.fn(() => Promise.resolve(null)),
}))
vi.mock('./shikiWorkerClient', () => ({
  tokenizeAsync: vi.fn(() => Promise.resolve(null)),
}))
vi.mock('./renderArtifactStore', () => ({
  sweepArtifacts: vi.fn(() => Promise.resolve(0)),
}))

const { renderMarkdownInWorker } = await import('./markdownWorkerClient')
const { tokenizeAsync } = await import('./shikiWorkerClient')
const { sweepArtifacts } = await import('./renderArtifactStore')
const { _resetWarmupForTest, scheduleRenderPipelineWarmup, WARMUP_FALLBACK_DELAY_MS } = await import('./renderPipelineWarmup')

describe('renderpipelinewarmup', () => {
  beforeEach(() => {
    _resetWarmupForTest()
    vi.mocked(renderMarkdownInWorker).mockClear()
    vi.mocked(tokenizeAsync).mockClear()
    vi.mocked(sweepArtifacts).mockClear()
    ;(globalThis as unknown as { Worker: unknown }).Worker = class {}
  })

  afterEach(() => {
    delete (globalThis as unknown as { Worker?: unknown }).Worker
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('runs one worker warm-up per surface plus the artifact sweep at idle', () => {
    const callbacks: Array<() => void> = []
    vi.stubGlobal('requestIdleCallback', (cb: () => void) => {
      callbacks.push(cb)
      return 1
    })

    scheduleRenderPipelineWarmup()
    expect(callbacks).toHaveLength(1)
    expect(renderMarkdownInWorker).not.toHaveBeenCalled() // deferred to idle

    callbacks[0]()
    expect(renderMarkdownInWorker).toHaveBeenCalledTimes(1)
    expect(vi.mocked(renderMarkdownInWorker).mock.calls[0][0]).toContain('```ts')
    expect(tokenizeAsync).toHaveBeenCalledWith('typescript', 'const warm = 1')
    expect(sweepArtifacts).toHaveBeenCalledTimes(1)
  })

  it('schedules at most once per session', () => {
    const callbacks: Array<() => void> = []
    vi.stubGlobal('requestIdleCallback', (cb: () => void) => {
      callbacks.push(cb)
      return 1
    })
    scheduleRenderPipelineWarmup()
    scheduleRenderPipelineWarmup()
    expect(callbacks).toHaveLength(1)
  })

  it('is a no-op without Worker support', () => {
    delete (globalThis as unknown as { Worker?: unknown }).Worker
    const ric = vi.fn()
    vi.stubGlobal('requestIdleCallback', ric)
    scheduleRenderPipelineWarmup()
    expect(ric).not.toHaveBeenCalled()
  })

  it('falls back to a timeout when requestIdleCallback is unavailable (Safari)', () => {
    vi.stubGlobal('requestIdleCallback', undefined)
    vi.useFakeTimers()
    scheduleRenderPipelineWarmup()
    expect(renderMarkdownInWorker).not.toHaveBeenCalled()
    vi.advanceTimersByTime(WARMUP_FALLBACK_DELAY_MS)
    expect(renderMarkdownInWorker).toHaveBeenCalledTimes(1)
    expect(tokenizeAsync).toHaveBeenCalledTimes(1)
    expect(sweepArtifacts).toHaveBeenCalledTimes(1)
  })
})
