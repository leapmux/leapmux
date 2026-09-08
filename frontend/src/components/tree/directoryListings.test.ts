import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ancestorChain, loadChildren, loadListings } from './directoryListings'

const listDirectory = vi.fn()
vi.mock('~/api/workerRpc', () => ({
  listDirectory: (...args: unknown[]) => listDirectory(...args),
}))

beforeEach(() => {
  listDirectory.mockReset()
})

function listing(path: string, names: string[] = []) {
  return {
    path,
    entries: names.map(name => ({
      name,
      path: `${path}/${name}`,
      isDir: false,
      hidden: false,
      size: 0n,
      modTime: '2026-01-01T00:00:00Z',
    })),
    truncated: false,
    totalEntries: names.length,
  }
}

describe('ancestorChain', () => {
  it('walks from the root down to the target, outermost first', () => {
    expect(ancestorChain('/', '/home/alice/proj', 'posix'))
      .toEqual(['/', '/home', '/home/alice', '/home/alice/proj'])
  })

  it('answers the root alone when the target is not under it', () => {
    expect(ancestorChain('/a', '/b/c', 'posix')).toEqual(['/a'])
  })

  it('answers the root alone for an empty target', () => {
    expect(ancestorChain('/a', '', 'posix')).toEqual(['/a'])
  })

  it('answers the root alone when the target IS the root', () => {
    expect(ancestorChain('/a', '/a', 'posix')).toEqual(['/a'])
  })

  /**
   * `split` rather than a scan for separators, because on win32 the VOLUME is
   * one leading segment. A hand-rolled walk over the separator would emit `C:`
   * as an ancestor, and `C:` identifies the current directory on drive C, not
   * its root.
   */
  it('never emits a bare drive letter as an ancestor on win32', () => {
    const chain = ancestorChain('C:\\', 'C:\\Users\\alice', 'win32')
    expect(chain).toEqual(['C:\\', 'C:\\Users', 'C:\\Users\\alice'])
    expect(chain).not.toContain('C:')
  })

  // The target reaches the tree in whatever spelling the user typed, so the
  // walk has to normalize before it compares.
  it('accepts a win32 target typed with forward slashes', () => {
    expect(ancestorChain('C:\\', 'C:/Users/alice', 'win32'))
      .toEqual(['C:\\', 'C:\\Users', 'C:\\Users\\alice'])
  })

  it('walks a UNC share root', () => {
    expect(ancestorChain('\\\\srv\\share\\', '\\\\srv\\share\\a\\b', 'win32'))
      .toEqual(['\\\\srv\\share\\', '\\\\srv\\share\\a', '\\\\srv\\share\\a\\b'])
  })
})

describe('loadListings', () => {
  it('asks for one directory when no root is given', async () => {
    listDirectory.mockResolvedValue({ listings: [listing('/a', ['x'])] })

    const resp = await loadListings('w1', '/a', true)

    expect(listDirectory.mock.calls[0][1]).toMatchObject({ path: '/a', dirsOnly: false })
    expect(listDirectory.mock.calls[0][1].fromRoot).toBeUndefined()
    expect(resp.listings.map(l => l.path)).toEqual(['/a'])
  })

  it('sends from_root for a chain, and dirs_only when files are hidden', async () => {
    listDirectory.mockResolvedValue({ listings: [listing('/'), listing('/a')] })

    const resp = await loadListings('w1', '/a', false, '/')

    expect(listDirectory.mock.calls[0][1]).toMatchObject({ path: '/a', fromRoot: '/', dirsOnly: true })
    expect(resp.listings.map(l => l.path)).toEqual(['/', '/a'])
  })

  // The worker names the directory its chain stopped at, and why. A chain that
  // completed carries nothing, so a tree cannot render a stale reason.
  it('carries the unreadable directory through, and omits it when absent', async () => {
    listDirectory.mockResolvedValueOnce({
      listings: [listing('/')],
      unreadable: { path: '/blocked', reason: 'permission denied' },
    })
    expect((await loadListings('w1', '/blocked/x', false, '/')).unreadable)
      .toEqual({ path: '/blocked', reason: 'permission denied' })

    listDirectory.mockResolvedValueOnce({ listings: [listing('/')] })
    expect((await loadListings('w1', '/', false)).unreadable).toBeUndefined()
  })

  // `size` arrives as a bigint on the wire, and the sessionStorage cache runs
  // JSON.stringify over this value -- which throws on a bigint.
  it('converts the wire bigint size to a number', async () => {
    listDirectory.mockResolvedValue({
      listings: [{ ...listing('/a'), entries: [{ name: 'x', path: '/a/x', isDir: false, hidden: false, size: 42n, modTime: '' }] }],
    })

    const [only] = (await loadListings('w1', '/a', true)).listings

    expect(only.entries[0].size).toBe(42)
    expect(() => JSON.stringify(only)).not.toThrow()
  })
})

describe('loadChildren', () => {
  it('answers the single listing the worker sent', async () => {
    listDirectory.mockResolvedValue({ listings: [listing('/a', ['x', 'y'])] })

    expect((await loadChildren('w1', '/a', true)).entries.map(e => e.displayName)).toEqual(['x', 'y'])
  })

  it('answers an empty directory as a listing with no entries', async () => {
    listDirectory.mockResolvedValue({ listings: [listing('/a')] })

    const only = await loadChildren('w1', '/a', true)

    expect(only.entries).toEqual([])
    expect(only.truncated).toBe(false)
  })

  /**
   * A worker that answers a single-directory request with NO listing committed
   * a protocol violation, not "the directory is empty" -- an empty directory
   * answers with one listing whose `entries` is empty. Throwing beats caching a
   * phantom listing, which the "already loaded" guard would then honour for
   * the rest of the session.
   */
  it('throws rather than caching a phantom listing', async () => {
    listDirectory.mockResolvedValue({ listings: [] })

    await expect(loadChildren('w1', '/a', true)).rejects.toThrow(/no listing for \/a/)
  })
})
