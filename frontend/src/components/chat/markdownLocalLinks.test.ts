import type { MarkdownLocalLinkActivationSource } from './markdownLocalLinks'
import { createEffect, createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { createMarkdownLocalLinkContext, withMarkdownLocalLinkBeforeActivate } from './markdownLocalLinks'

describe('createMarkdownLocalLinkContext', () => {
  it('memoizes each scalar field so unrelated metadata changes do not notify subscribers', () => {
    const [tab, setTab] = createSignal({
      workerId: 'w-1',
      workingDir: '/repo',
      title: 'Initial title',
      status: 'idle',
    })
    const onActivate = vi.fn()
    let ctx!: ReturnType<typeof createMarkdownLocalLinkContext>
    let runs = 0

    const dispose = createRoot((d) => {
      ctx = createMarkdownLocalLinkContext({
        workerId: () => tab().workerId,
        baseDir: () => tab().workingDir,
        workingDir: () => tab().workingDir,
        homeDir: () => '/home/alice',
        workerOs: () => 'linux',
        getOnActivate: () => onActivate,
      })

      createEffect(() => {
        void ctx.workerId
        void ctx.baseDir
        void ctx.workingDir
        void ctx.homeDir
        void ctx.flavor
        runs++
      })
      return d
    })

    try {
      expect(runs).toBe(1)

      // Updating unrelated tab metadata must not re-run downstream effects.
      setTab(prev => ({ ...prev, title: 'Renamed', status: 'busy' }))
      expect(runs).toBe(1)

      // Updating workingDir propagates immediately.
      setTab(prev => ({ ...prev, workingDir: '/repo/sub' }))
      expect(runs).toBe(2)
      expect(ctx.baseDir).toBe('/repo/sub')
      expect(ctx.workingDir).toBe('/repo/sub')
    }
    finally {
      dispose()
    }
  })

  it('reactively toggles workerId when getOnActivate changes and snapshots current values on activate', () => {
    createRoot((dispose) => {
      try {
        const [handler, setHandler] = createSignal<
          ((rawHref: string, source: MarkdownLocalLinkActivationSource) => void) | undefined
        >(undefined)
        const [os, setOs] = createSignal<string | undefined>('linux')
        const activateSource = vi.fn()

        const ctx = createMarkdownLocalLinkContext({
          workerId: () => 'w-1',
          baseDir: flavor => (flavor === 'win32'
            ? 'C:\\repo\\docs'
            : '/repo/docs'),
          workingDir: () => '/repo',
          homeDir: () => '/home/alice',
          workerOs: () => os(),
          getOnActivate: () => handler(),
          activateSource,
        })

        // Without an activator, workerId stays empty so MarkdownText stays non-interactive.
        expect(ctx.workerId).toBe('')
        ctx.onActivate('./README.md')

        const spy = vi.fn()
        setHandler(() => spy)
        expect(ctx.workerId).toBe('w-1')
        expect(ctx.flavor).toBe('posix')
        expect(ctx.baseDir).toBe('/repo/docs')

        setOs('windows')
        expect(ctx.flavor).toBe('win32')
        expect(ctx.baseDir).toBe('C:\\repo\\docs')

        ctx.onActivate('./README.md')
        expect(spy).toHaveBeenCalledWith('./README.md', {
          workerId: 'w-1',
          baseDir: 'C:\\repo\\docs',
          workingDir: '/repo',
          homeDir: '/home/alice',
          flavor: 'win32',
          activateSource,
        })

        // Clearing the activator returns workerId to '' reactively.
        setHandler(undefined)
        expect(ctx.workerId).toBe('')
      }
      finally {
        dispose()
      }
    })
  })

  it('wraps a parent context with beforeActivate while preserving reactive getters', () => {
    expect(withMarkdownLocalLinkBeforeActivate(undefined, vi.fn())).toBeUndefined()

    createRoot((dispose) => {
      try {
        const [dir, setDir] = createSignal('/repo')
        const order: string[] = []
        const parent = createMarkdownLocalLinkContext({
          workerId: () => 'w-1',
          baseDir: () => dir(),
          workingDir: () => dir(),
          homeDir: () => '/home/alice',
          workerOs: () => 'linux',
          getOnActivate: () => (href) => { order.push(`activate:${href}`) },
        })
        const wrapped = withMarkdownLocalLinkBeforeActivate(parent, () => {
          order.push('before')
        })!

        expect(wrapped.workerId).toBe('w-1')
        expect(wrapped.baseDir).toBe('/repo')
        setDir('/repo/next')
        expect(wrapped.baseDir).toBe('/repo/next')

        wrapped.onActivate('./doc.md')
        expect(order).toEqual(['before', 'activate:./doc.md'])
      }
      finally {
        dispose()
      }
    })
  })
})
