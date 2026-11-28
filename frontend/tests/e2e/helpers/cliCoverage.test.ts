import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { auditE2ECLICoverage, extractCoveredCommandsFromSource } from './cliCoverage'

const HELPERS_DIR = fileURLToPath(new URL('.', import.meta.url))
const E2E_DIR = resolve(HELPERS_DIR, '..')
const REPO_ROOT = resolve(E2E_DIR, '..', '..', '..')

describe('CLI E2E command coverage', () => {
  it('extracts static, loop, and helper-parameterized CLI commands from TypeScript AST', () => {
    const known = new Set([
      'admin user create',
      'daemon hub enable',
      'daemon hub start',
      'events',
    ])
    const sample = `
      async function runVerbs(mode: 'hub') {
        for (const verb of ['enable', 'start'] as const) {
          await runCLILocal(['daemon', mode, verb, '--data-dir', '/tmp/x'])
        }
      }
      await runVerbs('hub')
      await runCLI(cli, ['admin', 'user', 'create', 'alice'])
      startControlEventStream(cli, [])
    `
    const found = extractCoveredCommandsFromSource(sample, 'sample.spec.ts', known)
    expect([...found].sort()).toEqual([
      'admin user create',
      'daemon hub enable',
      'daemon hub start',
      'events',
    ])
  })

  it('exercises every runnable leaf command of the leapmux binary in Playwright E2E specs', () => {
    const { leafCommands, uncovered } = auditE2ECLICoverage(REPO_ROOT, E2E_DIR)
    expect(leafCommands.length).toBeGreaterThan(250)
    expect(uncovered, `Uncovered CLI leaf commands (${uncovered.length}):\n${uncovered.join('\n')}`).toEqual([])
  })
})
