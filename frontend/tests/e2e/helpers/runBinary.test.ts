import { chmodSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, renameSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { copyRunBinary, LEAPMUX_BINARY_NAME, runBinaryPath } from './runBinary'

let directory: string
let buildOutput: string
let runDir: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'run-binary-'))
  buildOutput = join(directory, LEAPMUX_BINARY_NAME)
  writeFileSync(buildOutput, 'first build')
  runDir = join(directory, 'run')
  mkdirSync(runDir)
})

afterEach(() => rmSync(directory, { recursive: true, force: true }))

describe('LEAPMUX_BINARY_NAME', () => {
  it('carries the executable suffix of the platform', () => {
    expect(LEAPMUX_BINARY_NAME).toBe(process.platform === 'win32' ? 'leapmux.exe' : 'leapmux')
  })
})

describe('runBinaryPath', () => {
  it('places the binary directly in the run directory', () => {
    expect(runBinaryPath(runDir)).toBe(join(runDir, LEAPMUX_BINARY_NAME))
  })
})

describe('copyRunBinary', () => {
  it('copies the build output to the run binary path', () => {
    expect(copyRunBinary(buildOutput, runDir)).toBe(runBinaryPath(runDir))
    expect(readFileSync(runBinaryPath(runDir), 'utf8')).toBe('first build')
  })

  it('keeps the copy when a rebuild replaces the build output with a new file', () => {
    copyRunBinary(buildOutput, runDir)
    const replacement = join(directory, 'replacement')
    writeFileSync(replacement, 'second build')
    renameSync(replacement, buildOutput)
    expect(readFileSync(runBinaryPath(runDir), 'utf8')).toBe('first build')
  })

  it('keeps the copy when a rebuild rewrites the build output in place', () => {
    // A hard link or a symbolic link passes the test above and fails this one.
    copyRunBinary(buildOutput, runDir)
    writeFileSync(buildOutput, 'second build')
    expect(readFileSync(runBinaryPath(runDir), 'utf8')).toBe('first build')
  })

  it('keeps the copy when the build output is deleted', () => {
    copyRunBinary(buildOutput, runDir)
    rmSync(buildOutput)
    expect(readFileSync(runBinaryPath(runDir), 'utf8')).toBe('first build')
  })

  it.skipIf(process.platform === 'win32')('keeps the executable permission of the build output', () => {
    chmodSync(buildOutput, 0o755)
    copyRunBinary(buildOutput, runDir)
    expect(statSync(runBinaryPath(runDir)).mode & 0o777).toBe(0o755)
  })

  it('copies an empty build output', () => {
    writeFileSync(buildOutput, '')
    copyRunBinary(buildOutput, runDir)
    expect(readFileSync(runBinaryPath(runDir), 'utf8')).toBe('')
  })

  it('refuses to replace a binary that the run directory already holds', () => {
    copyRunBinary(buildOutput, runDir)
    writeFileSync(buildOutput, 'second build')
    expect(() => copyRunBinary(buildOutput, runDir)).toThrow(expect.objectContaining({ code: 'EEXIST' }))
    expect(readFileSync(runBinaryPath(runDir), 'utf8')).toBe('first build')
  })

  it('fails without a partial copy when the build output is absent', () => {
    rmSync(buildOutput)
    expect(() => copyRunBinary(buildOutput, runDir)).toThrow(expect.objectContaining({ code: 'ENOENT' }))
    expect(existsSync(runBinaryPath(runDir))).toBe(false)
    expect(readdirSync(runDir)).toEqual([])
  })

  it('fails when the run directory is absent', () => {
    rmSync(runDir, { recursive: true })
    expect(() => copyRunBinary(buildOutput, runDir)).toThrow(expect.objectContaining({ code: 'ENOENT' }))
  })
})
