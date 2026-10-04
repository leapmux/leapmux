import { mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { assertPrivateNativePath } from './nativeCredentialIsolation'

const scratchRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../.tmp')
let scratch: string
let runDir: string

beforeEach(() => {
  mkdirSync(scratchRoot, { recursive: true })
  scratch = mkdtempSync(join(scratchRoot, 'native-private-path-unit-'))
  runDir = join(scratch, 'private-run')
  mkdirSync(runDir)
})

afterEach(() => rmSync(scratch, { recursive: true, force: true }))

describe('assertPrivateNativePath', () => {
  it('accepts actual private files and directories with spaces', () => {
    const directory = join(runDir, 'native configuration')
    mkdirSync(directory)
    const file = join(directory, 'models.json')
    writeFileSync(file, '{}')
    expect(() => assertPrivateNativePath(directory, runDir)).not.toThrow()
    expect(() => assertPrivateNativePath(file, runDir)).not.toThrow()
  })

  it('refuses a private-looking symlink that resolves outside the native run', () => {
    const outside = join(scratch, 'outside-run')
    mkdirSync(outside)
    const link = join(runDir, 'native-home')
    symlinkSync(outside, link, 'junction')
    expect(() => assertPrivateNativePath(link, runDir)).toThrow('outside the E2E run')
  })

  it('refuses a lexical path outside the private run', () => {
    expect(() => assertPrivateNativePath(scratch, runDir)).toThrow('outside the E2E run')
  })

  it.each([{ path: '', run: 'run' }, { path: 'path', run: '' }])('refuses an empty private path or run directory: %j', ({ path, run }) => {
    expect(() => assertPrivateNativePath(path, run)).toThrow('must be nonempty')
  })
})
