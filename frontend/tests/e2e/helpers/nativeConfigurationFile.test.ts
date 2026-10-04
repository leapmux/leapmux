import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { withNativeConfigurationFile } from './nativeConfigurationFile'

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
