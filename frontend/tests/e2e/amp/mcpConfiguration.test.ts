import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { mcpProbeServer } from '../helpers/mcpProbeServer'
import { ampMcpSettings, ampSettingsPath } from './mcpConfiguration'

const scratchRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../.tmp')
let directory: string
beforeEach(() => {
  mkdirSync(scratchRoot, { recursive: true })
  directory = mkdtempSync(join(scratchRoot, 'amp-mcp-configuration-unit-'))
})
afterEach(() => rmSync(directory, { recursive: true, force: true }))

const server = mcpProbeServer('echo_probe', '/run/echo.mjs')

describe('ampSettingsPath', () => {
  it('reads the settings file of the private config home', () => {
    expect(ampSettingsPath({ XDG_CONFIG_HOME: '/run/config' })).toBe('/run/config/amp/settings.json')
  })

  it.each([undefined, {}, { XDG_CONFIG_HOME: '' }])('refuses an environment without a private config home: %j', (env) => {
    expect(() => ampSettingsPath(env)).toThrow('config home is unavailable')
  })
})

describe('ampMcpSettings', () => {
  it('keeps the other settings, and gives the test server as the only MCP server', () => {
    const path = join(directory, 'settings.json')
    writeFileSync(path, JSON.stringify({ 'amp.theme': 'dark', 'amp.mcpServers': { other: { command: 'other', args: [] } } }))
    expect(ampMcpSettings(path, server)).toEqual({
      'amp.theme': 'dark',
      'amp.mcpServers': { echo_probe: { command: process.execPath, args: ['/run/echo.mjs'] } },
    })
  })

  it('reads an absent file as no settings', () => {
    expect(ampMcpSettings(join(directory, 'absent.json'), server)).toEqual({
      'amp.mcpServers': { echo_probe: { command: process.execPath, args: ['/run/echo.mjs'] } },
    })
  })

  it.each(['[]', 'null', '"text"'])('refuses settings that are not a JSON object: %s', (content) => {
    const path = join(directory, 'settings.json')
    writeFileSync(path, content)
    expect(() => ampMcpSettings(path, server)).toThrow('must hold a JSON object')
  })

  it('refuses a server whose name a configuration cannot hold', () => {
    expect(() => ampMcpSettings(join(directory, 'absent.json'), { ...server, name: 'echo probe' })).toThrow('MCP server name')
  })
})
