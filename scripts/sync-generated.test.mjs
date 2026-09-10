import { spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'

const root = resolve(import.meta.dirname, '..')
const fixtures = []

function fixture() {
  mkdirSync(join(root, '.tmp'), { recursive: true })
  const dir = mkdtempSync(join(root, '.tmp', 'sync-generated-'))
  fixtures.push(dir)
  mkdirSync(join(dir, 'source'))
  mkdirSync(join(dir, 'output'))
  writeFileSync(join(dir, 'source', 'file'), 'same bytes')
  return dir
}

function run(dir, args = []) {
  return spawnSync(process.execPath, [join(root, 'scripts/sync-generated.mjs'), '--base', dir, '--copy', join(dir, 'source'), 'staged', '--out', 'staged', join(dir, 'output'), ...args], { encoding: 'utf8' })
}

afterEach(() => {
  for (const dir of fixtures.splice(0))
    rmSync(dir, { recursive: true, force: true })
})

describe('generated output publication', () => {
  it('preserves identical files and removes obsolete outputs', () => {
    const dir = fixture()
    expect(run(dir).status).toBe(0)
    const before = statSync(join(dir, 'output', 'file')).mtimeMs
    writeFileSync(join(dir, 'output', 'obsolete'), 'removed')
    expect(run(dir).status).toBe(0)
    expect(statSync(join(dir, 'output', 'file')).mtimeMs).toBe(before)
    expect(readdirSync(join(dir, 'output'))).toEqual(['file'])
  })

  it('removes staging after a generator fails and preserves published files', () => {
    const dir = fixture()
    writeFileSync(join(dir, 'output', 'file'), 'previous output')
    const result = run(dir, ['--', process.execPath, '-e', 'process.exit(7)'])
    expect(result.status).toBe(7)
    expect(readFileSync(join(dir, 'output', 'file'), 'utf8')).toBe('previous output')
    expect(readdirSync(dir).filter(name => name.startsWith('.gen-stage-'))).toEqual([])
  })

  it('removes staging when the generator executable does not exist', () => {
    const dir = fixture()
    const result = run(dir, ['--', join(dir, 'missing-executable')])
    expect(result.status).not.toBe(0)
    expect(result.stderr).toContain('failed to run generator')
    expect(readdirSync(dir).filter(name => name.startsWith('.gen-stage-'))).toEqual([])
  })

  it('validates every output group before publishing the first group', () => {
    const dir = fixture()
    writeFileSync(join(dir, 'output', 'file'), 'previous output')
    const result = run(dir, ['--out', 'missing', join(dir, 'other')])
    expect(result.status).not.toBe(0)
    expect(readFileSync(join(dir, 'output', 'file'), 'utf8')).toBe('previous output')
    expect(readdirSync(dir).filter(name => name.startsWith('.gen-stage-'))).toEqual([])
  })

  it.skipIf(process.platform === 'win32')('replaces a destination symlink without writing through it', () => {
    const dir = fixture()
    writeFileSync(join(dir, 'outside'), 'keep this')
    symlinkSync(join(dir, 'outside'), join(dir, 'output', 'file'))
    expect(run(dir).status).toBe(0)
    expect(readFileSync(join(dir, 'outside'), 'utf8')).toBe('keep this')
    expect(readFileSync(join(dir, 'output', 'file'), 'utf8')).toBe('same bytes')
  })

  it('rejects a file where an output directory is required before publishing any group', () => {
    const dir = fixture()
    writeFileSync(join(dir, 'output', 'file'), 'previous output')
    const result = run(dir, ['--out', 'staged/file', join(dir, 'other')])
    expect(result.status).not.toBe(0)
    expect(result.stderr).toContain('Generator output is not a directory')
    expect(readFileSync(join(dir, 'output', 'file'), 'utf8')).toBe('previous output')
    expect(readdirSync(dir).filter(name => name.startsWith('.gen-stage-'))).toEqual([])
  })

  it.skipIf(process.platform === 'win32')('rejects a generated symlink before changing the destination', () => {
    const dir = fixture()
    writeFileSync(join(dir, 'output', 'file'), 'previous output')
    symlinkSync(join(dir, 'source', 'file'), join(dir, 'source', 'link'))
    const result = run(dir)
    expect(result.status).not.toBe(0)
    expect(result.stderr).toContain('Generator output is not a regular file')
    expect(readFileSync(join(dir, 'output', 'file'), 'utf8')).toBe('previous output')
    expect(readdirSync(dir).filter(name => name.startsWith('.gen-stage-'))).toEqual([])
  })
})
