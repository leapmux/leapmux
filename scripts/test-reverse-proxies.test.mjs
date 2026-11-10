import { spawnSync } from 'node:child_process'
import { join } from 'node:path'
import process from 'node:process'
import { describe, expect, it } from 'bun:test'
import { ROOT } from './reverse-proxy-examples.mjs'

describe('parseArguments', () => {
  it('refuses an empty examples directory before starting Docker', () => {
    // Windows treats environment keys case-insensitively, so remove every casing.
    const childEnv = Object.fromEntries(
      Object.entries(process.env).filter(([key]) => key.toLowerCase() !== 'path'),
    )
    childEnv.PATH = ''
    const result = spawnSync(process.execPath, [
      join(ROOT, 'scripts', 'test-reverse-proxies.mjs'),
      '--examples-dir=',
      'leapmux:test',
    ], {
      cwd: ROOT,
      encoding: 'utf8',
      // Docker must remain unavailable if argument validation regresses.
      env: childEnv,
      timeout: 30_000,
    })

    expect(result.error).toBeUndefined()
    expect(result.status).not.toBe(0)
    expect(result.stderr).toContain('--examples-dir requires a path.')
    expect(result.stdout).not.toContain('Testing ')
  })
})
