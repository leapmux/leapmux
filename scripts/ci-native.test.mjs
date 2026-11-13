import { spawnSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import { GO_TOOL_NAMES } from './go-tools.mjs'

const root = resolve(import.meta.dirname, '..')
const readYaml = path => Bun.YAML.parse(readFileSync(resolve(root, path), 'utf8'))
const expression = body => `\${{ ${body} }}`
// Each native runner tests the resource monitor's probes for its own OS.
const MONITOR_TEST = 'node scripts/run-bun-tests.mjs ci-monitor --timeout 30000 scripts/ci-resource-monitor.test.mjs scripts/ci-resource-monitor/'
const CI_RESULTS_TEST = 'node scripts/run-bun-tests.mjs ci-results --timeout 30000 ./scripts/run-test-suite.test.mjs ./scripts/archive-test-results.test.mjs ./scripts/run-bun-tests.test.mjs ./scripts/test-results-path.test.mjs ./scripts/test-results-tar.test.mjs'
const CACHE_NATIVE_TEST = 'node scripts/run-bun-tests.mjs cache-native --timeout 120000 ./scripts/cache-policy.test.mjs ./scripts/cache-snapshot.test.mjs ./scripts/cache-snapshot.native.test.mjs ./scripts/test-support.test.mjs ./scripts/run-go-tool.test.mjs ./scripts/download-go-modules.test.mjs ./scripts/download-cli-tools.test.mjs'
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
  it('runs every isolated Go tool through its module runner', () => {
    const directTool = new RegExp(`\\bgo\\s+tool\\s+(?:${GO_TOOL_NAMES.join('|')})\\b`)
    const directCommands = []
    for (const [name, task] of Object.entries(readYaml('Taskfile.yaml').tasks)) {
      for (const entry of task.cmds ?? []) {
        const command = typeof entry === 'string'
          ? entry
          : entry.cmd
        if (typeof command === 'string' && directTool.test(command))
          directCommands.push(`${name}: ${command}`)
      }
    }
    expect(directCommands).toEqual([])
  })

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
      const testCommands = commands(tasks, name).filter(cmd => cmd.includes('run-go-tool.mjs gotestsum'))
      expect(testCommands).toHaveLength(1)
      expect(testCommands[0]).toContain('-- -vet=off ')
      // No build tag hides a test. A test that needs Docker skips itself instead.
      expect(testCommands[0]).not.toContain('-tags')
    }
    const linux = readYaml('.github/workflows/ci.yaml').jobs.linux
    for (const arch of ['amd64', 'arm64'])
      expect(linux.strategy.matrix.include.map(entry => entry.arch)).toContain(arch)
    expect(linux.steps.find(step => step.run === 'node scripts/run-test-suite.mjs native-linux task test-native-linux')?.if).toBe('matrix.arch == \'arm64\'')
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
    for (const module of ['backend', 'desktop/go']) {
      expect(linux).toContain(`cd ${module} && node ${module === 'backend'
        ? '..'
        : '../..'}/scripts/run-go-tool.mjs golangci-lint run --allow-serial-runners ./...`)
    }
    expect(linux.filter(command => command.includes('cargo clippy'))).toHaveLength(1)
    expect(linux.filter(command => command.includes('bun run test'))).toEqual(['cd frontend && bun run test scripts/ tests/e2e/helpers/'])
    expect(linux).toContain(MONITOR_TEST)
    expect(linux).toContain(CI_RESULTS_TEST)
    expect(linux).toContain(CACHE_NATIVE_TEST)
    expect(linux.filter(command => command !== MONITOR_TEST && command !== CI_RESULTS_TEST && command !== CACHE_NATIVE_TEST).join('\n')).not.toMatch(/bun test|run-bun-tests|bun run (?:lint|typecheck)|validate-json|renovate/)
  })

  it.each([
    '',
    'frontend-native',
    'ci-monitor',
    'ci-results',
    'backend',
    'desktop-go',
    'rust-test',
    'cache-native',
    'rust-lint',
  ])('propagates failures and stops subsequent native commands: %s', (failure) => {
    const tasks = readYaml('Taskfile.yaml').tasks
    mkdirSync(join(root, '.tmp'), { recursive: true })
    const dir = mkdtempSync(join(root, '.tmp', 'ci-native-'))
    fixtures.push(dir)
    const stages = [
      'frontend-native',
      'ci-monitor',
      'ci-results',
      'backend',
      'desktop-go',
      'rust-test',
      'rust-lint',
    ]
    const selected = Object.fromEntries([
      'test-native',
      'test-frontend-native',
      'test-ci-monitor',
      'test-ci-results',
      'test-backend-no-docker-run',
      'test-desktop-run',
    ].map(name => [
      name,
      structuredClone(tasks[name]),
    ]))
    let index = 0
    // Retain the production orchestration. Replace compilation with a recorder
    // so the fixture can fail each test command without native toolchains.
    // test-cache-native keeps its `task:` reference, which the fixture's
    // Taskfile resolves to its own definition below.
    selected['test-cache-native'] = {
      cmds: ['bun record.mjs cache-native'],
    }
    for (const name of [
      'test-frontend-native',
      'test-ci-monitor',
      'test-ci-results',
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
    // test-desktop-run contributes two recorded commands (Go and Rust), so
    // the recorded order of the whole run is the stage list with the
    // cache-native stage inserted after it.
    const recorded = [
      ...stages.slice(0, -1),
      'cache-native',
      ...stages.slice(-1),
    ]
    const executedActual = readFileSync(join(dir, 'executed'), 'utf8').trim().split('\n')
    expect(executedActual).toEqual(failure
      ? recorded.slice(0, recorded.indexOf(failure) + 1)
      : recorded)
  })

  // The runner's minimal Rust profile has no rustfmt, so the check needs the component.
  it('installs rustfmt in the job that checks the Rust format', () => {
    expect(commands(readYaml('Taskfile.yaml').tasks, 'lint-desktop')).toContain('cd desktop/rust && cargo fmt --check')
    const steps = readYaml('.github/workflows/ci.yaml').jobs.linux.steps
    const setup = steps.find(step => step.uses === './.github/actions/setup-build')
    expect(setup.with['rust-components'].split(',')).toContain('rustfmt')
  })

  it('lets lint-fix-desktop run on a tree with changes and no built sidecar', () => {
    const tasks = readYaml('Taskfile.yaml').tasks
    const fix = commands(tasks, 'lint-fix-desktop')
    expect(fix).toContain('cd desktop/rust && cargo clippy --fix --allow-dirty --all-targets')
    expect(fix).toContain('cd desktop/rust && cargo fmt')
    for (const command of commands(tasks, 'build-desktop-sidecar'))
      expect(fix).toContain(command)
  })

  it('keeps native Go tests, Rust tests and Rust lint without repeated portable validation', () => {
    const tasks = readYaml('Taskfile.yaml').tasks
    const native = commands(tasks, 'test-native')
    expect(native.some(cmd => cmd.includes('cd backend && node ../scripts/run-go-tool.mjs gotestsum'))).toBe(true)
    expect(native.some(cmd => cmd.includes('cd desktop/go && node ../../scripts/run-go-tool.mjs gotestsum'))).toBe(true)
    expect(native).toContain('cd desktop/rust && cargo test --locked')
    expect(native).toContain('cd desktop/rust && cargo clippy --locked --all-targets -- -D warnings')
    expect(native).toContain('cd frontend && bun run test scripts/ tests/e2e/helpers/')
    expect(native).toContain(MONITOR_TEST)
    expect(native).toContain(CI_RESULTS_TEST)
    expect(native).toContain(CACHE_NATIVE_TEST)
    expect(native.filter(cmd => cmd !== MONITOR_TEST && cmd !== CI_RESULTS_TEST && cmd !== CACHE_NATIVE_TEST).join('\n')).not.toMatch(/golangci-lint|GOOS=|run-bun-tests|bun run (?:lint|typecheck)|bun test|renovate/)
    expect(native.filter(cmd => cmd.includes('bun run test'))).toEqual(['cd frontend && bun run test scripts/ tests/e2e/helpers/'])
    // The no-Docker run tests every package. The container tests skip themselves.
    const backend = native.find(cmd => cmd.includes('cd backend && node ../scripts/run-go-tool.mjs gotestsum'))
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
    expect(jobs.linux.steps.find(step => step.run === 'node scripts/run-test-suite.mjs full task test')?.if).toBe('matrix.arch == \'amd64\'')
    expect(jobs.linux.steps.find(step => step.run === 'task lint')?.if).toBe('matrix.arch == \'amd64\'')
    for (const os of ['macos', 'windows']) {
      const steps = jobs[os].steps
      expect(steps.filter(step => step.run === 'node scripts/run-test-suite.mjs native task test-native')).toHaveLength(1)
      expect(steps.some(step => step.run === 'task test-no-docker')).toBe(false)
      expect(steps.some(step => step.run === 'task build')).toBe(true)
      expect(steps.some(step => step.run === 'task check-build-cache')).toBe(true)
      if (os === 'windows') {
        for (const step of steps.filter(step => /^(?:task |node scripts\/run-test-suite\.mjs )/.test(step.run ?? '')))
          expect(step.shell).toBe('pwsh')
      }
    }
  })

  it.each(['linux', 'macos', 'windows'])('uploads passing and failed test results after the %s tests', (name) => {
    const { steps } = readYaml('.github/workflows/ci.yaml').jobs[name]
    const uploads = steps.filter(step => step.uses === './.github/actions/upload-test-results')
    expect(uploads).toHaveLength(1)
    expect(uploads[0].if).toBe('always()')
    const lastTest = steps.findLastIndex(step => /\btask test(?:-native(?:-linux)?)?$/.test(step.run ?? ''))
    expect(lastTest).toBeGreaterThan(-1)
    expect(steps.indexOf(uploads[0])).toBeGreaterThan(lastTest)
  })

  it('uploads one compressed archive with a distinct filename per runner, matrix cell and run attempt', () => {
    const action = readYaml('.github/actions/upload-test-results/action.yaml')
    expect(action.runs.using).toBe('composite')
    const uploads = action.runs.steps.filter(step => step.uses?.startsWith('actions/upload-artifact@'))
    expect(uploads).toHaveLength(1)
    const upload = uploads[0]
    expect(upload.if).toContain('always()')
    expect(upload.if).toContain('steps.archive.outputs.archive != \'\'')
    expect(upload.uses).toMatch(/^actions\/upload-artifact@[a-f0-9]{40}$/)
    expect(upload.with['if-no-files-found']).toBe('error')
    expect(upload.with.archive).toBe(false)
    expect(upload.with.path).toBe(expression('steps.archive.outputs.archive'))
    expect(upload.with['include-hidden-files']).toBe(true)
    expect(upload.with['retention-days']).toBeGreaterThan(0)
    expect(upload.with['retention-days']).toBeLessThanOrEqual(14)
    const archive = action.runs.steps.find(step => step.id === 'archive')
    expect(archive.if).toBe('always()')
    expect(archive.run).toBe('node scripts/archive-test-results.mjs')
    expect(archive.env.TEST_RESULTS_ARCHIVE_NAME).toContain(expression('strategy.job-index || \'0\''))
    expect(archive.env.TEST_RESULTS_ARCHIVE_NAME).toEndWith('.tar.gz')
    for (const context of [
      'github.job',
      'runner.os',
      'runner.arch',
      'github.run_attempt',
    ])
      expect(archive.env.TEST_RESULTS_ARCHIVE_NAME).toContain(expression(context))
  })

  it.each(['linux', 'macos', 'windows'])('monitors the %s runner on request, from before the cache restorations to the end of the job', (name) => {
    const job = readYaml('.github/workflows/ci.yaml').jobs[name]
    const steps = job.steps
    const monitor = 'bun scripts/ci-resource-monitor.mjs'
    const stop = steps.findIndex(step => step.uses === './.github/actions/ci-resource-monitor' && step.with.mode === 'stop')
    const setupIndex = steps.findIndex(step => step.uses === './.github/actions/setup-build')
    const setup = steps[setupIndex]
    const tests = steps.findIndex(step => /\btask test(?:-native(?:-linux)?)?$/.test(step.run ?? ''))
    // The job keeps only the stop step; the start moved into the setup.
    expect(steps.filter(step => step.uses === './.github/actions/ci-resource-monitor')).toHaveLength(1)
    expect(stop).toBe(steps.length - 1)
    expect(tests).toBeGreaterThan(setupIndex)
    // The repository variable CI_RESOURCE_MONITOR switches the monitor on,
    // and the setup starts it before the APT installation and the caches.
    expect(setup.with['monitor-resources']).toBe(expression('vars.CI_RESOURCE_MONITOR == \'true\''))
    const setupSteps = readYaml('.github/actions/setup-build/action.yaml').runs.steps
    const bunSetup = setupSteps.findIndex(step => step.name === 'Setup Bun')
    const start = setupSteps.findIndex(step => step.name === 'Start the resource monitor')
    const aptRestore = setupSteps.findIndex(step => step.id === 'apt-restore')
    expect(bunSetup).toBeGreaterThan(0)
    expect(start).toBe(bunSetup + 1)
    expect(aptRestore).toBeGreaterThan(start)
    expect(setupSteps[start].if).toBe('inputs.monitor-resources == \'true\'')
    expect(setupSteps[start].with.mode).toBe('start')
    // The stop step runs last and after a failure, so it prints the log of every job that GitHub keeps.
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
