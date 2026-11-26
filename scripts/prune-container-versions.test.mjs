import { describe, expect, it } from 'bun:test'
import { pruneContainerVersions } from './prune-container-versions.mjs'

const versionsPath = '/orgs/leapmux/packages/container/leapmux/versions'
const versionsPaginationPath = '/organizations/259782689/packages/container/leapmux/versions'
const registryPath = '/v2/leapmux/leapmux'
const now = Date.parse('2026-11-27T00:00:00Z')
const cutoff = '2026-11-20T00:00:00Z'
const old = '2026-11-19T23:59:59Z'
const indexType = 'application/vnd.oci.image.index.v1+json'
const imageType = 'application/vnd.oci.image.manifest.v1+json'
const digest = id => `sha256:${id.toString(16).padStart(64, '0')}`
function index(...ids) {
  return {
    schemaVersion: 2,
    mediaType: indexType,
    manifests: ids.map(id => ({
      digest: digest(id),
    })),
  }
}
const image = { schemaVersion: 2, mediaType: imageType, layers: [] }
function version(id, { created = old, updated = created, tags = [] } = {}) {
  return {
    id,
    name: digest(id),
    created_at: created,
    updated_at: updated,
    metadata: {
      container: { tags },
    },
  }
}

function registryFixture({ pages, manifests = {}, liveTags = { dev: 1 }, deleteStatuses = {}, remaining = 900 }) {
  const deletes = []
  const request = async (url, options) => {
    const { origin, pathname, searchParams } = new URL(url)
    if (origin === 'https://api.github.com') {
      expect(options.headers.Authorization).toBe('Bearer fixture')
      if (options.method === 'DELETE') {
        const id = Number(pathname.slice(versionsPath.length + 1))
        expect(pathname).toBe(`${versionsPath}/${id}`)
        deletes.push(id)
        const status = deleteStatuses[id] ?? 204
        return new Response(null, {
          status,
          headers: {
            'X-RateLimit-Remaining': String(remaining),
          },
        })
      }
      expect(searchParams.get('state')).toBe('active')
      const page = Number(searchParams.get('page') ?? 1)
      expect(pathname).toBe(page === 1
        ? versionsPath
        : versionsPaginationPath)
      const headers = {
        'X-RateLimit-Remaining': String(remaining),
      }
      if (page < pages.length)
        headers.Link = `<${origin}${versionsPaginationPath}?page=${page + 1}&per_page=100&state=active>; rel="next"`
      return Response.json(pages[page - 1], { headers })
    }
    expect(origin).toBe('https://ghcr.io')
    if (pathname === '/token') {
      expect(searchParams.get('scope')).toBe('repository:leapmux/leapmux:pull')
      return Response.json({ token: 'pull-token' })
    }
    expect(options.headers.Authorization).toBe('Bearer pull-token')
    if (pathname === `${registryPath}/tags/list`) {
      const [tag, next] = Object.keys(liveTags).slice(searchParams.has('last')
        ? 1
        : 0)
      const headers = next
        ? { Link: `<${registryPath}/tags/list?last=${tag}&n=100>; rel="next"` }
        : {}
      return Response.json({
        name: 'leapmux/leapmux',
        tags: tag
          ? [tag]
          : [],
      }, { headers })
    }
    const reference = pathname.slice(`${registryPath}/manifests/`.length)
    const id = reference.startsWith('sha256:')
      ? Number.parseInt(reference.slice(7), 16)
      : liveTags[reference]
    const manifest = manifests[id] ?? (id === 1
      ? index()
      : image)
    return manifest instanceof Response
      ? manifest
      : Response.json(manifest, {
          headers: {
            'Docker-Content-Digest': digest(id),
          },
        })
  }
  return { deletes, request }
}

function prune(fixture, overrides) {
  return pruneContainerVersions({
    repository: 'leapmux/leapmux',
    token: 'fixture',
    now,
    pause: async () => {},
    request: fixture.request,
    ...overrides,
  })
}

describe('pruneContainerVersions', () => {
  it('deletes only old unreachable versions across all pages', async () => {
    const fixture = registryFixture({
      pages: [
        [
          version(8),
          version(9),
          version(6, { created: cutoff }),
          version(7, { updated: cutoff }),
          version(1),
        ],
        [
          version(2),
          version(3),
          version(4, {
            tags: ['release'],
          }),
          version(5),
          version(10),
          version(11),
          version(12),
          version(13),
        ],
      ],
      liveTags: { 'dev': 1, 'dev-ubuntu': 10 },
      manifests: {
        1: index(2),
        2: index(3),
        4: index(5),
        6: index(12),
        10: index(11),
        11: {
          ...image,
          subject: {
            digest: digest(13),
          },
        },
      },
    })
    expect(await prune(fixture)).toMatchObject({ eligible: 2, deleted: 2 })
    expect(fixture.deletes).toEqual([8, 9])
  })

  it('fails before deletion if it cannot read the protected graph or complete the inventory', async () => {
    for (const fixture of [
      registryFixture({
        pages: [
          [
            version(8),
            version(1),
          ],
        ],
        manifests: {
          1: index(2),
          2: new Response(null, { status: 404 }),
        },
      }),
      registryFixture({
        pages: [
          [
            version(8),
            version(1),
          ],
        ],
        manifests: {
          1: {
            ...index(),
            manifests: [{}],
          },
        },
      }),
      registryFixture({
        pages: [
          [
            version(8),
          ],
          [
            version(1),
            version(8),
          ],
        ],
      }),
      registryFixture({
        pages: [
          [
            version(8),
          ],
          [
            version(1),
            version(9, { created: '0' }),
          ],
        ],
      }),
      registryFixture({
        pages: [
          [
            version(8),
            version(1),
          ],
        ],
        liveTags: {},
      }),
    ]) {
      await expect(prune(fixture)).rejects.toThrow()
      expect(fixture.deletes).toEqual([])
    }
  })

  it('processes at most 300 deletes per publish and pauses between writes', async () => {
    const entries = Array.from({ length: 301 }, (_, position) => version(position + 2)).reverse()
    const fixture = registryFixture({
      pages: [
        entries.slice(0, 100),
        entries.slice(100, 200),
        entries.slice(200, 300),
        entries.slice(300),
      ],
    })
    const pauses = []
    const pause = async (milliseconds) => {
      pauses.push(milliseconds)
    }
    expect(await prune(fixture, { pause })).toEqual({ eligible: 301, deleted: 300 })
    expect(fixture.deletes).toEqual(Array.from({ length: 300 }, (_, position) => position + 2))
    expect(pauses).toEqual(Array.from({ length: 299 }).fill(1000))
  })

  it('refuses another repository or a missing token before making a request', async () => {
    const request = () => {
      throw new Error('Unexpected request')
    }
    await expect(prune({ request }, { repository: 'fork/leapmux' })).rejects.toThrow('requires the leapmux/leapmux repository')
    await expect(prune({ request }, { token: ' ' })).rejects.toThrow('requires the leapmux/leapmux repository')
  })

  it('plans a read-only run and limits writes to the available REST budget', async () => {
    const fixture = registryFixture({
      pages: [
        [
          version(1),
          version(9),
          version(8),
        ],
      ],
      remaining: 101,
    })
    expect(await prune(fixture, { dryRun: true })).toMatchObject({ eligible: 2, deleted: 0 })
    expect(fixture.deletes).toEqual([])
    expect(await prune(fixture)).toMatchObject({ eligible: 2, deleted: 1 })
    expect(fixture.deletes).toEqual([8])
  })

  it('stops on rate limiting and reports permission failures', async () => {
    const pages = [
      [
        version(1),
        version(8),
        version(9),
      ],
    ]
    const limited = registryFixture({
      pages,
      deleteStatuses: { 8: 429 },
    })
    expect(await prune(limited)).toMatchObject({ eligible: 2, deleted: 0 })
    expect(limited.deletes).toEqual([8])
    const forbidden = registryFixture({
      pages,
      deleteStatuses: { 8: 403 },
    })
    await expect(prune(forbidden)).rejects.toThrow('403')
    expect(forbidden.deletes).toEqual([8])
  })
})
