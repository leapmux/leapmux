import { chmodSync, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { delimiter, join, resolve } from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'
import { afterEach, describe, expect, it } from 'bun:test'
import { goMetadata, resolveGoIdentity } from './renovate-exemptions.mjs'
import { scratchDirectories } from './test-support.mjs'

const scratch = scratchDirectories()
const servers = []
afterEach(() => {
  for (const server of servers.splice(0)) server.stop(true)
  scratch.cleanup()
})

function fixture() {
  const directory = scratch.create('renovate-exemptions-integration')
  return directory
}

// The module proxy layout of renovate-post-upgrade.integration.test.mjs. `go list -m`
// reads only the .info and .mod files, so the fixture writes no module zip.
function writeProxy(directory, modules) {
  for (const identity of modules) {
    const [path, version] = identity.split('@')
    const files = resolve(directory, path, '@v')
    mkdirSync(files, { recursive: true })
    writeFileSync(resolve(files, `${version}.mod`), `module ${path}\n`)
    writeFileSync(resolve(files, `${version}.info`), JSON.stringify({ Version: version, Time: '2026-10-20T00:00:00Z' }))
  }
  return directory
}

function goEnvironment(directory, extra = {}) {
  return {
    ...process.env,
    GOSUMDB: 'off',
    GONOPROXY: '',
    GONOSUMDB: '',
    GOPRIVATE: '',
    GOFLAGS: '-modcacherw',
    GOMODCACHE: resolve(directory, 'module-cache'),
    ...extra,
  }
}

// Serve a module proxy from a directory. A path in `answers` gets that status and body,
// as proxy.golang.org states the reason for an absent version in a 404 body.
function httpProxy(directory, answers = {}) {
  const requests = []
  const server = Bun.serve({
    port: 0,
    hostname: '127.0.0.1',
    fetch(request) {
      const path = decodeURIComponent(new URL(request.url).pathname).slice(1)
      requests.push(path)
      if (answers[path])
        return new Response(answers[path].body ?? '', { status: answers[path].status })
      const file = resolve(directory, path)
      return file.startsWith(directory) && existsSync(file) && !path.endsWith('/')
        ? new Response(readFileSync(file))
        : new Response(`not found: ${path}`, { status: 404 })
    },
  })
  servers.push(server)
  return { url: `http://127.0.0.1:${server.port}`, port: server.port, requests }
}

describe('goMetadata with a real go list and a file proxy', () => {
  it('reports a published version as found and a missing version as not found', async () => {
    const directory = fixture()
    const proxy = writeProxy(resolve(directory, 'proxy'), ['example.com/lib@v1.2.3'])
    const env = goEnvironment(directory, { GOPROXY: pathToFileURL(proxy).href })
    const found = await goMetadata('example.com/lib', 'v1.2.3', { env })
    expect(found.status).toBe('found')
    expect(found.info.Version).toBe('v1.2.3')
    const missing = await goMetadata('example.com/lib', 'v1.2.4', { env })
    expect(missing.status).toBe('not-found')
    expect(missing.message).toContain('no such file or directory')
  })
})

describe('goMetadata with a real go list and an HTTP proxy', () => {
  it('reports 404 and 410 answers as not found', async () => {
    const directory = fixture()
    const proxy = httpProxy(writeProxy(resolve(directory, 'proxy'), []), {
      'example.com/lib/@v/v1.2.5.info': { status: 410, body: 'gone' },
    })
    const env = goEnvironment(directory, { GOPROXY: proxy.url })
    const absent = await goMetadata('example.com/lib', 'v1.2.4', { env })
    expect(absent.status).toBe('not-found')
    expect(absent.message).toContain('404 Not Found')
    const gone = await goMetadata('example.com/lib', 'v1.2.5', { env })
    expect(gone.status).toBe('not-found')
    expect(gone.message).toContain('410 Gone')
  })
  it('reports a proxy refusal of +incompatible on a major-suffixed path as not found', async () => {
    const directory = fixture()
    const proxy = httpProxy(writeProxy(resolve(directory, 'proxy'), []), {
      'example.com/lib/v4/@v/v4.1.6+incompatible.info': {
        status: 404,
        body: 'not found: example.com/lib/v4@v4.1.6+incompatible: invalid version: +incompatible suffix not allowed: major version v4 is compatible',
      },
    })
    const result = await goMetadata('example.com/lib/v4', 'v4.1.6+incompatible', {
      env: goEnvironment(directory, { GOPROXY: proxy.url }),
    })
    expect(result.status).toBe('not-found')
    expect(result.message).toContain('+incompatible suffix not allowed')
  })
  it('reports server and authentication failures as errors', async () => {
    const directory = fixture()
    const proxy = httpProxy(writeProxy(resolve(directory, 'proxy'), []), {
      'example.com/lib/@v/v1.0.0.info': { status: 500, body: 'broken' },
      'example.com/lib/@v/v1.0.1.info': { status: 401, body: 'unauthorized' },
      'example.com/lib/@v/v1.0.2.info': { status: 403, body: 'forbidden' },
    })
    const env = goEnvironment(directory, { GOPROXY: proxy.url, GOAUTH: 'off' })
    for (const [version, text] of [
      ['v1.0.0', '500'],
      ['v1.0.1', '401'],
      ['v1.0.2', '403'],
    ]) {
      const result = await goMetadata('example.com/lib', version, { env })
      expect(result.status, result.message).toBe('error')
      expect(result.message).toContain(text)
    }
  })
  it('reports an HTTPS proxy URL that reaches a plain HTTP server as an error', async () => {
    const directory = fixture()
    const proxy = httpProxy(writeProxy(resolve(directory, 'proxy'), ['example.com/lib@v1.2.3']))
    const result = await goMetadata('example.com/lib', 'v1.2.3', {
      env: goEnvironment(directory, { GOPROXY: `https://127.0.0.1:${proxy.port}` }),
    })
    expect(result.status).toBe('error')
    expect(result.message).toContain('server gave HTTP response to HTTPS client')
  })
})

describe('resolveGoIdentity with a real go list', () => {
  it('stops at an error on the first candidate and never queries the second one', async () => {
    const directory = fixture()
    const proxy = httpProxy(writeProxy(resolve(directory, 'proxy'), ['example.com/lib@v2.0.0+incompatible']), {
      'example.com/lib/v2/@v/v2.0.0.info': { status: 500, body: 'broken' },
    })
    const env = goEnvironment(directory, { GOPROXY: proxy.url })
    await expect(resolveGoIdentity('example.com/lib', '2.0.0', {
      goMetadata: (module, version) => goMetadata(module, version, { env }),
    })).rejects.toThrow('Cannot read Go module example.com/lib/v2@v2.0.0')
    expect(proxy.requests).toContain('example.com/lib/v2/@v/v2.0.0.info')
    expect(proxy.requests.some(path => path.startsWith('example.com/lib/@v/'))).toBe(false)
  })
  it('falls back from an absent major-suffixed module to the published legacy version', async () => {
    const directory = fixture()
    const proxy = httpProxy(writeProxy(resolve(directory, 'proxy'), ['example.com/legacy@v2.0.0+incompatible']))
    const env = goEnvironment(directory, { GOPROXY: proxy.url })
    expect(await resolveGoIdentity('example.com/legacy', '2.0.0', {
      goMetadata: (module, version) => goMetadata(module, version, { env }),
    })).toEqual({ name: 'example.com/legacy', version: 'v2.0.0+incompatible' })
    expect(proxy.requests).toContain('example.com/legacy/v2/@v/v2.0.0.info')
  })
})

// The fake git is a POSIX shell script. Renovate and this check run on Linux.
describe.skipIf(process.platform === 'win32')('the invalid version regression with a direct VCS lookup', () => {
  it('reports a transport failure that Go wraps in invalid version as an error and stops the search', async () => {
    const directory = fixture()
    const bin = resolve(directory, 'bin')
    mkdirSync(bin)
    const log = resolve(directory, 'git.log')
    writeFileSync(join(bin, 'git'), `#!/bin/sh
printf '%s %s\\n' "$FAKE_GIT_QUERY" "$*" >> ${JSON.stringify(log)}
case " $* " in
  *" ls-remote "*)
    echo "fatal: unable to access 'https://github.com/leapmux-test/lib/': TLS handshake timeout" >&2
    exit 128 ;;
  " version ") echo "git version 2.50.0" ;;
esac
exit 0
`)
    chmodSync(join(bin, 'git'), 0o755)
    const env = goEnvironment(directory, {
      PATH: `${bin}${delimiter}${process.env.PATH}`,
      GOPROXY: 'direct',
      GIT_TERMINAL_PROMPT: '0',
    })
    const read = (module, version) => goMetadata(module, version, {
      env: { ...env, FAKE_GIT_QUERY: `${module}@${version}` },
    })
    const result = await read('github.com/leapmux-test/lib', 'v1.0.0')
    expect(result.message).toContain('invalid version')
    expect(result.message).toContain('TLS handshake timeout')
    expect(result.status).toBe('error')
    rmSync(log, { force: true })
    await expect(resolveGoIdentity('github.com/leapmux-test/lib', '2.0.0', { goMetadata: read })).rejects.toThrow('TLS handshake timeout')
    const queries = new Set(readFileSync(log, 'utf8').trim().split('\n').map(line => line.split(' ')[0]))
    expect(queries).toEqual(new Set(['github.com/leapmux-test/lib/v2@v2.0.0']))
  })
})
