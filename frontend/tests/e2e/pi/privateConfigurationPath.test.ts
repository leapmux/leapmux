import { mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { assertPiConfigurationPath } from './privateConfigurationPath'

let directory: string
let outside: string
beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'pi-config-path-'))
  outside = mkdtempSync(join(scratch, 'pi-config-path-outside-'))
})
afterEach(() => {
  rmSync(directory, { recursive: true, force: true })
  rmSync(outside, { recursive: true, force: true })
})

describe('assertPiConfigurationPath', () => {
  it('accepts a missing file below the actual private run and an existing regular file', () => {
    const path = join(directory, '.pi', 'extensions', 'codemode.ts')
    expect(() => assertPiConfigurationPath(path, directory)).not.toThrow()
    const existing = join(directory, 'settings.json')
    writeFileSync(existing, '{}')
    expect(() => assertPiConfigurationPath(existing, directory)).not.toThrow()
  })

  it.each(['', 'relative/config.json', 'relative', '\0'])('rejects an absent or relative path: %j', (path) => {
    expect(() => assertPiConfigurationPath(path, directory)).toThrow('absolute path')
  })

  it('rejects an outside existing path and an outside missing path', () => {
    expect(() => assertPiConfigurationPath(outside, directory)).toThrow('outside the E2E run')
    expect(() => assertPiConfigurationPath(join(outside, 'missing.json'), directory)).toThrow('outside the E2E run')
  })

  it('rejects both valid and broken symbolic links before writing', () => {
    const existing = join(outside, 'settings.json')
    writeFileSync(existing, '{}')
    const linked = join(directory, 'linked.json')
    symlinkSync(existing, linked)
    expect(() => assertPiConfigurationPath(linked, directory)).toThrow('symbolic link')
    const broken = join(directory, 'broken.json')
    symlinkSync(join(outside, 'missing.json'), broken)
    expect(() => assertPiConfigurationPath(broken, directory)).toThrow('symbolic link')
  })

  it('rejects an outside parent link for an existing child file', () => {
    writeFileSync(join(outside, 'settings.json'), '{}')
    const linked = join(directory, 'linked-parent')
    symlinkSync(outside, linked, 'dir')
    expect(() => assertPiConfigurationPath(join(linked, 'settings.json'), directory)).toThrow('outside the E2E run')
  })
})
