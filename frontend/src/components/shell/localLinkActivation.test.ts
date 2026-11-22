import type { LocalLinkActivationDeps, LocalLinkActivationSource } from './localLinkActivation'
import { Code } from '@connectrpc/connect'
import { describe, expect, it, vi } from 'vitest'
import { ChannelError } from '~/lib/channelError'
import { deferred, settle } from '~/test-support/async'
import { createLocalLinkActivator } from './localLinkActivation'

function makeSource(overrides: Partial<LocalLinkActivationSource> = {}): LocalLinkActivationSource {
  return {
    workerId: 'w-1',
    baseDir: '/repo/docs',
    workingDir: '/repo',
    homeDir: '/home/alice',
    flavor: 'posix',
    ...overrides,
  }
}

function makeDeps(overrides: Partial<LocalLinkActivationDeps> = {}) {
  const statFile = vi.fn<LocalLinkActivationDeps['statFile']>()
  const openFile = vi.fn<LocalLinkActivationDeps['openFile']>()
  const revealDirectory = vi.fn<LocalLinkActivationDeps['revealDirectory']>()
  const showWarning = vi.fn<LocalLinkActivationDeps['showWarning']>()
  const deps: LocalLinkActivationDeps = {
    statFile,
    openFile,
    revealDirectory,
    showWarning,
    ...overrides,
  }
  return {
    deps,
    statFile,
    openFile,
    revealDirectory,
    showWarning,
  }
}

describe('createLocalLinkActivator', () => {
  it('calls activateSource synchronously before statFile resolves', async () => {
    const gate = deferred<{ info?: { path: string, isDir: boolean }, canonicalPath?: string }>()
    const statFile = vi.fn(() => gate.promise)
    const { deps, openFile } = makeDeps({ statFile })
    const activateSource = vi.fn()
    const activator = createLocalLinkActivator(deps)

    const pending = activator.activate('./guide.md', makeSource({ activateSource }))

    // Synchronous call before statFile settles.
    expect(activateSource).toHaveBeenCalledTimes(1)
    expect(statFile).toHaveBeenCalledTimes(1)
    expect(statFile).toHaveBeenCalledWith('w-1', { path: '/repo/docs/guide.md' })
    expect(openFile).not.toHaveBeenCalled()

    gate.resolve({
      info: { path: '/repo/docs/guide.md', isDir: false },
      canonicalPath: '/repo/docs/guide.md',
    })
    await pending

    expect(openFile).toHaveBeenCalledWith('/repo/docs/guide.md', {
      workerId: 'w-1',
      workingDir: '/repo',
    })
  })

  it('opens a file with source workingDir and falls back to parentDirectory when workingDir is empty', async () => {
    const { deps, statFile, openFile } = makeDeps()
    statFile.mockResolvedValue({
      info: { path: '/repo/docs/guide.md', isDir: false },
      canonicalPath: '/repo/docs/guide.md',
    })
    const activator = createLocalLinkActivator(deps)

    await activator.activate('./guide.md', makeSource())
    expect(openFile).toHaveBeenLastCalledWith('/repo/docs/guide.md', {
      workerId: 'w-1',
      workingDir: '/repo',
    })

    await activator.activate('./guide.md', makeSource({ workingDir: '' }))
    expect(openFile).toHaveBeenLastCalledWith('/repo/docs/guide.md', {
      workerId: 'w-1',
      workingDir: '/repo/docs',
    })
  })

  it('falls back across candidates on NotFound and PermissionDenied but stops immediately on transport errors', async () => {
    const { deps, statFile, openFile, showWarning } = makeDeps()
    statFile
      .mockRejectedValueOnce(new ChannelError('rpc', 'not found', { code: Code.NotFound }))
      .mockResolvedValueOnce({
        info: { path: '/repo/docs/app.ts', isDir: false },
      })
    const activator = createLocalLinkActivator(deps)

    // POSIX tries exact `app.ts:42`, then falls back to `app.ts` on NotFound.
    await activator.activate('./app.ts:42', makeSource())
    expect(statFile.mock.calls.map(c => c[1].path)).toEqual([
      '/repo/docs/app.ts:42',
      '/repo/docs/app.ts',
    ])
    expect(openFile).toHaveBeenCalledWith('/repo/docs/app.ts', {
      workerId: 'w-1',
      workingDir: '/repo',
    })

    // Repo-root fallback on PermissionDenied for `/LICENSE.md`.
    statFile.mockReset()
    statFile
      .mockRejectedValueOnce(new ChannelError('rpc', 'permission denied', { code: Code.PermissionDenied }))
      .mockResolvedValueOnce({
        info: { path: '/repo/LICENSE.md', isDir: false },
      })
    await activator.activate('/LICENSE.md', makeSource())
    expect(statFile.mock.calls.map(c => c[1].path)).toEqual([
      '/LICENSE.md',
      '/repo/LICENSE.md',
    ])
    expect(openFile).toHaveBeenLastCalledWith('/repo/LICENSE.md', {
      workerId: 'w-1',
      workingDir: '/repo',
    })

    // Transport error on candidate 1 stops priority selection even if candidate 2 succeeds.
    statFile.mockReset()
    const transportErr = new ChannelError('transport', 'channel not open')
    statFile
      .mockRejectedValueOnce(transportErr)
      .mockResolvedValueOnce({
        info: { path: '/repo/LICENSE.md', isDir: false },
      })
    await activator.activate('/LICENSE.md', makeSource())
    expect(openFile).toHaveBeenCalledTimes(2)
    expect(showWarning).toHaveBeenCalledWith('Cannot open local link', transportErr)
  })

  it('formats RPC errors, transport errors, and unresolvable paths distinctly', async () => {
    const { deps, statFile, openFile, revealDirectory, showWarning } = makeDeps()
    const activator = createLocalLinkActivator(deps)

    // Unresolvable path (empty baseDir and workingDir for relative link): no cause.
    await activator.activate('./missing.md', makeSource({ baseDir: '', workingDir: '' }))
    expect(showWarning).toHaveBeenLastCalledWith('Cannot resolve local path: ./missing.md')
    expect(showWarning.mock.calls.at(-1)?.length).toBe(1)

    // RPC error preserves message and passes error as cause.
    const rpcErr = new ChannelError('rpc', 'file not found', { code: Code.NotFound })
    statFile.mockRejectedValueOnce(rpcErr)
    await activator.activate('./nope.md', makeSource())
    expect(showWarning).toHaveBeenLastCalledWith('Cannot open local link: file not found', rpcErr)

    // Transport error hides raw transport text and passes error as cause.
    const transportErr = new ChannelError('transport', 'channel not open')
    statFile.mockRejectedValueOnce(transportErr)
    await activator.activate('./nope.md', makeSource())
    expect(showWarning).toHaveBeenLastCalledWith('Cannot open local link', transportErr)

    // Empty StatFileResponse metadata falls through to generic warning with diagnostic Error cause.
    statFile.mockResolvedValueOnce({})
    await activator.activate('./empty.md', makeSource())
    expect(showWarning).toHaveBeenLastCalledWith('Cannot open local link', expect.any(Error))

    expect(openFile).not.toHaveBeenCalled()
    expect(revealDirectory).not.toHaveBeenCalled()
  })

  it('reveals a directory inside treeRoot including symlinks inside treeRoot', async () => {
    const { deps, statFile, revealDirectory, showWarning } = makeDeps()
    const activator = createLocalLinkActivator(deps)

    statFile.mockResolvedValueOnce({
      info: { path: '/repo/docs/nested', isDir: true },
      canonicalPath: '/repo/docs/nested',
    })
    await activator.activate('./nested', makeSource())
    expect(revealDirectory).toHaveBeenLastCalledWith({
      path: '/repo/docs/nested',
      canonicalPath: '/repo/docs/nested',
    })

    // Symlink directory inside treeRoot whose canonicalPath points outside treeRoot.
    statFile.mockResolvedValueOnce({
      info: { path: '/repo/link', isDir: true },
      canonicalPath: '/outside/dir',
    })
    await activator.activate('../link', makeSource())
    expect(revealDirectory).toHaveBeenLastCalledWith({
      path: '/repo/link',
      canonicalPath: '/outside/dir',
    })
    expect(showWarning).not.toHaveBeenCalled()
  })

  it('translates symlinked ancestor directories via cached canonical root stat and respects clearCanonicalRootCache', async () => {
    const { deps, statFile, revealDirectory } = makeDeps()
    statFile.mockImplementation(async (_workerId, req) => {
      if (req.path === '/private/tmp/repo/sub') {
        return {
          info: { path: '/private/tmp/repo/sub', isDir: true },
          canonicalPath: '/private/tmp/repo/sub',
        }
      }
      if (req.path === '/tmp/repo') {
        return {
          info: { path: '/tmp/repo', isDir: true },
          canonicalPath: '/private/tmp/repo',
        }
      }
      throw new ChannelError('rpc', 'not found', { code: Code.NotFound })
    })
    const activator = createLocalLinkActivator(deps)
    const source = makeSource({
      baseDir: '/tmp/repo',
      workingDir: '/tmp/repo',
    })

    await activator.activate('/private/tmp/repo/sub', source)
    expect(revealDirectory).toHaveBeenLastCalledWith({
      path: '/tmp/repo/sub',
      canonicalPath: '/private/tmp/repo/sub',
    })
    expect(statFile.mock.calls.filter(c => c[1].path === '/tmp/repo')).toHaveLength(1)

    // Second reveal reuses the cached canonical root stat with zero extra root RPC.
    await activator.activate('/private/tmp/repo/sub', source)
    expect(statFile.mock.calls.filter(c => c[1].path === '/tmp/repo')).toHaveLength(1)

    // Clearing the cache forces a fresh root stat on the next reveal.
    activator.clearCanonicalRootCache()
    await activator.activate('/private/tmp/repo/sub', source)
    expect(statFile.mock.calls.filter(c => c[1].path === '/tmp/repo')).toHaveLength(2)
  })

  it('warns without revealing when target directory lies outside a resolved treeRoot', async () => {
    const { deps, statFile, openFile, revealDirectory, showWarning } = makeDeps()
    statFile.mockImplementation(async (_workerId, req) => {
      if (req.path === '/other/dir') {
        return {
          info: { path: '/other/dir', isDir: true },
          canonicalPath: '/other/dir',
        }
      }
      return {
        info: { path: '/repo', isDir: true },
        canonicalPath: '/repo',
      }
    })
    const activator = createLocalLinkActivator(deps)

    await activator.activate('/other/dir', makeSource())
    expect(statFile).toHaveBeenCalledWith('w-1', { path: '/repo' })
    expect(openFile).not.toHaveBeenCalled()
    expect(revealDirectory).not.toHaveBeenCalled()
    expect(showWarning).toHaveBeenCalledWith('Directory is outside the current file tree: /other/dir')
    expect(showWarning.mock.calls[0]?.length).toBe(1)
  })

  it('falls through cleanly to outside-tree warning when statFile(treeRoot) rejects', async () => {
    const { deps, statFile, revealDirectory, showWarning } = makeDeps()
    statFile.mockImplementation(async (_workerId, req) => {
      if (req.path === '/other/dir') {
        return {
          info: { path: '/other/dir', isDir: true },
          canonicalPath: '/other/dir',
        }
      }
      throw new Error('working directory removed')
    })
    const activator = createLocalLinkActivator(deps)

    await activator.activate('/other/dir', makeSource())
    expect(revealDirectory).not.toHaveBeenCalled()
    expect(showWarning).toHaveBeenCalledWith('Directory is outside the current file tree: /other/dir')
    expect(showWarning.mock.calls[0]?.length).toBe(1)
  })

  it('supersedes an earlier in-flight activation when a newer activation starts', async () => {
    const firstGate = deferred<{ info?: { path: string, isDir: boolean }, canonicalPath?: string }>()
    const secondGate = deferred<{ info?: { path: string, isDir: boolean }, canonicalPath?: string }>()
    const { deps, openFile, revealDirectory } = makeDeps({
      statFile: vi.fn((_workerId, req) => (req.path.endsWith('first.md')
        ? firstGate.promise
        : secondGate.promise)),
    })
    const activator = createLocalLinkActivator(deps)

    const p1 = activator.activate('./first.md', makeSource())
    const p2 = activator.activate('./second.md', makeSource())

    secondGate.resolve({
      info: { path: '/repo/docs/second.md', isDir: false },
    })
    await p2
    firstGate.resolve({
      info: { path: '/repo/docs/first.md', isDir: false },
    })
    await p1

    expect(openFile).toHaveBeenCalledTimes(1)
    expect(openFile).toHaveBeenCalledWith('/repo/docs/second.md', {
      workerId: 'w-1',
      workingDir: '/repo',
    })
    expect(revealDirectory).not.toHaveBeenCalled()
  })

  it('bails silently on cancelPending during both target statFile and treeRoot canonical statFile', async () => {
    const targetGate = deferred<{ info?: { path: string, isDir: boolean }, canonicalPath?: string }>()
    const rootGate = deferred<{ info?: { path: string, isDir: boolean }, canonicalPath?: string }>()
    const { deps, openFile, revealDirectory, showWarning } = makeDeps({
      statFile: vi.fn((_workerId, req) => (req.path === '/tmp/repo'
        ? rootGate.promise
        : targetGate.promise)),
    })
    const activator = createLocalLinkActivator(deps)

    // Cancel while target statFile is pending.
    const p1 = activator.activate('./a.md', makeSource())
    activator.cancelPending()
    targetGate.reject(new ChannelError('rpc', 'not found', { code: Code.NotFound }))
    await p1
    expect(openFile).not.toHaveBeenCalled()
    expect(revealDirectory).not.toHaveBeenCalled()
    expect(showWarning).not.toHaveBeenCalled()

    // Cancel while treeRoot statFile is pending.
    const { deps: deps2, openFile: open2, revealDirectory: reveal2, showWarning: warn2 } = makeDeps({
      statFile: vi.fn((_workerId, req) => {
        if (req.path === '/tmp/repo')
          return rootGate.promise
        return Promise.resolve({
          info: { path: '/private/tmp/repo/sub', isDir: true },
          canonicalPath: '/private/tmp/repo/sub',
        })
      }),
    })
    const activator2 = createLocalLinkActivator(deps2)
    const p2 = activator2.activate('/private/tmp/repo/sub', makeSource({
      baseDir: '/tmp/repo',
      workingDir: '/tmp/repo',
    }))
    // Let the first statFile resolve so execution awaits rootGate.
    await Promise.resolve()
    await Promise.resolve()
    activator2.cancelPending()
    rootGate.resolve({
      info: { path: '/tmp/repo', isDir: true },
      canonicalPath: '/private/tmp/repo',
    })
    await p2
    expect(open2).not.toHaveBeenCalled()
    expect(reveal2).not.toHaveBeenCalled()
    expect(warn2).not.toHaveBeenCalled()
  })

  it('cancels an earlier in-flight activation when a newer activation fails to resolve', async () => {
    const firstGate = deferred<{ info?: { path: string, isDir: boolean }, canonicalPath?: string }>()
    const { deps, openFile, showWarning } = makeDeps({
      statFile: vi.fn(() => firstGate.promise),
    })
    const activator = createLocalLinkActivator(deps)

    const p1 = activator.activate('./slow.md', makeSource())
    // Second activation cannot resolve (empty workerId) and returns early.
    await activator.activate('./unresolvable.md', makeSource({ workerId: '' }))
    expect(showWarning).toHaveBeenCalledWith('Cannot resolve local path: ./unresolvable.md')

    firstGate.resolve({
      info: { path: '/repo/docs/slow.md', isDir: false },
    })
    await p1

    expect(openFile).not.toHaveBeenCalled()
  })

  it('ignores stale canonical root writeback when clearCanonicalRootCache runs mid-flight', async () => {
    const rootGate = deferred<{ info?: { path: string, isDir: boolean }, canonicalPath?: string }>()
    let rootCalls = 0
    const { deps, revealDirectory } = makeDeps({
      statFile: vi.fn((_workerId, req) => {
        if (req.path === '/tmp/repo') {
          rootCalls++
          if (rootCalls === 1)
            return rootGate.promise
          return Promise.resolve({
            info: { path: '/tmp/repo', isDir: true },
            canonicalPath: '/private/tmp/repo-new',
          })
        }
        return Promise.resolve({
          info: { path: req.path, isDir: true },
          canonicalPath: req.path,
        })
      }),
    })
    const activator = createLocalLinkActivator(deps)
    const source = makeSource({ baseDir: '/tmp/repo', workingDir: '/tmp/repo' })

    const p1 = activator.activate('/private/tmp/repo-old/sub', source)
    // Wait until target stat completes and treeRoot stat is in flight.
    await settle()
    expect(rootCalls).toBe(1)

    // Clear the cache without calling cancelPending.
    activator.clearCanonicalRootCache()
    rootGate.resolve({
      info: { path: '/tmp/repo', isDir: true },
      canonicalPath: '/private/tmp/repo-old',
    })
    await p1
    expect(revealDirectory).toHaveBeenCalledWith({
      path: '/tmp/repo/sub',
      canonicalPath: '/private/tmp/repo-old/sub',
    })

    // Next activation must NOT observe the pre-clear cached root value.
    await activator.activate('/private/tmp/repo-new/sub', source)
    expect(rootCalls).toBe(2)
    expect(revealDirectory).toHaveBeenLastCalledWith({
      path: '/tmp/repo/sub',
      canonicalPath: '/private/tmp/repo-new/sub',
    })
  })

  it('probes multi-candidate statFile calls concurrently while respecting priority order', async () => {
    const firstGate = deferred<{ info?: { path: string, isDir: boolean }, canonicalPath?: string }>()
    const secondGate = deferred<{ info?: { path: string, isDir: boolean }, canonicalPath?: string }>()
    const statFile = vi.fn((_workerId: string, req: { path: string }) => (req.path === '/LICENSE.md'
      ? firstGate.promise
      : secondGate.promise))
    const { deps, openFile } = makeDeps({ statFile })
    const activator = createLocalLinkActivator(deps)

    const pending = activator.activate('/LICENSE.md', makeSource())
    // Both candidates are dispatched concurrently before either settles.
    expect(statFile).toHaveBeenCalledTimes(2)
    expect(statFile.mock.calls.map(c => c[1].path)).toEqual(['/LICENSE.md', '/repo/LICENSE.md'])

    // Even if candidate 2 resolves first, candidate 1 wins when it succeeds.
    secondGate.resolve({
      info: { path: '/repo/LICENSE.md', isDir: false },
    })
    await Promise.resolve()
    firstGate.resolve({
      info: { path: '/LICENSE.md', isDir: false },
    })
    await pending

    expect(openFile).toHaveBeenCalledExactlyOnceWith('/LICENSE.md', {
      workerId: 'w-1',
      workingDir: '/repo',
    })
  })

  it('preserves leading backslashes in POSIX relative subpaths under a symlinked treeRoot', async () => {
    const { deps, statFile, revealDirectory } = makeDeps()
    statFile.mockImplementation(async (_workerId, req) => {
      if (req.path === '/private/tmp/repo/\\weird') {
        return {
          info: { path: '/private/tmp/repo/\\weird', isDir: true },
          canonicalPath: '/private/tmp/repo/\\weird',
        }
      }
      if (req.path === '/tmp/repo') {
        return {
          info: { path: '/tmp/repo', isDir: true },
          canonicalPath: '/private/tmp/repo',
        }
      }
      throw new ChannelError('rpc', 'not found', { code: Code.NotFound })
    })
    const activator = createLocalLinkActivator(deps)
    await activator.activate('/private/tmp/repo/%5Cweird', makeSource({
      baseDir: '/tmp/repo',
      workingDir: '/tmp/repo',
    }))
    expect(revealDirectory).toHaveBeenCalledWith({
      path: '/tmp/repo/\\weird',
      canonicalPath: '/private/tmp/repo/\\weird',
    })
  })
})
