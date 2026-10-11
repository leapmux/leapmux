import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { mcpProbeServer } from '../helpers/mcpProbeServer'
import { writeKiroProjectMcpServers } from './mcpConfiguration'

const scratchRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../.tmp')
let workingDir: string
beforeEach(() => {
  mkdirSync(scratchRoot, { recursive: true })
  workingDir = mkdtempSync(join(scratchRoot, 'kiro-mcp-configuration-unit-'))
})
afterEach(() => rmSync(workingDir, { recursive: true, force: true }))

describe('writeKiroProjectMcpServers', () => {
  it('writes each server under its name into the project settings of the working directory', () => {
    const echo = mcpProbeServer('echo_probe', '/run/echo.mjs')
    const form = mcpProbeServer('probe', '/run/form.mjs')
    const path = writeKiroProjectMcpServers(workingDir, echo, form)
    expect(path).toBe(join(workingDir, '.kiro', 'settings', 'mcp.json'))
    expect(JSON.parse(readFileSync(path, 'utf8'))).toEqual({
      mcpServers: {
        echo_probe: { command: process.execPath, args: ['/run/echo.mjs'] },
        probe: { command: process.execPath, args: ['/run/form.mjs'] },
      },
    })
  })

  it('refuses an empty server list and writes no settings directory', () => {
    expect(() => writeKiroProjectMcpServers(workingDir)).toThrow('at least one server')
    expect(existsSync(join(workingDir, '.kiro'))).toBe(false)
  })

  it('refuses two servers with one name and writes no settings directory', () => {
    const server = mcpProbeServer('probe', '/run/form.mjs')
    expect(() => writeKiroProjectMcpServers(workingDir, server, server)).toThrow('two servers named probe')
    expect(existsSync(join(workingDir, '.kiro'))).toBe(false)
  })
})
