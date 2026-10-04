import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { basename, dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createNativeToolDirectory } from './nativeToolDirectory'
import { quotePosixShellArgument } from './shellArguments'

const scratchRoot = resolve(process.cwd(), '../.tmp')
let directory: string
beforeEach(() => {
  mkdirSync(scratchRoot, { recursive: true })
  directory = mkdtempSync(join(scratchRoot, 'native-tool-directory-unit-'))
})
afterEach(() => rmSync(directory, { recursive: true, force: true }))

describe('createNativeToolDirectory', () => {
  it('creates distinct private directories and preserves literal shell metacharacters', () => {
    const first = createNativeToolDirectory(directory)
    const second = createNativeToolDirectory(directory)
    expect(first).not.toBe(second)
    expect(dirname(first)).toBe(directory)
    expect(dirname(second)).toBe(directory)
    expect(basename(first)).toMatch(/^native path \$\(touch command-expanded-marker\) ; & ' `-/)
    expect(readdirSync(directory).sort()).toEqual([basename(first), basename(second)].sort())
  })

  it.runIf(existsSync('/bin/sh'))('writes and reads actual bytes without expanding its directory', () => {
    const target = join(createNativeToolDirectory(directory), 'original file.txt')
    execFileSync('/bin/sh', ['-c', `printf 'native-%s\\n' "$((40 + 2))" > ${quotePosixShellArgument(target)}; cat ${quotePosixShellArgument(target)}`], { cwd: directory })
    expect(readFileSync(target, 'utf8')).toBe('native-42\n')
    expect(existsSync(join(directory, 'command-expanded-marker'))).toBe(false)
    expect(readdirSync(directory)).toEqual([basename(dirname(target))])
  })

  it.each(['', '.', '..', 'relative/path', '/invalid\0path'])('rejects a nonabsolute or invalid working directory %j', (workingDir) => {
    expect(() => createNativeToolDirectory(workingDir)).toThrow('absolute private working directory')
    expect(readdirSync(directory)).toEqual([])
  })

  it('rejects an absent working directory without creating it', () => {
    const absent = join(directory, 'absent')
    expect(() => createNativeToolDirectory(absent)).toThrow()
    expect(existsSync(absent)).toBe(false)
  })

  it('rejects a regular file without changing its bytes', () => {
    const file = join(directory, 'file')
    writeFileSync(file, 'unchanged')
    expect(() => createNativeToolDirectory(file)).toThrow('real directory without a symlink')
    expect(readFileSync(file, 'utf8')).toBe('unchanged')
  })

  it('rejects a directory symlink rather than creating files through it', () => {
    const target = join(directory, 'target')
    const link = join(directory, 'link')
    mkdirSync(target)
    symlinkSync(resolve(target), link, 'junction')
    expect(() => createNativeToolDirectory(link)).toThrow('real directory without a symlink')
    expect(readdirSync(target)).toEqual([])
  })
})
