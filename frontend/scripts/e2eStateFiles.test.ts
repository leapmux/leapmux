import { chmodSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import { basename, dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { absoluteDestination, readOptionalStateFile, writeFileAtomically } from './e2eStateFiles'

const roots: string[] = []
afterEach(() => {
  for (const root of roots.splice(0))
    rmSync(root, { recursive: true, force: true })
})

function destination(): string {
  const scratch = resolve(import.meta.dirname, '../../.tmp')
  mkdirSync(scratch, { recursive: true })
  const root = mkdtempSync(join(scratch, 'e2e-state-file-test-'))
  roots.push(root)
  return join(root, 'state', 'value.json')
}

describe('absoluteDestination', () => {
  it('returns an absolute path unchanged', () => {
    const path = resolve('state.json')
    expect(absoluteDestination(path, 'state')).toBe(path)
  })

  it.each([undefined, 7, '', 'relative.json', `${resolve('state')}\0suffix`])('rejects an invalid destination with its description: %j', (value) => {
    expect(() => absoluteDestination(value, 'example destination')).toThrow('The example destination must be an absolute path without NUL characters.')
  })
})

describe('writeFileAtomically', () => {
  it('creates the parent directory and writes the exact content', () => {
    const path = destination()

    writeFileAtomically(path, 'first\nsecond\n')

    expect(readFileSync(path, 'utf8')).toBe('first\nsecond\n')
    expect(readdirSync(dirname(path))).toEqual(['value.json'])
  })

  it('keeps the preceding content complete until the draft is renamed', () => {
    const path = destination()
    writeFileAtomically(path, 'prior')
    const observed: string[] = []

    writeFileAtomically(path, 'next', {
      mkdirSync,
      writeFileSync: (draft, content, options) => {
        if (typeof draft !== 'string')
          throw new Error('The state draft is not a file path.')
        observed.push(draft)
        expect(dirname(draft)).toBe(dirname(path))
        expect(basename(draft)).toMatch(/^\.leapmux-[0-9a-f-]+\.writing$/u)
        expect(options).toEqual({ encoding: 'utf8', flag: 'wx', mode: 0o600 })
        expect(readFileSync(path, 'utf8')).toBe('prior')
        writeFileSync(draft, content, options)
      },
      renameSync: (draft, target) => {
        expect(target).toBe(path)
        expect(readFileSync(path, 'utf8')).toBe('prior')
        expect(readFileSync(draft, 'utf8')).toBe('next')
        renameSync(draft, target)
      },
      rmSync,
    })

    expect(observed).toHaveLength(1)
    expect(readFileSync(path, 'utf8')).toBe('next')
    expect(readdirSync(dirname(path))).toEqual(['value.json'])
  })

  it('removes a partial draft after a write failure and preserves the preceding content', () => {
    const path = destination()
    writeFileAtomically(path, 'prior')
    const failure = new Error('The state write failed.')

    expect(() => writeFileAtomically(path, 'next', {
      mkdirSync,
      writeFileSync: (draft) => {
        writeFileSync(draft, 'partial')
        throw failure
      },
      renameSync,
      rmSync,
    })).toThrow(failure)

    expect(readFileSync(path, 'utf8')).toBe('prior')
    expect(readdirSync(dirname(path))).toEqual(['value.json'])
  })

  it('removes the complete draft after a rename failure and preserves the preceding content', () => {
    const path = destination()
    writeFileAtomically(path, 'prior')
    const failure = new Error('The state rename failed.')

    expect(() => writeFileAtomically(path, 'next', {
      mkdirSync,
      writeFileSync,
      renameSync: () => {
        throw failure
      },
      rmSync,
    })).toThrow(failure)

    expect(readFileSync(path, 'utf8')).toBe('prior')
    expect(readdirSync(dirname(path))).toEqual(['value.json'])
  })

  it('reports both write and draft-cleanup failures', () => {
    const path = destination()
    const writeFailure = new Error('The state write failed.')
    const cleanupFailure = new Error('The state draft cleanup failed.')
    let observed: unknown
    try {
      writeFileAtomically(path, 'next', {
        mkdirSync,
        writeFileSync: () => {
          throw writeFailure
        },
        renameSync,
        rmSync: () => {
          throw cleanupFailure
        },
      })
    }
    catch (error) {
      observed = error
    }

    expect(observed).toBeInstanceOf(AggregateError)
    if (!(observed instanceof AggregateError))
      throw new Error('The state writer discarded one of its failures.')
    expect(observed.errors).toEqual([writeFailure, cleanupFailure])
  })

  it('propagates a directory failure before creating a draft', () => {
    const path = destination()
    const failure = new Error('The state directory cannot be created.')
    const write = vi.fn<typeof writeFileSync>()

    expect(() => writeFileAtomically(path, 'next', {
      mkdirSync: () => {
        throw failure
      },
      writeFileSync: write,
      renameSync,
      rmSync,
    })).toThrow(failure)
    expect(write).not.toHaveBeenCalled()
  })

  it('rejects a relative destination before accessing the filesystem', () => {
    const mkdir = vi.fn<typeof mkdirSync>()

    expect(() => writeFileAtomically('relative.json', 'next', { mkdirSync: mkdir, writeFileSync, renameSync, rmSync }))
      .toThrow('absolute path without NUL')
    expect(mkdir).not.toHaveBeenCalled()
  })
})

describe('readOptionalStateFile', () => {
  it('returns the exact content of an existing file', () => {
    const path = destination()
    writeFileAtomically(path, '{"value":1}\n')

    expect(readOptionalStateFile(path)).toBe('{"value":1}\n')
  })

  it('returns undefined only for an absent file', () => {
    expect(readOptionalStateFile(destination())).toBeUndefined()
  })

  it('throws a read error other than an absent file', () => {
    const path = destination()
    mkdirSync(path, { recursive: true })

    expect(() => readOptionalStateFile(path)).toThrow(expect.objectContaining({ code: 'EISDIR' }))
  })

  it.skipIf(process.platform === 'win32' || process.getuid?.() === 0)('throws a permission error instead of reporting an absent file', () => {
    const path = destination()
    writeFileAtomically(path, 'private')
    chmodSync(path, 0o000)
    try {
      expect(() => readOptionalStateFile(path)).toThrow(expect.objectContaining({ code: 'EACCES' }))
    }
    finally {
      chmodSync(path, 0o600)
    }
  })
})
