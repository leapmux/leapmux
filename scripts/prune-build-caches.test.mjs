import { spawn } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import { createReplacementPlan, pruneBuildCaches, runCacheReplacement, selectArchivesToReplace } from './prune-build-caches.mjs'

const root = resolve(import.meta.dirname, '..')
const prefix = 'docker-go-build-arm64-go1.27.1-'
const hash = 'a'.repeat(64)
const key = `${prefix}${hash}-2027-W01`
const scope = { key, prefix, repository: 'leapmux/leapmux', ref: 'refs/heads/main', eventName: 'push' }
const registered = new Date('2027-01-04T12:00:00.500Z')
const plan = createReplacementPlan(scope, registered)
const fixtures = []
const servers = []

afterEach(() => {
  for (const server of servers.splice(0)) server.stop(true)
  for (const dir of fixtures.splice(0)) rmSync(dir, { recursive: true, force: true })
})

function archive(id, changes = {}) {
  return { id, key: `${prefix}${hash}-2026-W53`, ref: scope.ref, created_at: '2026-12-28T00:00:00Z', ...changes }
}

function fakeApi(initial, { failPage, malformedPage, deleteStatus, apiPath = '' } = {}) {
  const caches = new Map(initial.map(entry => [entry.id, entry]))
  const requests = []
  const server = Bun.serve({
    hostname: '127.0.0.1',
    port: 0,
    fetch(request) {
      const url = new URL(request.url)
      requests.push({ method: request.method, path: url.pathname, query: Object.fromEntries(url.searchParams), authorization: request.headers.get('authorization') })
      if (url.pathname === `${apiPath}/repos/leapmux/leapmux/actions/caches` && request.method === 'GET') {
        const page = Number(url.searchParams.get('page'))
        if (page === failPage)
          return new Response('fixture failure', { status: 500 })
        if (page === malformedPage)
          return Response.json({ actions_caches: [archive(9999, { id: 'invalid' })] })
        const entries = [...caches.values()]
        return Response.json({ total_count: entries.length, actions_caches: entries.slice((page - 1) * 100, page * 100) })
      }
      const match = /\/actions\/caches\/(\d+)$/.exec(url.pathname)
      if (match && request.method === 'DELETE') {
        if (deleteStatus) {
          if (deleteStatus === 404)
            caches.delete(Number(match[1]))
          return new Response(null, { status: deleteStatus })
        }
        const removed = caches.delete(Number(match[1]))
        return new Response(null, { status: removed ? 204 : 404 })
      }
      return new Response('unexpected request', { status: 400 })
    },
  })
  servers.push(server)
  return { caches, requests, apiUrl: `http://127.0.0.1:${server.port}${apiPath}` }
}

function runNode(script, env) {
  return new Promise((resolveResult, reject) => {
    const child = spawn('node', [join(root, script)], {
      cwd: root,
      env: { ...process.env, ...env },
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    let stdout = ''
    let stderr = ''
    child.stdout.on('data', chunk => stdout += chunk)
    child.stderr.on('data', chunk => stderr += chunk)
    child.on('error', reject)
    child.on('close', code => resolveResult({ code, stdout, stderr }))
  })
}

describe('createReplacementPlan', () => {
  it('accepts exact native and Docker partitions without retaining credentials', () => {
    for (const partition of [prefix, 'docker-go-build-amd64-go1.27.1-', 'go-build-Linux-X64-ubuntu24-go1.27.1-', 'go-build-Windows-X64-win22-go1.27.1-', 'go-build-macOS-ARM64-macos26-arm64-go1.27.1-']) {
      const value = createReplacementPlan({ ...scope, prefix: partition, key: `${partition}${hash}-2027-W01`, token: 'must not persist' }, registered)
      expect(value.startedAt).toBe(registered.toISOString())
      expect(JSON.stringify(value)).not.toContain('must not persist')
    }
  })

  it('refuses broad partitions and keys outside the exact partition', () => {
    for (const changes of [
      { prefix: '' },
      { prefix: 'go-build-' },
      { prefix: 'docker-go-build-' },
      { prefix: 'bun-packages-Linux-' },
      { key: `${prefix}${hash}` },
      { key: `${prefix}${hash}-2027-W00` },
      { key: `${prefix}${hash}-2027-W54` },
      { key: `${prefix}${hash}-2027-W53` },
      { key: `${prefix}${hash}-2027-W01\n` },
      { key: `other-${hash}-2027-W01` },
      { repository: '../leapmux' },
      { repository: 'leapmux/..' },
    ]) {
      expect(() => createReplacementPlan({ ...scope, ...changes }, registered)).toThrow()
    }
  })

  it('permits only an exact main-branch push or manual release', () => {
    for (const ref of ['refs/heads/main', 'refs/heads/Main', 'refs/heads/main-feature', 'refs/pull/1/merge', 'refs/tags/main', '']) {
      for (const eventName of ['push', 'workflow_dispatch', 'pull_request', 'pull_request_target', '']) {
        const create = () => createReplacementPlan({ ...scope, ref, eventName }, registered)
        if (ref === scope.ref && ['push', 'workflow_dispatch'].includes(eventName))
          expect(create).not.toThrow()
        else expect(create).toThrow()
      }
    }
  })
})

describe('selectArchivesToReplace', () => {
  it('preserves the current key and every unrelated or newer archive', () => {
    const caches = [
      archive(1),
      archive(2, { key: `${prefix}${hash}` }),
      archive(3, { key: `${prefix}${'b'.repeat(64)}-2027-W01` }),
      archive(4, { key }),
      archive(5, { key: `${prefix}${hash}-2027-W02` }),
      archive(6, { ref: 'refs/pull/1/merge' }),
      archive(7, { ref: 'refs/heads/Main' }),
      archive(8, { key: `go-modules-Linux-${hash}` }),
      archive(9, { key: `docker-go-build-amd64-go1.27.1-${hash}-2026-W53` }),
      archive(10, { key: `${prefix}unknown-content` }),
      archive(11, { created_at: '2027-01-04T12:00:00Z' }),
      archive(12, { created_at: '2027-01-04T12:00:01Z' }),
      archive(13, { key: `${prefix}${hash}-2027-W54` }),
    ]
    expect(selectArchivesToReplace(plan, caches).map(entry => entry.id)).toEqual([1, 2, 3])
    expect(selectArchivesToReplace(plan, [])).toEqual([])
  })

  it('preserves an invalid week even when its year precedes the replacement', () => {
    const later = createReplacementPlan({ ...scope, key: `${prefix}${hash}-2028-W01` }, new Date('2028-01-04T00:00:00Z'))
    expect(selectArchivesToReplace(later, [archive(1, { key: `${prefix}${hash}-2027-W53` })])).toEqual([])
    expect(() => createReplacementPlan({ ...scope, key: `${prefix}${hash}-2026-W53` }, registered)).not.toThrow()
  })

  it('checks week 53 for ordinary and leap years', () => {
    for (const year of [2015, 2020, 2026]) {
      expect(() => createReplacementPlan({ ...scope, key: `${prefix}${hash}-${year}-W53` }, registered)).not.toThrow()
    }
    for (const year of [1800, 2014, 2021, 2027]) {
      expect(() => createReplacementPlan({ ...scope, key: `${prefix}${hash}-${year}-W53` }, registered)).toThrow()
    }
  })

  it('rejects malformed records before selecting any deletion', () => {
    for (const changes of [{ id: 0 }, { id: -1 }, { id: Number.MAX_SAFE_INTEGER + 1 }, { key: null }, { ref: null }, { created_at: 'not a date' }, { created_at: '2027-02-31T00:00:00Z' }]) {
      expect(() => selectArchivesToReplace(plan, [archive(1), archive(2, changes)])).toThrow()
    }
    expect(() => selectArchivesToReplace(plan, [archive(1), archive(1)])).toThrow()
    expect(() => selectArchivesToReplace(plan, null)).toThrow()
  })
})

describe('pruneBuildCaches', () => {
  it('reads every page before deleting and preserves current data', async () => {
    const api = fakeApi([...Array.from({ length: 203 }, (_, index) => archive(index + 1)), archive(204, { key })], { apiPath: '/api/v3' })
    const removed = await pruneBuildCaches(plan, { token: 'fixture-token', apiUrl: api.apiUrl })
    expect(removed).toBe(203)
    expect([...api.caches.keys()]).toEqual([204])
    expect(api.requests.slice(0, 3).map(request => request.method)).toEqual(['GET', 'GET', 'GET'])
    expect(api.requests.slice(3).every(request => request.method === 'DELETE')).toBe(true)
    for (const request of api.requests.slice(0, 3)) {
      expect(request.query).toMatchObject({ ref: scope.ref, key: prefix, per_page: '100', sort: 'created_at', direction: 'asc' })
      expect(request.authorization).toBe('Bearer fixture-token')
    }
  })

  it('makes no deletion after a later page failure or malformed entry', async () => {
    for (const failure of [{ failPage: 2 }, { malformedPage: 2 }]) {
      const api = fakeApi(Array.from({ length: 101 }, (_, index) => archive(index + 1)), failure)
      await expect(pruneBuildCaches(plan, { token: 'fixture-token', apiUrl: api.apiUrl })).rejects.toThrow()
      expect(api.requests.map(request => request.method)).toEqual(['GET', 'GET'])
      expect(api.caches.size).toBe(101)
    }
  })

  it('accepts an already removed archive but stops on other deletion failures', async () => {
    for (const status of [404, 403, 500]) {
      const api = fakeApi([archive(1), archive(2)], { deleteStatus: status })
      const operation = pruneBuildCaches(plan, { token: 'fixture-token', apiUrl: api.apiUrl })
      if (status === 404)
        await expect(operation).resolves.toBe(0)
      else await expect(operation).rejects.toThrow(`HTTP ${status}`)
      expect(api.requests.filter(request => request.method === 'DELETE')).toHaveLength(status === 404 ? 2 : 1)
    }
  })

  it('does not delete on an empty list or a failed network request', async () => {
    const api = fakeApi([])
    expect(await pruneBuildCaches(plan, { token: 'fixture-token', apiUrl: api.apiUrl })).toBe(0)
    expect(api.requests).toHaveLength(1)
    const refuseNetwork = async () => {
      throw new Error('fixture network failure')
    }
    await expect(pruneBuildCaches(plan, { token: 'fixture-token', fetchImpl: refuseNetwork })).rejects.toThrow('request failed')
    await expect(pruneBuildCaches(plan, { token: '', fetchImpl: refuseNetwork })).rejects.toThrow('job token')
  })

  it('refuses invalid JSON and repeated pages without deleting anything', async () => {
    let calls = 0
    const repeated = Array.from({ length: 100 }, (_, index) => archive(index + 1))
    await expect(pruneBuildCaches(plan, { token: 'fixture-token', fetchImpl: async (_, options) => {
      expect(options.method).toBe('GET')
      expect(options.redirect).toBe('error')
      calls++
      return Response.json({ actions_caches: repeated })
    } })).rejects.toThrow('repeated an ID')
    expect(calls).toBe(2)
    await expect(pruneBuildCaches(plan, { token: 'fixture-token', fetchImpl: async () => new Response('not JSON') })).rejects.toThrow('not valid JSON')
  })
})

describe('cache replacement entrypoints', () => {
  it('preserves state and makes no API request when registration fails', async () => {
    mkdirSync(join(root, '.tmp'), { recursive: true })
    const dir = mkdtempSync(join(root, '.tmp', 'cache-registration-'))
    fixtures.push(dir)
    const statePath = join(dir, 'state')
    writeFileSync(statePath, 'existing-state=value\n')
    const api = fakeApi([archive(1)])
    const env = {
      GITHUB_ACTIONS: 'true',
      GITHUB_REF: scope.ref,
      GITHUB_REPOSITORY: scope.repository,
      GITHUB_EVENT_NAME: scope.eventName,
      GITHUB_STATE: statePath,
      GITHUB_API_URL: api.apiUrl,
      INPUT_KEY: key,
      INPUT_PREFIX: prefix,
      INPUT_TOKEN: 'fixture-token',
    }
    for (const change of [
      { GITHUB_ACTIONS: 'false' },
      { GITHUB_REF: 'refs/heads/Main' },
      { GITHUB_EVENT_NAME: 'pull_request' },
      { INPUT_PREFIX: 'docker-go-build-' },
      { INPUT_TOKEN: '' },
      { INPUT_TOKEN: ' \t' },
      { GITHUB_STATE: '' },
      { GITHUB_STATE: dir },
    ]) {
      const result = await runNode('scripts/register-build-cache-replacement.mjs', { ...env, ...change })
      expect(result.code).toBe(1)
      expect(result.stderr).toContain('Cache replacement registration failed:')
      expect(result.stderr).not.toContain(env.INPUT_TOKEN)
      expect(readFileSync(statePath, 'utf8')).toBe('existing-state=value\n')
      expect(api.requests).toHaveLength(0)
      expect([...api.caches.keys()]).toEqual([1])
    }
  })

  it('registers without API access and deletes only during the post entrypoint', async () => {
    mkdirSync(join(root, '.tmp'), { recursive: true })
    const dir = mkdtempSync(join(root, '.tmp', 'cache-replacement-'))
    fixtures.push(dir)
    const api = fakeApi([archive(1, { created_at: '2000-01-01T00:00:00Z' }), archive(2, { key })])
    const env = {
      GITHUB_ACTIONS: 'true',
      GITHUB_REF: scope.ref,
      GITHUB_REPOSITORY: scope.repository,
      GITHUB_EVENT_NAME: scope.eventName,
      GITHUB_STATE: join(dir, 'state'),
      GITHUB_API_URL: api.apiUrl,
      INPUT_KEY: key,
      INPUT_PREFIX: prefix,
      INPUT_TOKEN: 'fixture-token',
    }
    const registration = await runNode('scripts/register-build-cache-replacement.mjs', env)
    expect(registration.code, registration.stderr).toBe(0)
    expect(api.requests).toHaveLength(0)
    const state = readFileSync(env.GITHUB_STATE, 'utf8').trim().slice('CACHE_REPLACEMENT='.length)
    expect(state).not.toContain(env.INPUT_TOKEN)
    const saved = JSON.parse(state)
    api.caches.set(3, archive(3, { created_at: saved.startedAt }))
    const postEnv = { ...env, STATE_CACHE_REPLACEMENT: state }
    for (const change of [{ GITHUB_REF: 'refs/pull/1/merge' }, { GITHUB_EVENT_NAME: 'pull_request' }, { GITHUB_REPOSITORY: 'another/repo' }, { INPUT_KEY: `${prefix}${hash}-2026-W53` }, { STATE_CACHE_REPLACEMENT: '{' }]) {
      const refused = await runNode('scripts/prune-build-caches.mjs', { ...postEnv, ...change })
      expect(refused.code).not.toBe(0)
      expect(api.requests).toHaveLength(0)
    }
    await expect(runCacheReplacement(env)).rejects.toThrow('absent')
    const post = await runNode('scripts/prune-build-caches.mjs', postEnv)
    expect(post.code, post.stderr).toBe(0)
    expect([...api.caches.keys()]).toEqual([2, 3])
    expect(post.stdout).toContain('Removed 1')
  })

  it('returns a failed post status when deletion fails', async () => {
    const api = fakeApi([archive(1)], { deleteStatus: 403 })
    const result = await runNode('scripts/prune-build-caches.mjs', {
      GITHUB_ACTIONS: 'true',
      GITHUB_REF: scope.ref,
      GITHUB_REPOSITORY: scope.repository,
      GITHUB_EVENT_NAME: scope.eventName,
      GITHUB_API_URL: api.apiUrl,
      STATE_CACHE_REPLACEMENT: JSON.stringify(plan),
      INPUT_KEY: key,
      INPUT_PREFIX: prefix,
      INPUT_TOKEN: 'fixture-token',
    })
    expect(result.code).toBe(1)
    expect(result.stderr).toContain('HTTP 403')
    expect(api.caches.size).toBe(1)
  })
})
