import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { runInNewContext } from 'node:vm'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { activateNativeCodemode } from './codemodeConfiguration'

let directory: string
let outside: string
beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'native-codemode-config-'))
  outside = mkdtempSync(join(scratch, 'native-codemode-outside-'))
})
afterEach(() => {
  rmSync(directory, { recursive: true, force: true })
  rmSync(outside, { recursive: true, force: true })
})

describe('activateNativeCodemode', () => {
  it('activates the existing executor after session start and retains native tools', () => {
    const path = activateNativeCodemode(directory, directory)
    const source = readFileSync(path, 'utf8')
    const activate = runInNewContext(`(${source.replace('export default ', '')})`)
    const callbacks = new Map<string, () => void>()
    const setActiveTools = vi.fn()
    activate({ on: (event: string, callback: () => void) => callbacks.set(event, callback), getActiveTools: () => ['read', 'mcp__probe__inspect'], setActiveTools })
    expect(setActiveTools).not.toHaveBeenCalled()
    expect([...callbacks.keys()]).toEqual(['session_start'])
    callbacks.get('session_start')?.()
    expect(setActiveTools).toHaveBeenCalledExactlyOnceWith(['read', 'mcp__probe__inspect', 'codemode'])
    expect(source).not.toContain('registerTool')
  })

  it('rejects an outside project before it writes the extension', () => {
    expect(() => activateNativeCodemode(outside, directory)).toThrow('outside the E2E run')
    expect(existsSync(join(outside, '.pi'))).toBe(false)
  })

  it('rejects an extension-directory symlink before it writes outside the run', () => {
    mkdirSync(join(directory, '.pi'))
    symlinkSync(outside, join(directory, '.pi', 'extensions'), 'dir')
    expect(() => activateNativeCodemode(directory, directory)).toThrow('outside the E2E run')
    expect(existsSync(join(outside, 'native-codemode.ts'))).toBe(false)
  })

  it('rejects an existing extension-file symlink before it changes its target', () => {
    const extensions = join(directory, '.pi', 'extensions')
    mkdirSync(extensions, { recursive: true })
    const target = join(outside, 'native-codemode.ts')
    writeFileSync(target, 'PRIVATE_TARGET_UNCHANGED')
    symlinkSync(target, join(extensions, 'native-codemode.ts'))
    expect(() => activateNativeCodemode(directory, directory)).toThrow('outside the E2E run')
    expect(readFileSync(target, 'utf8')).toBe('PRIVATE_TARGET_UNCHANGED')
  })

  it('rejects a broken extension-file symlink before it creates its outside target', () => {
    const extensions = join(directory, '.pi', 'extensions')
    mkdirSync(extensions, { recursive: true })
    const target = join(outside, 'absent-native-codemode.ts')
    symlinkSync(target, join(extensions, 'native-codemode.ts'))
    expect(() => activateNativeCodemode(directory, directory)).toThrow()
    expect(existsSync(target)).toBe(false)
  })
})
