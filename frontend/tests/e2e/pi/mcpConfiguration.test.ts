import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { writePiMcpConfiguration } from './mcpConfiguration'

let runDir: string
let outside: string
beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  runDir = mkdtempSync(join(scratch, 'pi-native-config-'))
  outside = mkdtempSync(join(scratch, 'pi-outside-config-'))
})
afterEach(() => {
  rmSync(runDir, { recursive: true, force: true })
  rmSync(outside, { recursive: true, force: true })
})

describe('writePiMcpConfiguration', () => {
  it('writes native direct exposure and preserves exact arguments', () => {
    const args = ['private server.mjs', '', '출력']
    const path = writePiMcpConfiguration(runDir, runDir, { 'probe-one': { command: process.execPath, args } })
    expect(path).toBe(join(runDir, '.pi', 'mcp.json'))
    expect(JSON.parse(readFileSync(path, 'utf8'))).toEqual({ mcpServers: { 'probe-one': { command: process.execPath, args, exposure: 'direct' } } })
    expect(existsSync(join(runDir, '.mcp.json'))).toBe(false)
  })

  it.each(['', '../outside', 'probe.dot'])('refuses an invalid server name before writing: %s', (name) => {
    expect(() => writePiMcpConfiguration(runDir, runDir, { [name]: { command: process.execPath, args: [] } })).toThrow('valid server name')
    expect(existsSync(join(runDir, '.pi'))).toBe(false)
  })

  it('refuses an empty catalog and a relative command before writing', () => {
    expect(() => writePiMcpConfiguration(runDir, runDir, {})).toThrow('requires a server')
    expect(() => writePiMcpConfiguration(runDir, runDir, { probe: { command: 'node', args: [] } })).toThrow('absolute command')
    expect(existsSync(join(runDir, '.pi'))).toBe(false)
  })

  it('refuses an outside directory and a project symlink before writing', () => {
    expect(() => writePiMcpConfiguration(outside, runDir, { probe: { command: process.execPath, args: [] } })).toThrow('outside the E2E run')
    symlinkSync(outside, join(runDir, '.pi'), 'dir')
    expect(() => writePiMcpConfiguration(runDir, runDir, { probe: { command: process.execPath, args: [] } })).toThrow('outside the E2E run')
    expect(existsSync(join(outside, 'mcp.json'))).toBe(false)
  })

  it('refuses a broken configuration symlink before it creates an outside file', () => {
    const project = join(runDir, '.pi')
    mkdirSync(project)
    const target = join(outside, 'absent-mcp.json')
    symlinkSync(target, join(project, 'mcp.json'))
    expect(() => writePiMcpConfiguration(runDir, runDir, { probe: { command: process.execPath, args: [] } })).toThrow('The private native path must not be a symbolic link, which could point outside the E2E run.')
    expect(existsSync(target)).toBe(false)
  })
})
