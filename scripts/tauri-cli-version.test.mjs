import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'bun:test'
import { tauriCliHash, tauriCliVersion } from './tauri-cli-version.mjs'

function lock(version) {
  return [
    '{',
    '  "packages": {',
    '    "@tauri-apps/api": ["@tauri-apps/api@2.10.0", "", {}, "sha512-a"],',
    `    "@tauri-apps/cli": ["@tauri-apps/cli@${version}", "", { "bin": { "tauri": "tauri.js" } }, "sha512-b"],`,
    '    "@tauri-apps/cli-linux-x64-gnu": ["@tauri-apps/cli-linux-x64-gnu@9.9.9", "", { "os": "linux" }, "sha512-c"],',
    '  }',
    '}',
  ].join('\n')
}

describe('tauriCliVersion', () => {
  it('reads the resolved version of the Tauri CLI package entry', () => {
    expect(tauriCliVersion(lock('2.12.4'))).toBe('2.12.4')
  })

  it('reads the real lockfile', () => {
    expect(tauriCliVersion(readFileSync(join(import.meta.dirname, '..', 'frontend', 'bun.lock'), 'utf8'))).toMatch(/^\d+\.\d+\.\d+/)
  })

  it('refuses a lockfile without the Tauri CLI', () => {
    expect(() => tauriCliVersion('{ "packages": {} }')).toThrow('frontend/bun.lock resolves no @tauri-apps/cli package.')
  })
})

describe('tauriCliHash', () => {
  it('hashes the resolved package identity, and changes with the version', () => {
    expect(tauriCliHash(lock('2.12.4'))).toBe(createHash('sha256').update('@tauri-apps/cli@2.12.4').digest('hex'))
    expect(tauriCliHash(lock('2.13.0'))).not.toBe(tauriCliHash(lock('2.12.4')))
  })
})

describe('the command line', () => {
  const script = join(import.meta.dirname, 'tauri-cli-version.mjs')

  it('prints the hash of the resolved Tauri CLI of the working directory', () => {
    const result = spawnSync('node', [script, '--hash'], {
      encoding: 'utf8',
      cwd: join(import.meta.dirname, '..'),
    })
    expect(result.status, result.stderr).toBe(0)
    expect(result.stdout).toBe(`${tauriCliHash(readFileSync(join(import.meta.dirname, '..', 'frontend', 'bun.lock'), 'utf8'))}\n`)
  })

  it('refuses any other argument', () => {
    const result = spawnSync('node', [script], { encoding: 'utf8' })
    expect(result.status).toBe(1)
    expect(result.stderr).toContain('Use --hash')
  })
})
