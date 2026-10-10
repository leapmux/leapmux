import { execFileSync } from 'node:child_process'
import { chmodSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterAll, describe, expect, it } from 'bun:test'
import { recordIdentity, verifyIdentity } from './verify-agent-source-freeze.mjs'

// A throwaway git repository freezes real file facts without touching the checkout.
const root = mkdtempSync(join(tmpdir(), 'source-freeze-test-'))
afterAll(() => rmSync(root, { recursive: true, force: true }))
execFileSync('git', ['init', '-q', '.'], { cwd: root })
execFileSync('git', ['config', 'user.email', 'freeze@test'], { cwd: root })
execFileSync('git', ['config', 'user.name', 'freeze'], { cwd: root })
mkdirSync(join(root, 'src'))
writeFileSync(join(root, 'src', 'a.ts'), 'export const a = 1\n')
writeFileSync(join(root, 'b.ts'), 'export const b = 2\n')
execFileSync('git', ['add', '.'], { cwd: root })
execFileSync('git', ['commit', '-qm', 'freeze'], { cwd: root })

describe('verifyIdentity', () => {
  it('accepts an unchanged tree', () => {
    const identity = recordIdentity(root)
    expect(verifyIdentity(root, identity)).toEqual([])
  })

  it('refuses modified bytes', () => {
    const identity = recordIdentity(root)
    writeFileSync(join(root, 'src', 'a.ts'), 'export const a = 2\n')
    const failures = verifyIdentity(root, identity)
    expect(failures.some(failure => failure.startsWith('modified bytes in src/a.ts'))).toBe(true)
    writeFileSync(join(root, 'src', 'a.ts'), 'export const a = 1\n')
  })

  it('refuses a changed mode', () => {
    const identity = recordIdentity(root)
    chmodSync(join(root, 'b.ts'), 0o755)
    const failures = verifyIdentity(root, identity)
    expect(failures.some(failure => failure.startsWith('changed mode of b.ts'))).toBe(true)
    chmodSync(join(root, 'b.ts'), 0o644)
  })

  it('refuses a new file', () => {
    const identity = recordIdentity(root)
    writeFileSync(join(root, 'new.ts'), 'export const n = 1\n')
    const failures = verifyIdentity(root, identity)
    expect(failures.some(failure => failure.startsWith('new file new.ts'))).toBe(true)
    rmSync(join(root, 'new.ts'))
  })

  it('refuses a deleted file', () => {
    const identity = recordIdentity(root)
    rmSync(join(root, 'b.ts'))
    execFileSync('git', ['add', '-A'], { cwd: root })
    const failures = verifyIdentity(root, identity)
    expect(failures.some(failure => failure.startsWith('deleted file b.ts'))).toBe(true)
    writeFileSync(join(root, 'b.ts'), 'export const b = 2\n')
    execFileSync('git', ['add', '-A'], { cwd: root })
  })

  it('refuses a changed discovery manifest', () => {
    // The manifest lives outside the frozen tree, as a real run's test-results does.
    const manifestRoot = mkdtempSync(join(tmpdir(), 'source-freeze-manifest-'))
    const discoveryPath = join(manifestRoot, 'discovery.json')
    writeFileSync(discoveryPath, '{"v":1}\n')
    const identity = recordIdentity(root, discoveryPath)
    expect(verifyIdentity(root, identity)).toEqual([])
    writeFileSync(discoveryPath, '{"v":2}\n')
    const failures = verifyIdentity(root, identity)
    expect(failures.some(failure => failure.includes('changed since the freeze'))).toBe(true)
  })

  it('refuses a moved HEAD', () => {
    const identity = recordIdentity(root)
    writeFileSync(join(root, 'c.ts'), 'export const c = 3\n')
    execFileSync('git', ['add', '.'], { cwd: root })
    execFileSync('git', ['commit', '-qm', 'move'], { cwd: root })
    const failures = verifyIdentity(root, identity)
    expect(failures.some(failure => failure.startsWith('HEAD moved'))).toBe(true)
    execFileSync('git', ['reset', '-q', '--hard', 'HEAD~1'], { cwd: root })
  })

  it('records new and deleted files against a previous identity', () => {
    const first = recordIdentity(root)
    rmSync(join(root, 'b.ts'))
    writeFileSync(join(root, 'src', 'd.ts'), 'export const d = 4\n')
    execFileSync('git', ['add', '-A'], { cwd: root })
    execFileSync('git', ['commit', '-qm', 'swap'], { cwd: root })
    const second = recordIdentity(root, undefined, first)
    expect(second.newFiles).toEqual(['src/d.ts'])
    expect(second.deletedFiles).toEqual(['b.ts'])
  })
})
