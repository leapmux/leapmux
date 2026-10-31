import { describe, expect, it } from 'bun:test'
import { alertedPackages, readAlerts } from './dependabot-alerts.mjs'

function alert(name, { ecosystem = name.startsWith('@')
  ? 'npm'
  : 'rust', manifest = name.startsWith('@')
  ? 'frontend/package.json'
  : 'desktop/rust/Cargo.lock', state = 'open' } = {}) {
  return {
    state,
    dependency: { manifest_path: manifest },
    security_vulnerability: {
      package: { name, ecosystem },
      vulnerable_version_range: '< 2.6.0',
      first_patched_version: { identifier: '2.6.0' },
    },
  }
}

describe('readAlerts', () => {
  it('follows GitHub canonical repository links using the authenticated repository identity', async () => {
    const calls = []
    const alerts = await readAlerts('fixture', async (url) => {
      calls.push(url)
      const parsed = new URL(url)
      if (parsed.pathname === '/repos/leapmux/leapmux')
        return Response.json({ id: 1151167922 })
      if (parsed.pathname === '/repositories/1151167922/dependabot/alerts') {
        return Response.json([
          alert('typescript'),
        ])
      }
      return Response.json([
        alert('tauri'),
      ], {
        headers: { link: '<https://api.github.com/repositories/1151167922/dependabot/alerts?state=open&per_page=100&after=cursor>; rel="next"' },
      })
    })
    expect(alerts).toHaveLength(2)
    expect(calls).toHaveLength(3)
    expect(calls[0]).toBe('https://api.github.com/repos/leapmux/leapmux')
  })
  const readTestAlerts = (token, request) => readAlerts(token, async (url, options) => {
    if (new URL(url).pathname === '/repos/leapmux/leapmux')
      return Response.json({ id: 1151167922 })
    return request(url, options)
  })

  it('follows opaque cursor links even after a short page and stops without a next link', async () => {
    const calls = []
    const cursor = 'opaque+/cursor=='
    const alerts = await readTestAlerts('fixture', async (url, options) => {
      expect(options.method).toBe('GET')
      const parsed = new URL(url)
      if (parsed.searchParams.has('page'))
        return Response.json({ message: 'Pagination using the page parameter is not supported.' }, { status: 400 })
      calls.push(url)
      if (calls.length === 1) {
        return Response.json([
          alert('tauri'),
        ], {
          headers: { link: `<https://api.github.com/repos/leapmux/leapmux/dependabot/alerts?state=open&per_page=100&after=${encodeURIComponent(cursor)}>; rel="next"` },
        })
      }
      expect(parsed.searchParams.get('after')).toBe(cursor)
      return Response.json(Array.from({ length: 100 }, () => alert('typescript')))
    })
    expect(alerts).toHaveLength(101)
    expect(calls).toHaveLength(2)
  })
  it('handles an empty response without another request', async () => {
    let requests = 0
    expect(await readTestAlerts('fixture', async () => {
      requests++
      return Response.json([])
    })).toEqual([])
    expect(requests).toBe(1)
  })
  it('rejects unsafe or malformed next links before forwarding credentials', async () => {
    const path = '/repos/leapmux/leapmux/dependabot/alerts?state=open&per_page=100&after=cursor'
    for (const link of [
      `<https://other.invalid${path}>; rel="next"`,
      `<http://api.github.com${path}>; rel="next"`,
      `<https://user@api.github.com${path}>; rel="next"`,
      `<https://api.github.com/repos/other/repo/dependabot/alerts?state=open&per_page=100&after=cursor>; rel="next"`,
      '<https://api.github.com/repositories/1/dependabot/alerts?state=open&per_page=100&after=cursor>; rel="next"',
      `<https://api.github.com${path}&page=2>; rel="next"`,
      `<https://api.github.com${path.replace('state=open', 'state=closed')}>; rel="next"`,
      `<https://api.github.com${path}#fragment>; rel="next"`,
      `<https://api.github.com${path.replace('&after=cursor', '')}>; rel="next"`,
      'invalid link header',
      `<https://api.github.com${path}>; rel="next", <https://api.github.com${path}&extra=1>; rel="next"`,
    ]) {
      let requests = 0
      await expect(readTestAlerts('fixture', async () => {
        requests++
        return Response.json([
          alert('tauri'),
        ], {
          headers: { link },
        })
      })).rejects.toThrow('pagination')
      expect(requests).toBe(1)
    }
  })
  it('rejects cursor cycles without retrying the repeated page', async () => {
    let requests = 0
    await expect(readTestAlerts('fixture', async () => {
      requests++
      if (requests > 2)
        throw new Error('The pagination cycle was not detected.')
      return Response.json([
        alert('tauri'),
      ], {
        headers: { link: '<https://api.github.com/repos/leapmux/leapmux/dependabot/alerts?state=open&per_page=100&after=cursor>; rel="next"' },
      })
    })).rejects.toThrow('pagination cycle')
    expect(requests).toBe(2)
  })
  it('fails on missing credentials, denied access, and malformed data', async () => {
    await expect(readTestAlerts('', async () => {
      throw new Error('A read without a token must not reach the network.')
    })).rejects.toThrow('requires a token that may read them')
    await expect(readTestAlerts('fixture', async () => new Response('', { status: 403 }))).rejects.toThrow('403')
    await expect(readTestAlerts('fixture', async () => Response.json({}))).rejects.toThrow()
  })
  it('rejects an invalid repository identity before requesting alerts', async () => {
    for (const id of [
      null,
      0,
      -1,
      '1151167922',
      Number.MAX_SAFE_INTEGER + 1,
    ]) {
      let requests = 0
      await expect(readAlerts('fixture', async (url) => {
        requests++
        expect(new URL(url).pathname).toBe('/repos/leapmux/leapmux')
        return Response.json({ id })
      })).rejects.toThrow('repository identity')
      expect(requests).toBe(1)
    }
  })
})

describe('alertedPackages', () => {
  it('lists the package of each open alert under the lockfile that records it, for a lockfile or a manifest path', () => {
    expect(alertedPackages([
      alert('tauri'),
      alert('glib'),
      alert('@tauri-apps/api'),
      alert('left-pad', { ecosystem: 'npm', manifest: 'frontend/bun.lock' }),
      alert('renovate-dep', { ecosystem: 'npm', manifest: '.github/renovate/package.json' }),
      alert('example.com/lib', { ecosystem: 'go', manifest: 'backend/go.mod' }),
    ])).toEqual(new Map([
      [
        'desktop/rust/Cargo.lock',
        new Set(['tauri', 'glib']),
      ],
      [
        'frontend/bun.lock',
        new Set(['@tauri-apps/api', 'left-pad']),
      ],
      [
        '.github/renovate/bun.lock',
        new Set(['renovate-dep']),
      ],
      [
        'backend/go.mod',
        new Set(['example.com/lib']),
      ],
    ]))
  })
  it('ignores a closed alert, an untracked file, a package of another ecosystem, and a malformed alert', () => {
    expect(alertedPackages([
      alert('tauri', { state: 'fixed' }),
      alert('tauri', { state: 'dismissed' }),
      alert('tauri', { manifest: 'other/Cargo.lock' }),
      alert('tauri', { manifest: null }),
      // A crate alert on an npm lockfile names a package that lockfile cannot hold.
      alert('tauri', { manifest: 'frontend/bun.lock' }),
      alert('actions/checkout', { ecosystem: 'actions', manifest: '.github/workflows/ci.yaml' }),
      { state: 'open' },
      null,
      {
        state: 'open',
        dependency: { manifest_path: 'desktop/rust/Cargo.lock' },
        security_vulnerability: {
          package: { ecosystem: 'rust' },
        },
      },
    ])).toEqual(new Map())
    expect(alertedPackages([])).toEqual(new Map())
  })
})
