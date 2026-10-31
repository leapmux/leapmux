import { spawnSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'

const root = resolve(import.meta.dirname, '..')
const readYaml = path => Bun.YAML.parse(readFileSync(resolve(root, path), 'utf8'))
const expression = body => `\${{ ${body} }}`
// The one Bun test that the native jobs run: the resource monitor reads each
// OS through its own probe, so each runner tests its own.
const MONITOR_TEST = 'bun test --timeout 30000 scripts/ci-resource-monitor.test.mjs scripts/ci-resource-monitor/'
const fixtures = []

afterEach(() => {
  for (const dir of fixtures.splice(0))
    rmSync(dir, { recursive: true, force: true })
})

// Traverse Task's dependencies and command tasks. A hidden dependency on a full
// test target would restore the duplicated frontend tests and cross-platform lint.
function commands(tasks, name, visited = new Set()) {
  if (visited.has(name))
    return []
  visited.add(name)
  const task = tasks[name]
  if (!task)
    throw new Error(`Unknown task: ${name}`)
  return [
    ...(task.deps ?? []).flatMap(dep => commands(tasks, typeof dep === 'string'
      ? dep
      : dep.task, visited)),
    ...(task.cmds ?? []).flatMap((cmd) => {
      if (typeof cmd === 'string')
        return [cmd]
      return cmd.task
        ? commands(tasks, cmd.task, visited)
        : [cmd.cmd]
    }),
  ]
}

describe('native CI validation', () => {
  it('serializes SQL generators that compile the same Go tool on a cold cache', () => {
    const task = readYaml('Taskfile.yaml').tasks['generate-sqlc']
    expect(task.deps ?? []).toEqual([])
    expect(task.cmds).toEqual([
      { task: 'generate-sqlc-hub' },
      { task: 'generate-sqlc-worker' },
    ])
  })

  it('keeps cross-platform Go vet in lint instead of repeating it during tests', () => {
    const tasks = readYaml('Taskfile.yaml').tasks
    for (const name of ['test-backend', 'test-backend-no-docker-run', 'test-desktop-run']) {
      const testCommands = commands(tasks, name).filter(cmd => cmd.includes('go tool gotestsum'))
      expect(testCommands).toHaveLength(1)
      expect(testCommands[0]).toContain('-- -vet=off ')
      // No build tag hides a test. A test that needs Docker skips itself instead.
      expect(testCommands[0]).not.toContain('-tags')
    }
    const linux = readYaml('.github/workflows/ci.yaml').jobs.linux
    for (const arch of ['amd64', 'arm64'])
      expect(linux.strategy.matrix.include.map(entry => entry.arch)).toContain(arch)
    expect(linux.steps.find(step => step.run === 'task test-native-linux')?.if).toBe('matrix.arch == \'arm64\'')
    const armTests = commands(tasks, 'test-native-linux')
    for (const module of ['backend', 'desktop/go']) {
      const lint = commands(tasks, module === 'backend'
        ? 'lint-backend'
        : 'lint-desktop')
      for (const os of ['windows', 'linux', 'darwin']) {
        expect(lint).toContain(`cd ${module} && GOOS=${os} go vet ./...`)
        expect(armTests).toContain(`cd ${module} && GOOS=${os} go vet ./...`)
      }
    }
  })

  it('keeps architecture-dependent tests and Go analysis on Linux arm64 without repeating portable suites', () => {
    const tasks = readYaml('Taskfile.yaml').tasks
    const native = commands(tasks, 'test-native')
    const linux = commands(tasks, 'test-native-linux')
    for (const command of native) {
      // lint-desktop already runs Clippy before the Rust tests.
      if (!command.includes('cargo clippy'))
        expect(linux).toContain(command)
    }
    for (const module of ['backend', 'desktop/go'])
      expect(linux).toContain(`cd ${module} && go tool golangci-lint run --allow-serial-runners ./...`)
    expect(linux.filter(command => command.includes('cargo clippy'))).toHaveLength(1)
    expect(linux.filter(command => command.includes('bun run test'))).toEqual(['cd frontend && bun run test scripts/ tests/e2e/helpers/'])
    expect(linux).toContain(MONITOR_TEST)
    expect(linux.filter(command => command !== MONITOR_TEST).join('\n')).not.toMatch(/bun test|bun run (?:lint|typecheck)|validate-json|renovate/)
  })

  it.each([
    '',
    'frontend-native',
    'ci-monitor',
    'backend',
    'desktop-go',
    'rust-test',
    'rust-lint',
  ])('propagates failures and stops subsequent native commands: %s', (failure) => {
    const tasks = readYaml('Taskfile.yaml').tasks
    mkdirSync(join(root, '.tmp'), { recursive: true })
    const dir = mkdtempSync(join(root, '.tmp', 'ci-native-'))
    fixtures.push(dir)
    const stages = [
      'frontend-native',
      'ci-monitor',
      'backend',
      'desktop-go',
      'rust-test',
      'rust-lint',
    ]
    const selected = Object.fromEntries([
      'test-native',
      'test-frontend-native',
      'test-ci-monitor',
      'test-backend-no-docker-run',
      'test-desktop-run',
    ].map(name => [
      name,
      structuredClone(tasks[name]),
    ]))
    let index = 0
    // Retain the production orchestration. Replace compilation with a recorder
    // so the fixture can fail each test command without native toolchains.
    for (const name of [
      'test-frontend-native',
      'test-ci-monitor',
      'test-backend-no-docker-run',
      'test-desktop-run',
      'test-native',
    ]) {
      delete selected[name].deps
      selected[name].cmds = selected[name].cmds.map(cmd => typeof cmd === 'string'
        ? `bun record.mjs ${stages[index++]}`
        : cmd)
    }
    writeFileSync(join(dir, 'Taskfile.yaml'), Bun.YAML.stringify({ version: '3', tasks: selected }))
    writeFileSync(join(dir, 'record.mjs'), `import { appendFileSync } from 'node:fs'; appendFileSync('executed', process.argv[2] + '\\n'); if (process.argv[2] === process.env.FAIL_STAGE) process.exit(1)`)
    const result = spawnSync('task', ['test-native'], {
      cwd: dir,
      env: { ...process.env, FAIL_STAGE: failure },
      encoding: 'utf8',
    })
    expect(result.error).toBeUndefined()
    expect(result.status === 0, result.stdout + result.stderr).toBe(failure === '')
    expect(readFileSync(join(dir, 'executed'), 'utf8').trim().split('\n')).toEqual(failure
      ? stages.slice(0, stages.indexOf(failure) + 1)
      : stages)
  })

  it('keeps native Go tests, Rust tests and Rust lint without repeated portable validation', () => {
    const tasks = readYaml('Taskfile.yaml').tasks
    const native = commands(tasks, 'test-native')
    expect(native.some(cmd => cmd.includes('cd backend && go tool gotestsum'))).toBe(true)
    expect(native.some(cmd => cmd.includes('cd desktop/go && go tool gotestsum'))).toBe(true)
    expect(native).toContain('cd desktop/rust && cargo test --locked')
    expect(native).toContain('cd desktop/rust && cargo clippy --locked --all-targets -- -D warnings')
    expect(native).toContain('cd frontend && bun run test scripts/ tests/e2e/helpers/')
    expect(native).toContain(MONITOR_TEST)
    expect(native.filter(cmd => cmd !== MONITOR_TEST).join('\n')).not.toMatch(/golangci-lint|GOOS=|bun run (?:lint|typecheck)|bun test|renovate/)
    expect(native.filter(cmd => cmd.includes('bun run test'))).toEqual(['cd frontend && bun run test scripts/ tests/e2e/helpers/'])
    // The no-Docker run tests every package. The container tests skip themselves.
    const backend = native.find(cmd => cmd.includes('cd backend && go tool gotestsum'))
    expect(backend).toEndWith('-- -vet=off ./...')
    expect(native.some(cmd => cmd.includes('build-sidecar.mjs') || cmd.includes('go build'))).toBe(true)
  })

  it('skips the tests that need Docker in the no-Docker run, and only there', () => {
    const taskfile = readYaml('Taskfile.yaml')
    const { tasks } = taskfile
    // storetest.SQLContainer skips its test through testutil.RequireDocker, which reads this variable.
    const docker = readFileSync(resolve(root, 'backend/internal/util/testutil/docker.go'), 'utf8')
    const name = /^const SkipDockerEnv = "(\w+)"$/m.exec(docker)?.[1]
    expect(name).toBe('LEAPMUX_TEST_SKIP_DOCKER')
    expect(tasks['test-backend-no-docker-run'].env).toEqual({ [name]: '1' })
    for (const entry of ['test-native', 'test-native-linux', 'test-no-docker']) {
      const reached = new Set()
      commands(tasks, entry, reached)
      expect(reached.has('test-backend-no-docker-run'), entry).toBe(true)
    }
    // The full run on Linux amd64 fails when Docker is absent instead of skipping the container tests.
    const full = new Set()
    commands(tasks, 'test', full)
    expect(full.has('test-backend-no-docker-run')).toBe(false)
    expect(taskfile.env?.[name]).toBeUndefined()
    for (const [task, definition] of Object.entries(tasks)) {
      if (task !== 'test-backend-no-docker-run')
        expect(JSON.stringify(definition), task).not.toContain(name)
    }
    expect(readFileSync(resolve(root, '.github/workflows/ci.yaml'), 'utf8')).not.toContain(name)
  })

  it('runs complete validation on Linux and native validation plus packaging on macOS and Windows', () => {
    const { jobs } = readYaml('.github/workflows/ci.yaml')
    expect(jobs.linux.steps.find(step => step.run === 'task test')?.if).toBe('matrix.arch == \'amd64\'')
    expect(jobs.linux.steps.find(step => step.run === 'task lint')?.if).toBe('matrix.arch == \'amd64\'')
    for (const os of ['macos', 'windows']) {
      const steps = jobs[os].steps
      expect(steps.filter(step => step.run === 'task test-native')).toHaveLength(1)
      expect(steps.some(step => step.run === 'task test-no-docker')).toBe(false)
      expect(steps.some(step => step.run === 'task build')).toBe(true)
      expect(steps.some(step => step.run === 'task check-build-cache')).toBe(true)
      if (os === 'windows') {
        for (const step of steps.filter(step => step.run?.startsWith('task ')))
          expect(step.shell).toBe('pwsh')
      }
    }
  })

  it.each(['linux', 'macos', 'windows'])('monitors the %s runner on request, from the toolchain setup to the end of the job', (name) => {
    const job = readYaml('.github/workflows/ci.yaml').jobs[name]
    const steps = job.steps
    const monitor = 'bun scripts/ci-resource-monitor.mjs'
    const start = steps.findIndex(step => step.uses === './.github/actions/ci-resource-monitor' && step.with.mode === 'start')
    const stop = steps.findIndex(step => step.uses === './.github/actions/ci-resource-monitor' && step.with.mode === 'stop')
    const setup = steps.findIndex(step => step.uses === './.github/actions/setup-build')
    const tests = steps.findIndex(step => /^task test(?:-native(?:-linux)?)?$/.test(step.run ?? ''))
    // The monitor needs the Bun that the toolchain setup installs.
    expect(start).toBe(setup + 1)
    expect(tests).toBeGreaterThan(start)
    // The stop step runs last and after a failure, so it prints the log of every job that GitHub keeps.
    expect(stop).toBe(steps.length - 1)
    // The repository variable CI_RESOURCE_MONITOR switches the monitor on.
    expect(steps[start].if).toBe('vars.CI_RESOURCE_MONITOR == \'true\'')
    expect(steps[stop].if).toBe('always() && vars.CI_RESOURCE_MONITOR == \'true\'')
    const action = readYaml('.github/actions/ci-resource-monitor/action.yaml')
    expect(action.inputs.mode.required).toBe(true)
    expect(action.runs.using).toBe('composite')
    expect(action.runs.steps).toHaveLength(1)
    const invocation = action.runs.steps[0]
    expect(invocation.if).toBe('always()')
    expect(invocation.shell).toBe(expression('runner.os == \'Windows\' && \'pwsh\' || \'bash\''))
    expect(invocation['working-directory']).toBe(expression('github.workspace'))
    expect(invocation.env.GH_TOKEN).toBe(expression('github.token'))
    expect(invocation.env.MONITOR_HEAD_SHA).toBe(expression('github.event.pull_request.head.sha || github.sha'))
    expect(invocation.run).toBe(`${monitor} "${expression('inputs.mode')}"`)
    expect(job.permissions.checks).toBe('write')
    expect(existsSync(resolve(root, 'scripts/ci-resource-monitor.mjs'))).toBe(true)
  })
})
