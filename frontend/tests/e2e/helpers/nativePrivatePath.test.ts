import { mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { assertPrivateNativePath } from './nativePrivatePath'

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

  it('refuses a symlink inside the native run that resolves outside it', () => {
    const outside = join(scratch, 'outside-run')
    mkdirSync(outside)
    const link = join(runDir, 'native-home')
    symlinkSync(outside, link, 'junction')
    expect(() => assertPrivateNativePath(link, runDir)).toThrow('outside the E2E run')
  })

  it('resolves a directory link before the following parent segment', () => {
    const outside = join(scratch, 'outside-run')
    const child = join(outside, 'child')
    mkdirSync(child, { recursive: true })
    writeFileSync(join(outside, 'settings.json'), 'outside bytes')
    writeFileSync(join(runDir, 'settings.json'), 'private bytes')
    const link = join(runDir, 'linked-parent')
    symlinkSync(child, link, 'junction')
    const path = `${link}${sep}..${sep}settings.json`
    const actualText = readFileSync(path, 'utf8')
    if (actualText === 'outside bytes') {
      expect(() => assertPrivateNativePath(path, runDir)).toThrow('outside the E2E run')
    }
    else {
      expect(actualText).toBe('private bytes')
      expect(() => assertPrivateNativePath(path, runDir)).not.toThrow()
    }
  })

  it('refuses a lexical path outside the private run', () => {
    expect(() => assertPrivateNativePath(scratch, runDir)).toThrow('outside the E2E run')
  })

  it.each([{ path: '', run: 'run' }, { path: 'path', run: '' }])('refuses an empty private path or run directory: %j', ({ path, run }) => {
    expect(() => assertPrivateNativePath(path, run)).toThrow('must be nonempty')
  })

  it('states a private path that does not exist, in place of a raw lstat error', () => {
    const absent = join(runDir, 'agent-home', '.claude')
    expect(() => assertPrivateNativePath(absent, runDir)).toThrow(`The private native path ${absent} does not exist.`)
  })

  it('states a run directory that does not exist', () => {
    const absentRun = join(scratch, 'absent-run')
    expect(() => assertPrivateNativePath(runDir, absentRun)).toThrow(`The E2E run directory ${absentRun} does not exist.`)
  })

  it('states a link whose target does not exist', () => {
    const link = join(runDir, 'dangling')
    symlinkSync(join(runDir, 'absent-target'), link, 'junction')
    expect(() => assertPrivateNativePath(link, runDir)).toThrow(`The private native path ${link} does not exist.`)
  })
})
