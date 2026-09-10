import { spawnSync } from 'node:child_process'
import { chmodSync, copyFileSync, cpSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import { readBuildLog } from './check-build-cache.mjs'

const root = resolve(import.meta.dirname, '..')
const fixtures = []
const taskExecutable = Bun.which('task') ?? Bun.which('go-task')

function fixture(change = () => {}) {
  mkdirSync(join(root, '.tmp'), { recursive: true })
  const dir = mkdtempSync(join(root, '.tmp', 'check build cache-test-'))
  fixtures.push(dir)
  const production = Bun.YAML.parse(readFileSync(join(root, 'Taskfile.yaml'), 'utf8'))
  expect(production.tasks['check-build-cache']).toBeDefined()
  const config = {
    version: '3',
    method: 'checksum',
    run: 'once',
    tasks: {
      'build': { deps: ['prepare'], cmds: [{ task: 'compile' }] },
      'prepare': { deps: ['generate'] },
      'generate': { sources: ['schema'], generates: ['generated'], cmds: ['cp schema generated'] },
      'compile': {
        sources: ['generated'],
        generates: ['artifact'],
        cmds: ['cp generated artifact', 'echo compiled >> executions'],
      },
      'check-build-cache': production.tasks['check-build-cache'],
    },
  }
  change(config)
  writeFileSync(join(dir, 'Taskfile.yaml'), Bun.YAML.stringify(config))
  writeFileSync(join(dir, 'schema'), 'first input')
  mkdirSync(join(dir, 'scripts'))
  cpSync(join(root, 'scripts/check-build-cache.mjs'), join(dir, 'scripts/check-build-cache.mjs'))
  return dir
}

function run(dir, target, env = {}) {
  return spawnSync(taskExecutable, [target], {
    cwd: dir,
    encoding: 'utf8',
    maxBuffer: 8 * 1024 * 1024,
    env: { ...process.env, TASK_TEMP_DIR: join(dir, '.task'), ...env },
  })
}

function expectSuccess(result) {
  expect(result.error).toBeUndefined()
  expect(result.status, result.stdout + result.stderr).toBe(0)
}

function logs(dir) {
  const scratch = join(dir, '.tmp')
  return readdirSync(scratch).filter(name => name.startsWith('build-cache-')).map(name => ({
    stdout: readFileSync(join(scratch, name, 'stdout.log'), 'utf8'),
    stderr: readFileSync(join(scratch, name, 'stderr.log'), 'utf8'),
  }))
}

afterEach(() => {
  for (const dir of fixtures.splice(0))
    rmSync(dir, { recursive: true, force: true })
})

describe('native build cache check', () => {
  it('uses the invoking Task executable when its path contains spaces', () => {
    const dir = fixture()
    expectSuccess(run(dir, 'build'))
    const executable = join(dir, process.platform === 'win32' ? 'task executable.exe' : 'task executable')
    copyFileSync(taskExecutable, executable)
    chmodSync(executable, 0o755)
    expectSuccess(spawnSync(executable, ['check-build-cache'], {
      cwd: dir,
      encoding: 'utf8',
      env: { ...process.env, TASK_TEMP_DIR: join(dir, '.task') },
    }))
  })

  it('accepts cached dependencies and keeps logs from each check', () => {
    const dir = fixture()
    expectSuccess(run(dir, 'build'))
    expectSuccess(run(dir, 'check-build-cache'))
    expectSuccess(run(dir, 'check-build-cache'))
    expect(readFileSync(join(dir, 'executions'), 'utf8')).toBe('compiled\n')
    expect(logs(dir)).toHaveLength(2)
    for (const log of logs(dir)) {
      expect(log.stderr).toContain('task: "build" started')
      expect(log.stderr).toContain('Task "compile" is up to date')
    }
  })

  it.each(['input', 'output'])('fails when a changed %s triggers actual build commands', (change) => {
    const dir = fixture()
    expectSuccess(run(dir, 'build'))
    if (change === 'input')
      writeFileSync(join(dir, 'schema'), 'changed input')
    else
      rmSync(join(dir, 'artifact'))
    const result = run(dir, 'check-build-cache')
    expect(result.status).not.toBe(0)
    expect(result.stderr).toContain(`Build commands ran: ${change === 'input' ? 'compile, generate' : 'compile'}\n`)
    expect(readFileSync(join(dir, 'artifact'), 'utf8')).toBe(change === 'input' ? 'changed input' : 'first input')
    expect(readFileSync(join(dir, 'executions'), 'utf8')).toBe('compiled\ncompiled\n')
    expect(logs(dir)).toHaveLength(1)
  })

  it.each(['root', 'task', 'command', 'call', 'environment'])('detects commands with silence set on the %s', (scope) => {
    const dir = fixture((config) => {
      delete config.tasks.compile.sources
      if (scope === 'root')
        config.silent = true
      if (scope === 'task')
        config.tasks.compile.silent = true
      if (scope === 'command')
        config.tasks.compile.cmds = config.tasks.compile.cmds.map(cmd => ({ cmd, silent: true }))
      if (scope === 'call')
        config.tasks.build.cmds[0].silent = true
    })
    expectSuccess(run(dir, 'build'))
    const result = run(dir, 'check-build-cache', scope === 'environment' ? { TASK_SILENT: 'true' } : {})
    expect(result.status).not.toBe(0)
    expect(result.stderr).toContain('Build commands ran: compile')
  })

  it('ignores commands that Task skips for conditions or other platforms', () => {
    const dir = fixture((config) => {
      config.tasks.build.cmds.push(
        { cmd: 'echo must-not-run', if: 'exit 1' },
        { cmd: 'echo must-not-run', platforms: [process.platform === 'win32' ? 'linux' : 'windows'] },
      )
    })
    expectSuccess(run(dir, 'build'))
    expectSuccess(run(dir, 'check-build-cache'))
    expect(logs(dir)[0].stdout).toContain('if condition not met - skipped')
    expect(logs(dir)[0].stdout).toContain('not for current platform - ignored')
  })

  it('fails for a missing platform output even if the producer exits successfully', () => {
    const dir = fixture((config) => {
      config.tasks.compile.generates.push('other-platform-artifact')
    })
    expectSuccess(run(dir, 'build'))
    const result = run(dir, 'check-build-cache')
    expect(result.status).not.toBe(0)
    expect(result.stderr).toContain('Build commands ran: compile')
  })

  it('reports a failed build and preserves stdout and stderr', () => {
    const dir = fixture((config) => {
      config.tasks.compile.cmds = ['echo build-output; echo build-error >&2; exit 7']
    })
    const result = run(dir, 'check-build-cache')
    expect(result.status).not.toBe(0)
    expect(result.stderr).toContain('Build failed')
    expect(result.stderr).toContain('build-output')
    expect(result.stderr).toContain('build-error')
    expect(logs(dir)[0].stdout).toContain('build-output')
    expect(logs(dir)[0].stderr).toContain('build-error')
  })

  it('retains compiler output larger than the default subprocess buffer', () => {
    const dir = fixture((config) => {
      config.tasks.compile.cmds = ['bun compiler-output.mjs']
    })
    const bytes = 2 * 1024 * 1024
    writeFileSync(join(dir, 'compiler-output.mjs'), `process.stdout.write('x'.repeat(${bytes}) + '\\ncompiler-output-end\\n'); process.exitCode = 7\n`)
    const result = run(dir, 'check-build-cache')
    expect(result.error).toBeUndefined()
    expect(result.status).not.toBe(0)
    expect(result.stderr).toContain('compiler-output-end')
    expect(result.stderr).toContain('Build failed')
    expect(logs(dir)[0].stdout).toBe(`${'x'.repeat(bytes)}\ncompiler-output-end\n`)
  })

  it('fails when Task cannot start', () => {
    const dir = fixture()
    const result = spawnSync(process.execPath, [join(dir, 'scripts/check-build-cache.mjs'), join(dir, 'missing-task')], {
      cwd: dir,
      encoding: 'utf8',
    })
    expect(result.status).not.toBe(0)
    expect(result.stderr).toContain('Cannot start Task')
  })

  it('runs actual commands even when the environment requests a dry run', () => {
    const dir = fixture()
    expectSuccess(run(dir, 'build'))
    rmSync(join(dir, 'artifact'))
    const result = spawnSync(process.execPath, [join(dir, 'scripts/check-build-cache.mjs'), taskExecutable], {
      cwd: dir,
      encoding: 'utf8',
      env: { ...process.env, TASK_TEMP_DIR: join(dir, '.task'), TASK_DRY: 'true' },
    })
    expect(result.status).not.toBe(0)
    expect(result.stderr).toContain('Build commands ran: compile')
    expect(readFileSync(join(dir, 'executions'), 'utf8')).toBe('compiled\ncompiled\n')
  })

  it.each(['', 'task: "build" started\n', 'task: "build" finished\n', 'unknown Task log format\n'])(
    'rejects an absent or incomplete execution log: %j',
    async (content) => {
      const dir = fixture()
      const path = join(dir, 'log')
      writeFileSync(path, content)
      await expect(readBuildLog(path)).rejects.toThrow('Task did not report a complete build run')
    },
  )

  it('accepts a build that Task skips through its own status check', () => {
    const dir = fixture((config) => {
      config.tasks.build = { status: ['exit 0'], cmds: ['echo must-not-run'] }
    })
    expectSuccess(run(dir, 'check-build-cache'))
    expect(logs(dir)[0].stderr).toContain('Task "build" is up to date')
  })

  it('rejects a missing build log', async () => {
    const dir = fixture()
    await expect(readBuildLog(join(dir, 'missing.log'))).rejects.toThrow()
  })

  it('reads carriage returns and a final line without a newline', async () => {
    const dir = fixture()
    const path = join(dir, 'log')
    writeFileSync(path, 'task: "build" started\r\ntask: [compile] cp input output\r\ntask: "build" finished')
    expect(await readBuildLog(path)).toEqual(['compile'])
  })
})

describe('native build cache checks in CI', () => {
  it('prepares signing tools through the shared Task prerequisite', () => {
    const workflow = Bun.YAML.parse(readFileSync(join(root, '.github/workflows/desktop-artifacts.yaml'), 'utf8'))
    const steps = workflow.jobs['macos-app-sign'].steps
    const setup = steps.findIndex(step => step.uses?.startsWith('arduino/setup-task@'))
    const prepare = steps.findIndex(step => step.run === 'task prepare-dmg-tools')
    expect(setup).toBeGreaterThanOrEqual(0)
    expect(prepare).toBeGreaterThan(setup)
    const taskfile = Bun.YAML.parse(readFileSync(join(root, 'Taskfile.yaml'), 'utf8'))
    expect(taskfile.tasks['prepare-dmg-tools'].internal ?? false).toBe(false)
    expect(steps.some(step => /node-gyp|bun install/.test(step.run ?? ''))).toBe(false)
  })

  it.each(['ci.yaml', 'desktop-artifacts.yaml'])('checks every native build immediately in %s', (file) => {
    const workflow = Bun.YAML.parse(readFileSync(join(root, '.github/workflows', file), 'utf8'))
    const jobs = Object.entries(workflow.jobs).filter(([, job]) => job.steps?.some(step => step.run === 'task build'))
    expect(jobs.map(([name]) => name).sort()).toEqual(['linux', 'macos', 'windows'])
    for (const [name, job] of jobs) {
      const index = job.steps.findIndex(step => step.run === 'task build')
      const build = job.steps[index]
      const check = job.steps[index + 1]
      expect(check?.run, `${file}: ${name}`).toBe('task check-build-cache')
      expect(check.shell).toBe(build.shell)
      expect(check.env).toEqual(build.env)
      expect(check.if).toBe(build.if)
      expect(check['continue-on-error'] ?? false).toBe(false)
      const diagnostics = job.steps.find(step => step.name === 'Diagnose linuxdeploy (on failure)')
      if (diagnostics) {
        expect(build.id).toBe('build')
        expect(diagnostics.if).toBe('failure() && steps.build.outcome == \'failure\'')
      }
    }
  })
})
