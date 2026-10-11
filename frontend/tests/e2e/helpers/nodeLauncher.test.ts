import { spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { writeNodeLauncher } from './nodeLauncher'

let directory: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
  mkdirSync(scratch, { recursive: true })
  // A space in the path proves that the launcher quotes each path.
  directory = mkdtempSync(join(scratch, 'node launcher-'))
})

afterEach(() => rmSync(directory, { recursive: true, force: true }))

function script(): string {
  const path = join(directory, 'probe script.cjs')
  writeFileSync(path, 'process.stdout.write(JSON.stringify({ argv: process.argv.slice(2), pid: process.pid })); process.exitCode = 7', { mode: 0o600 })
  return path
}

describe('writeNodeLauncher', () => {
  it.skipIf(process.platform === 'win32')('runs the script through Node with every argument unchanged and its exit code', () => {
    const launcher = writeNodeLauncher(directory, 'native-probe', { node: process.execPath, script: script() })
    expect(launcher).toBe(join(directory, 'native-probe'))
    expect(statSync(launcher).mode & 0o777).toBe(0o700)
    const result = spawnSync(launcher, ['--flag', 'quoted 한글 argument', '$HOME', ''], { encoding: 'utf8' })
    expect(result.error).toBeUndefined()
    expect(result.status).toBe(7)
    const output = JSON.parse(result.stdout) as { argv: string[], pid: number }
    expect(output.argv).toEqual(['--flag', 'quoted 한글 argument', '$HOME', ''])
    // `exec` replaces the shell, so the launched process is Node itself.
    expect(output.pid).toBe(result.pid)
  })

  it.skipIf(process.platform !== 'win32')('writes a command file that passes every argument to Node', () => {
    const scriptPath = script()
    const launcher = writeNodeLauncher(directory, 'native-probe', { node: process.execPath, script: scriptPath })
    expect(launcher).toBe(join(directory, 'native-probe.cmd'))
    expect(readFileSync(launcher, 'utf8')).toBe(`@"${process.execPath}" "${scriptPath}" %*\r\n`)
  })

  it.skipIf(process.platform === 'win32')('replaces a launcher that exists, and restores its private mode', () => {
    const path = join(directory, 'native-probe')
    writeFileSync(path, 'stale', { mode: 0o755 })
    writeNodeLauncher(directory, 'native-probe', { node: process.execPath, script: script() })
    expect(readFileSync(path, 'utf8')).toContain('exec ')
    expect(statSync(path).mode & 0o777).toBe(0o700)
  })

  it.each(['', '.', '..', 'bin/native', 'native\0'])('refuses the name %j, which is not one file-name component', (name) => {
    expect(() => writeNodeLauncher(directory, name, { node: process.execPath, script: script() })).toThrow('one file-name component')
  })

  it.each([
    ['directory', { directory: 'relative', node: process.execPath, script: '/absolute/script.cjs' }],
    ['Node executable', { directory: '/absolute', node: 'node', script: '/absolute/script.cjs' }],
    ['script', { directory: '/absolute', node: process.execPath, script: 'script.cjs' }],
  ])('refuses a relative %s', (_label, paths) => {
    expect(() => writeNodeLauncher(paths.directory, 'native-probe', { node: paths.node, script: paths.script })).toThrow('requires an absolute directory, Node executable, and script')
  })
})
