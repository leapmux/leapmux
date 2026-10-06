import { existsSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createTestDirectory, isFileNameComponent } from './runDirectory'

let runDir: string
vi.mock('./server', () => ({ getGlobalState: () => ({ tmpDir: runDir }) }))

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
  mkdirSync(scratch, { recursive: true })
  runDir = mkdtempSync(join(scratch, 'run-directory-'))
})

afterEach(() => rmSync(runDir, { recursive: true, force: true }))

describe('run-owned directories', () => {
  it('creates distinct directories below the current run', () => {
    const first = createTestDirectory('agent-')
    const second = createTestDirectory('agent-')
    expect(first).not.toBe(second)
    expect(dirname(first)).toBe(runDir)
    expect(dirname(second)).toBe(runDir)
    expect(existsSync(first)).toBe(true)
    expect(existsSync(second)).toBe(true)
  })

  it.each(['', '.', '..', '../outside', '/outside', 'folder\\outside', 'agent\0-'])('rejects a prefix that is not one filename component: %j', (prefix) => {
    expect(() => createTestDirectory(prefix)).toThrow('one filename component')
  })
})

describe('isFileNameComponent', () => {
  it.each(['agent', 'agent-', '.hidden', 'a.b', '...', 'name with space', 'é'])('accepts one file-name component: %j', (value) => {
    expect(isFileNameComponent(value)).toBe(true)
  })

  it.each([
    { value: '', why: 'an empty name' },
    { value: '.', why: 'the current directory' },
    { value: '..', why: 'the parent directory' },
    { value: 'a/b', why: 'a POSIX separator' },
    { value: 'a/', why: 'a trailing POSIX separator' },
    { value: '/', why: 'a root' },
    { value: 'a\\b', why: 'a Windows separator' },
    { value: 'a\0b', why: 'a NUL' },
    { value: '\0', why: 'a NUL alone' },
  ])('refuses $why: $value', ({ value }) => {
    expect(isFileNameComponent(value)).toBe(false)
  })
})
