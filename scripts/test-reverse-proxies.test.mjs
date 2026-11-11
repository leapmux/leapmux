import { spawnSync } from 'node:child_process'
import { join } from 'node:path'
import process from 'node:process'
import { describe, expect, it } from 'bun:test'
import { EXAMPLES_DIR, PROXIES, ROOT } from './reverse-proxy-examples.mjs'
import { DockerSession } from './reverse-proxy-runtime.mjs'
import { parseArguments, ProxyEnvironment } from './test-reverse-proxies.mjs'

function runProxyCliWithoutDocker(args) {
  // Windows treats environment keys case-insensitively, so remove every casing.
  const childEnv = Object.fromEntries(
    Object.entries(process.env).filter(([key]) => key.toLowerCase() !== 'path'),
  )
  childEnv.PATH = ''
  return spawnSync(process.execPath, [
    join(ROOT, 'scripts', 'test-reverse-proxies.mjs'),
    ...args,
  ], {
    cwd: ROOT,
    encoding: 'utf8',
    // Docker must remain unavailable if argument validation regresses.
    env: childEnv,
    timeout: 30_000,
  })
}

function headerEnvironment() {
  const commands = new DockerSession(undefined, async () => {
    throw new Error('Unexpected Docker command')
  })
  const environment = new ProxyEnvironment(PROXIES[0], 'test-image', join(ROOT, '.tmp', 'unused-header-check'), {}, EXAMPLES_DIR, commands)
  environment.log = () => {}
  environment.clientAddress = '192.0.2.10'
  return environment
}

function forwardedHeaderFixture({ values, address, secure = true, sessionPresent = true, sameTimestamp = false } = {}) {
  const environment = headerEnvironment()
  const calls = []
  let logins = 0
  let listings = 0
  environment.signIn = async () => {
    calls.push('login')
    logins++
    return {
      cookie: logins === 1
        ? 'leapmux-session=first'
        : `${secure ? '__Host-' : ''}leapmux-session=current`,
      userId: 'admin',
    }
  }
  environment.rpc = async (method) => {
    calls.push(method)
    if (method === 'UserService/ElevateSession')
      return { body: {} }
    if (method === 'AdminSettingsService/UpdateSettings') {
      return {
        body: { values: values ?? [
          { key: 'trusted_proxy_ranges', effectiveJson: '["127.0.0.1","::1"]' },
        ] },
      }
    }
    if (method === 'AdminUserService/ListSessions') {
      listings++
      const current = {
        id: sessionPresent ? 'current' : 'another-session',
        createdAt: '2026-11-10T00:00:01Z',
        ipAddress: listings === 1
          ? (address ?? environment.clientAddress)
          : environment.clientAddress,
      }
      const previous = { id: 'first', createdAt: current.createdAt, ipAddress: '127.0.0.1' }
      return {
        body: {
          sessions: sameTimestamp && listings === 1
            ? [previous, current]
            : [current, previous],
        },
      }
    }
    throw new Error(`Unexpected procedure: ${method}`)
  }
  return { environment, calls }
}

function expectOneFreshLogin(calls) {
  expect(calls.filter(call => call === 'login')).toHaveLength(2)
  expect(calls.filter(call => call === 'AdminUserService/ListSessions')).toHaveLength(1)
  expect(calls).not.toContain('AdminSettingsService/ListSettings')
}

describe('parseArguments', () => {
  it('refuses an empty examples directory before starting Docker', () => {
    const result = runProxyCliWithoutDocker(['--examples-dir=', 'leapmux:test'])
    expect(result.error).toBeUndefined()
    expect(result.status).not.toBe(0)
    expect(result.stderr).toContain('--examples-dir requires a path.')
    expect(result.stdout).not.toContain('Testing ')
  })

  for (const duration of [
    '0',
    '-1',
    '1.5',
    '',
    '1801',
    '2147484',
    '9'.repeat(400),
  ]) {
    const label = duration.length > 20
      ? 'an overflowing value'
      : JSON.stringify(duration)
    it(`refuses the unsupported stability duration ${label} before starting Docker`, () => {
      const result = runProxyCliWithoutDocker([`--stability-seconds=${duration}`, 'leapmux:test'])
      expect(result.error).toBeUndefined()
      expect(result.status).not.toBe(0)
      expect(result.stderr).toContain('--stability-seconds must be an integer between 1 and 1800')
      expect(result.stdout).not.toContain('Testing ')
    })
  }

  it('accepts the minimum, maximum, default, and leading-zero durations', () => {
    expect(parseArguments(['leapmux:test']).stabilitySeconds).toBe(200)
    for (const [raw, expected] of [
      ['1', 1],
      ['1800', 1800],
      ['0001', 1],
    ])
      expect(parseArguments(['leapmux:test', `--stability-seconds=${raw}`]).stabilitySeconds).toBe(expected)
  })
})

describe('ProxyEnvironment checkForwardedHeaders', () => {
  it('checks the effective setting and performs one fresh login', async () => {
    const { environment, calls } = forwardedHeaderFixture()
    const session = await environment.checkForwardedHeaders()
    expect(session.cookie).toBe('__Host-leapmux-session=current')
    expectOneFreshLogin(calls)
  })

  it('identifies the current session when creation timestamps are equal', async () => {
    const { environment, calls } = forwardedHeaderFixture({ sameTimestamp: true })
    await environment.checkForwardedHeaders()
    expectOneFreshLogin(calls)
  })

  it('fails an incorrect forwarded address without creating another session', async () => {
    const { environment, calls } = forwardedHeaderFixture({ address: '127.0.0.1' })
    await expect(environment.checkForwardedHeaders()).rejects.toThrow('client address')
    expectOneFreshLogin(calls)
  })

  for (const [label, values] of [
    ['missing', []],
    [
      'stale',
      [
        { key: 'trusted_proxy_ranges', effectiveJson: '[]' },
      ],
    ],
    [
      'malformed',
      [
        { key: 'trusted_proxy_ranges', effectiveJson: '{' },
      ],
    ],
  ]) {
    it(`fails a ${label} effective setting before another login`, async () => {
      const { environment, calls } = forwardedHeaderFixture({ values })
      await expect(environment.checkForwardedHeaders()).rejects.toThrow('trusted_proxy_ranges')
      expect(calls.filter(call => call === 'login')).toHaveLength(1)
      expect(calls).not.toContain('AdminUserService/ListSessions')
    })
  }

  it('fails when the new session is absent from the session listing', async () => {
    const { environment, calls } = forwardedHeaderFixture({ sessionPresent: false })
    await expect(environment.checkForwardedHeaders()).rejects.toThrow('new session')
    expectOneFreshLogin(calls)
  })

  it('fails incorrect HTTPS forwarding without another login', async () => {
    const { environment, calls } = forwardedHeaderFixture({ secure: false })
    await expect(environment.checkForwardedHeaders()).rejects.toThrow('HTTPS through X-Forwarded-Proto')
    expectOneFreshLogin(calls)
  })
})

describe('ProxyEnvironment signIn', () => {
  for (const [label, body] of [
    ['missing report', {}],
    [
      'missing user',
      { sessionReport: {} },
    ],
    [
      'empty user ID',
      {
        sessionReport: {
          user: { id: '' },
        },
      },
    ],
  ]) {
    it(`reports a login with ${label}`, async () => {
      const environment = headerEnvironment()
      environment.rpc = async () => ({ body, sessionCookie: 'leapmux-session=session' })
      await expect(environment.signIn()).rejects.toThrow('user identity for the proxy check')
    })
  }
})
