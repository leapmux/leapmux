import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { assertPrivateNativeAncestor, withNativeConfigurationFile } from './nativeConfigurationFile'

const scratchRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../.tmp')
let runDir: string
beforeEach(() => {
  mkdirSync(scratchRoot, { recursive: true })
  runDir = mkdtempSync(join(scratchRoot, 'native-config-file-unit-'))
})
afterEach(() => rmSync(runDir, { recursive: true, force: true }))

describe('withNativeConfigurationFile', () => {
  it('rejects an outside path before creating its parent directory', async () => {
    const outside = mkdtempSync(join(scratchRoot, 'outside-native-config-unit-'))
    const parent = join(outside, 'must-not-exist')
    const use = vi.fn(async () => {})
    try {
      await expect(withNativeConfigurationFile({ path: join(parent, 'config.json'), content: '{}', runDir }, use))
        .rejects
        .toThrow('outside the E2E run')
      expect(existsSync(parent)).toBe(false)
      expect(use).not.toHaveBeenCalled()
    }
    finally {
      rmSync(outside, { recursive: true, force: true })
    }
  })

  it('rejects a parent symlink before creating a directory in its outside target', async () => {
    const outside = mkdtempSync(join(scratchRoot, 'outside-native-config-link-'))
    const linked = join(runDir, 'linked-parent')
    symlinkSync(outside, linked, 'dir')
    const parent = join(outside, 'must-not-exist')
    const use = vi.fn(async () => {})
    try {
      await expect(withNativeConfigurationFile({ path: join(linked, 'must-not-exist', 'config.json'), content: '{}', runDir }, use))
        .rejects
        .toThrow('outside the E2E run')
      expect(existsSync(parent)).toBe(false)
      expect(use).not.toHaveBeenCalled()
    }
    finally {
      rmSync(outside, { recursive: true, force: true })
    }
  })

  it('rejects a broken link at the path before the write creates its outside target', async () => {
    const outside = mkdtempSync(join(scratchRoot, 'outside-native-config-broken-'))
    const target = join(outside, 'missing.json')
    const path = join(runDir, 'config.json')
    symlinkSync(target, path)
    const use = vi.fn(async () => {})
    try {
      await expect(withNativeConfigurationFile({ path, content: '{}', runDir }, use)).rejects.toThrow(/^The private native path .+ does not exist\.$/)
      expect(existsSync(target)).toBe(false)
      expect(use).not.toHaveBeenCalled()
    }
    finally {
      rmSync(outside, { recursive: true, force: true })
    }
  })

  it('rejects a link at the path whose target lies outside the run', async () => {
    const outside = mkdtempSync(join(scratchRoot, 'outside-native-config-file-'))
    const target = join(outside, 'config.json')
    writeFileSync(target, 'outside bytes')
    const path = join(runDir, 'config.json')
    symlinkSync(target, path)
    const use = vi.fn(async () => {})
    try {
      await expect(withNativeConfigurationFile({ path, content: '{}', runDir }, use)).rejects.toThrow('outside the E2E run')
      expect(readFileSync(target, 'utf8')).toBe('outside bytes')
      expect(use).not.toHaveBeenCalled()
    }
    finally {
      rmSync(outside, { recursive: true, force: true })
    }
  })

  it('restores the original exact bytes after native use', async () => {
    const path = join(runDir, 'config.json')
    const original = '{"original":true}\n'
    writeFileSync(path, original)
    await withNativeConfigurationFile({ path, content: '{"native":true}', runDir }, async () => {
      expect(readFileSync(path, 'utf8')).toBe('{"native":true}')
    })
    expect(readFileSync(path, 'utf8')).toBe(original)
  })

  it('restores a file after the native operation fails', async () => {
    const path = join(runDir, 'config.json')
    writeFileSync(path, 'original bytes')
    await expect(withNativeConfigurationFile({ path, content: 'temporary bytes', runDir }, async () => {
      throw new Error('Native fixture operation failed.')
    })).rejects.toThrow('Native fixture operation failed.')
    expect(readFileSync(path, 'utf8')).toBe('original bytes')
  })

  it('removes a temporary native file that did not exist before use', async () => {
    const path = join(runDir, 'native', 'config.json')
    await withNativeConfigurationFile({ path, content: 'temporary bytes', runDir }, async () => {
      expect(existsSync(path)).toBe(true)
    })
    expect(existsSync(path)).toBe(false)
  })
})

describe('assertPrivateNativeAncestor', () => {
  let outside: string
  beforeEach(() => {
    outside = mkdtempSync(join(scratchRoot, 'outside-native-ancestor-unit-'))
  })
  afterEach(() => rmSync(outside, { recursive: true, force: true }))

  it('accepts a missing file below the private run, and returns its nearest existing ancestor', () => {
    const path = join(runDir, '.pi', 'extensions', 'codemode.ts')
    expect(assertPrivateNativeAncestor(path, runDir)).toBe(runDir)
    expect(assertPrivateNativeAncestor(path, runDir, { refuseSymlink: true })).toBe(runDir)
  })

  it('accepts an existing regular file', () => {
    const existing = join(runDir, 'settings.json')
    writeFileSync(existing, '{}')
    expect(assertPrivateNativeAncestor(existing, runDir, { refuseSymlink: true })).toBe(existing)
  })

  it.each(['', 'relative/config.json', 'relative', '\0', '/absolute\0/config.json'])('refuses an absent, relative, or NUL path: %j', (path) => {
    expect(() => assertPrivateNativeAncestor(path, runDir)).toThrow('absolute path')
  })

  it('refuses an outside existing path and an outside missing path', () => {
    expect(() => assertPrivateNativeAncestor(outside, runDir)).toThrow('outside the E2E run')
    expect(() => assertPrivateNativeAncestor(join(outside, 'missing.json'), runDir)).toThrow('outside the E2E run')
  })

  it('accepts a link to a target inside the run unless the caller refuses links', () => {
    const target = join(runDir, 'settings.json')
    writeFileSync(target, '{}')
    const linked = join(runDir, 'linked.json')
    symlinkSync(target, linked)
    expect(assertPrivateNativeAncestor(linked, runDir)).toBe(linked)
    expect(() => assertPrivateNativeAncestor(linked, runDir, { refuseSymlink: true })).toThrow('symbolic link')
  })

  it('refuses a link to an outside target, and a broken link when the caller refuses links', () => {
    const existing = join(outside, 'settings.json')
    writeFileSync(existing, '{}')
    const linked = join(runDir, 'linked.json')
    symlinkSync(existing, linked)
    expect(() => assertPrivateNativeAncestor(linked, runDir)).toThrow('outside the E2E run')
    expect(() => assertPrivateNativeAncestor(linked, runDir, { refuseSymlink: true })).toThrow('symbolic link')
    const broken = join(runDir, 'broken.json')
    symlinkSync(join(outside, 'missing.json'), broken)
    expect(() => assertPrivateNativeAncestor(broken, runDir, { refuseSymlink: true })).toThrow('symbolic link')
  })

  it('refuses an outside parent link for an existing child file', () => {
    writeFileSync(join(outside, 'settings.json'), '{}')
    const linked = join(runDir, 'linked-parent')
    symlinkSync(outside, linked, 'dir')
    expect(() => assertPrivateNativeAncestor(join(linked, 'settings.json'), runDir, { refuseSymlink: true })).toThrow('outside the E2E run')
  })
})
