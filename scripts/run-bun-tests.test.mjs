import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import { runBunTests } from './run-bun-tests.mjs'
import { scratchDirectories, writeFiles } from './test-support.mjs'

const scratch = scratchDirectories()
afterEach(scratch.cleanup)

describe('runBunTests', () => {
  it.each([0, 1])('writes a real JUnit result for exit code %i', (exitCode) => {
    const fails = exitCode !== 0
    const root = scratch.create('bun junit reports')
    writeFiles(root, { 'fixture.test.mjs': `import { test, expect } from 'bun:test'; test('fixture case', () => expect(1).toBe(${fails ? 2 : 1}));` })
    const env = {
      ...process.env,
      LEAPMUX_TEST_RESULTS_DIR: join(root, 'reports'),
    }
    const status = runBunTests('scripts', [
      join(root, 'fixture.test.mjs'),
    ], {
      env,
      run: (command, args, options) => spawnSync(command, args, { ...options, cwd: root, stdio: 'pipe' }),
    })
    expect(status).toBe(fails ? 1 : 0)
    const report = readFileSync(join(root, 'reports/scripts.xml'), 'utf8')
    expect(report).toContain('fixture case')
    expect(report).toContain(`failures="${fails ? 1 : 0}"`)
  })

  it('preserves literal arguments and exit codes without reports outside CI', () => {
    let observed
    expect(runBunTests('scripts', ['--timeout', '30000', 'a file.test.mjs'], {
      env: {},
      run: (command, args, options) => {
        observed = { command, args, options }
        return { status: 7 }
      },
    })).toBe(7)
    expect(observed.args).toEqual([
      'test',
      '--timeout',
      '30000',
      'a file.test.mjs',
    ])
  })
})
