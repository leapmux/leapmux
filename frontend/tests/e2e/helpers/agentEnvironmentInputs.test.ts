import { describe, expect, it } from 'vitest'
import { LOOPBACK_HOSTNAMES, requireLoopbackHttpURL, validatedMcpServers } from './agentEnvironmentInputs'

describe('requireLoopbackHttpURL', () => {
  it.each(['http://127.0.0.1:4567', 'http://localhost:4567/v1', 'http://[::1]:4567/'])('accepts the loopback URL %s', (value) => {
    expect(requireLoopbackHttpURL(value, 'The endpoint').href).toBe(new URL(value).href)
  })

  it.each([
    'https://127.0.0.1:4567',
    'http://example.com:4567',
    'http://10.0.0.1:4567',
    'http://user:secret@127.0.0.1:4567',
    'http://user@localhost:4567',
    'http://127.0.0.1:4567/?query=1',
    'http://127.0.0.1:4567/#fragment',
    'not a URL',
    '',
  ])('refuses %j', (value) => {
    expect(() => requireLoopbackHttpURL(value, 'The endpoint')).toThrow('The endpoint must be a loopback HTTP URL.')
  })

  it('keeps the parse failure of an invalid URL as the cause', () => {
    expect(() => requireLoopbackHttpURL('not a URL', 'The endpoint')).toThrow(expect.objectContaining({ cause: expect.any(TypeError) }))
  })

  it('accepts a bare origin and refuses a path when the caller needs an origin', () => {
    expect(requireLoopbackHttpURL('http://localhost:4321', 'The origin', { originOnly: true }).origin).toBe('http://localhost:4321')
    expect(requireLoopbackHttpURL('http://[::1]:4321/', 'The origin', { originOnly: true }).pathname).toBe('/')
    expect(() => requireLoopbackHttpURL('http://localhost:4321/v1', 'The origin', { originOnly: true })).toThrow('The origin must be a loopback HTTP origin.')
  })
})

describe('LOOPBACK_HOSTNAMES', () => {
  it('spells each host name as URL.hostname reports it', () => {
    for (const value of ['http://127.0.0.1', 'http://localhost', 'http://[::1]'])
      expect(LOOPBACK_HOSTNAMES.has(new URL(value).hostname)).toBe(true)
  })
})

describe('validatedMcpServers', () => {
  it('returns copies of the servers in their order', () => {
    const args = ['/srv/echo.mjs']
    const servers = validatedMcpServers([{ name: 'echo-probe', command: '/usr/bin/node', args }, { name: 'form_probe', command: '/usr/bin/node', args: [] }], 'Agent', 32)
    expect(servers).toEqual([{ name: 'echo-probe', command: '/usr/bin/node', args }, { name: 'form_probe', command: '/usr/bin/node', args: [] }])
    expect(servers[0]!.args).not.toBe(args)
  })

  it('returns no server for an absent or empty list', () => {
    expect(validatedMcpServers(undefined, 'Agent', 32)).toEqual([])
    expect(validatedMcpServers([], 'Agent', 32)).toEqual([])
  })

  it('applies the name limit that the caller states', () => {
    const name = 'n'.repeat(40)
    expect(() => validatedMcpServers([{ name, command: '/bin/server', args: [] }], 'Agent', 32)).toThrow('The Agent MCP server names must be valid and distinct.')
    expect(validatedMcpServers([{ name, command: '/bin/server', args: [] }], 'Agent', 64)).toHaveLength(1)
  })

  it.each([
    { label: 'an empty name', servers: [{ name: '', command: '/bin/server', args: [] }] },
    { label: 'a name with a space', servers: [{ name: 'echo probe', command: '/bin/server', args: [] }] },
    { label: 'a name with a dot', servers: [{ name: 'echo.probe', command: '/bin/server', args: [] }] },
    { label: 'a duplicate name', servers: [{ name: 'echo', command: '/bin/a', args: [] }, { name: 'echo', command: '/bin/b', args: [] }] },
  ])('refuses $label', ({ servers }) => {
    expect(() => validatedMcpServers(servers, 'Agent', 32)).toThrow('The Agent MCP server names must be valid and distinct.')
  })

  it('refuses a command that is not absolute', () => {
    expect(() => validatedMcpServers([{ name: 'echo', command: 'node', args: [] }], 'Agent', 32)).toThrow('The Agent MCP command must be absolute.')
  })

  it.each([0, -1, 1.5, Number.NaN])('refuses the name limit %s', (limit) => {
    expect(() => validatedMcpServers([], 'Agent', limit)).toThrow('The Agent MCP server name limit must be a positive integer.')
  })
})
