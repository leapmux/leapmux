import { spawnSync } from 'node:child_process'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { delimiter, join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'

const project = resolve(import.meta.dirname, '../../..')
const directories = []

function runImage(extra = {}) {
  const root = mkdtempSync(join(project, '.tmp', 'dmg-test-'))
  directories.push(root)
  const bin = join(root, 'bin')
  mkdirSync(bin)
  const app = join(root, 'Application with spaces.app')
  mkdirSync(app)
  const output = join(root, 'result.dmg')
  writeFileSync(output, 'previous image')
  const driver = join(root, 'tool.mjs')
  writeFileSync(driver, `
import { appendFileSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
const root = process.env.DMG_TEST_ROOT
const [tool, ...args] = process.argv.slice(2)
const log = join(root, 'calls')
appendFileSync(log, JSON.stringify([tool, ...args]) + '\\n')
if (tool === 'mktemp') {
  console.log(mkdtempSync(join(root, 'stage-')))
} else if (tool === 'du') {
  console.log('1\\t' + args.at(-1))
} else if (tool === 'hdiutil') {
  if (args[0] === 'attach') {
    const index = args.indexOf('-mountpoint')
    const mount = index < 0 ? '/Volumes/LeapMux Desktop test' : args[index + 1]
    if (mount.startsWith(root)) mkdirSync(mount, { recursive: true })
    console.log('/dev/fake\\tApple_HFS\\t' + mount)
  }
  if (args[0] === 'detach' && process.env.DMG_FAIL === 'detach') process.exit(19)
  if (args[0] === 'detach' && !args.includes('-force') && process.env.DMG_BUSY === 'always') process.exit(16)
  if (args[0] === 'detach' && !args.includes('-force') && process.env.DMG_FAIL === 'detach-once') process.exit(19)
  if (args[0] === 'detach' && process.env.DMG_BUSY === '1') {
    const calls = readFileSync(log, 'utf8').trim().split('\\n').map(line => JSON.parse(line))
    if (calls.filter(call => call[0] === 'hdiutil' && call[1] === 'detach').length === 1) process.exit(16)
  }
  if (args[0] === 'convert') {
    if (process.env.DMG_FAIL === 'convert') process.exit(17)
    writeFileSync(args[args.indexOf('-o') + 1], 'new image')
  }
} else if (tool === 'node' && process.env.DMG_FAIL === 'metadata') {
  process.exit(18)
}
`)
  for (const tool of ['hdiutil', 'mktemp', 'du', 'cp', 'ln', 'node', 'chmod', 'sync', 'sleep', 'find']) {
    const path = join(bin, tool)
    writeFileSync(path, `#!/bin/sh\nexec "$DMG_BUN" "$DMG_DRIVER" ${tool} "$@"\n`)
    chmodSync(path, 0o755)
  }
  const result = spawnSync('bash', [join(import.meta.dirname, 'create-dmg.sh'), 'test', app, output], {
    encoding: 'utf8',
    env: { ...process.env, PATH: `${bin}${delimiter}${process.env.PATH}`, DMG_TEST_ROOT: root, DMG_BUN: process.execPath, DMG_DRIVER: driver, ...extra },
  })
  const calls = readFileSync(join(root, 'calls'), 'utf8').trim().split('\n').map(line => JSON.parse(line))
  return { root, app, output, calls, result }
}

afterEach(() => {
  for (const directory of directories.splice(0))
    rmSync(directory, { recursive: true, force: true })
})

describe.skipIf(process.platform === 'win32')('disk image assembly', () => {
  it('copies the app once and touches only its own mount', () => {
    const { root, app, calls, result } = runImage()
    expect(result.status, result.stderr).toBe(0)
    expect(calls.filter(call => call[0] === 'find')).toEqual([])
    expect(calls.filter(call => call[0] === 'cp' && call.includes(app))).toHaveLength(1)
    const attach = calls.find(call => call[0] === 'hdiutil' && call[1] === 'attach')
    expect(attach).toContain('-mountpoint')
    const mount = attach[attach.indexOf('-mountpoint') + 1]
    expect(mount.startsWith(root)).toBe(true)
    expect(calls.filter(call => call[0] === 'hdiutil' && call[1] === 'detach').every(call => call.includes(mount))).toBe(true)
    expect(calls.filter(call => call[0] === 'sleep')).toEqual([])
    expect(readFileSync(join(root, 'result.dmg'), 'utf8')).toBe('new image')
    expect(readdirSync(root).filter(name => name.startsWith('stage-'))).toEqual([])
  })

  it('preserves a previous image after conversion fails', () => {
    const { output, result } = runImage({ DMG_FAIL: 'convert' })
    expect(result.status).not.toBe(0)
    expect(readFileSync(output, 'utf8')).toBe('previous image')
  })

  it('detaches its mount after metadata generation fails', () => {
    const { calls, result } = runImage({ DMG_FAIL: 'metadata' })
    expect(result.status).not.toBe(0)
    const attach = calls.find(call => call[0] === 'hdiutil' && call[1] === 'attach')
    const mount = attach[attach.indexOf('-mountpoint') + 1]
    expect(calls.some(call => call[0] === 'hdiutil' && call[1] === 'detach' && call.includes(mount))).toBe(true)
  })

  it('waits only after an actual busy detach', () => {
    const { calls, result } = runImage({ DMG_BUSY: '1' })
    expect(result.status, result.stderr).toBe(0)
    expect(calls.filter(call => call[0] === 'sleep')).toEqual([['sleep', '1']])
  })

  it('forces its own busy mount after five attempts', () => {
    const { root, calls, result } = runImage({ DMG_BUSY: 'always' })
    expect(result.status, result.stderr).toBe(0)
    const detach = calls.filter(call => call[0] === 'hdiutil' && call[1] === 'detach')
    expect(detach).toHaveLength(6)
    expect(detach.slice(0, 5).every(call => !call.includes('-force'))).toBe(true)
    expect(detach[5]).toContain('-force')
    expect(new Set(detach.map(call => call[2])).size).toBe(1)
    expect(detach[5][2].startsWith(root)).toBe(true)
    expect(calls.filter(call => call[0] === 'sleep')).toEqual([1, 2, 3, 4].map(delay => ['sleep', String(delay)]))
  })

  it('does not retry a detach error other than resource busy', () => {
    const { root, output, calls, result } = runImage({ DMG_FAIL: 'detach-once' })
    expect(result.status).toBe(19)
    expect(calls.filter(call => call[0] === 'sleep')).toEqual([])
    expect(calls.some(call => call[0] === 'hdiutil' && call[1] === 'convert')).toBe(false)
    expect(readFileSync(output, 'utf8')).toBe('previous image')
    expect(readdirSync(root).filter(name => name.startsWith('stage-'))).toEqual([])
  })

  it('preserves staging and the previous artifact when cleanup cannot detach the mount', () => {
    const { root, output, calls, result } = runImage({ DMG_FAIL: 'detach' })
    expect(result.status).not.toBe(0)
    expect(result.stderr).toContain('Staging remains intact')
    expect(calls.some(call => call[0] === 'hdiutil' && call[1] === 'convert')).toBe(false)
    expect(readFileSync(output, 'utf8')).toBe('previous image')
    const staging = readdirSync(root).filter(name => name.startsWith('stage-'))
    expect(staging).toHaveLength(1)
    expect(existsSync(join(root, staging[0], 'volume'))).toBe(true)
  })
})
