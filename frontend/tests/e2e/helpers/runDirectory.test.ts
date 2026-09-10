import { existsSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createTestDirectory } from './runDirectory'

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

  it.each(['', '.', '..', '../outside', '/outside', 'folder\\outside'])('rejects a prefix that is not one filename component: %s', (prefix) => {
    expect(() => createTestDirectory(prefix)).toThrow('one filename component')
  })
})
