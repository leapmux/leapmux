import { spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'

const root = resolve(import.meta.dirname, '..')
const readYaml = path => Bun.YAML.parse(readFileSync(resolve(root, path), 'utf8'))
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
      expect(testCommands[0]).toContain('-- -vet=off -tags integration')
    }
    const linux = readYaml('.github/workflows/ci.yaml').jobs.linux
    for (const arch of ['amd64', 'arm64'])
      expect(linux.strategy.matrix.include.map(entry => entry.arch)).toContain(arch)
    expect(linux.steps.find(step => step.run === 'task test-no-docker')?.if).toBe('matrix.arch == \'arm64\'')
    const armTests = commands(tasks, 'test-no-docker')
    for (const module of ['backend', 'desktop/go']) {
      const lint = commands(tasks, module === 'backend'
        ? 'lint-backend'
        : 'lint-desktop')
      for (const os of ['windows', 'linux', 'darwin']) {
        expect(lint).toContain(`cd ${module} && GOOS=${os} go vet -tags integration ./...`)
        expect(armTests).toContain(`cd ${module} && GOOS=${os} go vet -tags integration ./...`)
      }
    }
  })

  it.each([
    '',
    'frontend-native',
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
      'backend',
      'desktop-go',
      'rust-test',
      'rust-lint',
    ]
    const selected = Object.fromEntries([
      'test-native',
      'test-frontend-native',
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
    expect(native.some(cmd => cmd.includes('cd backend && go tool gotestsum') && cmd.includes('-tags integration'))).toBe(true)
    expect(native.some(cmd => cmd.includes('cd desktop/go && go tool gotestsum'))).toBe(true)
    expect(native).toContain('cd desktop/rust && cargo test --locked')
    expect(native).toContain('cd desktop/rust && cargo clippy --locked --all-targets -- -D warnings')
    expect(native).toContain('cd frontend && bun run test scripts/ tests/e2e/helpers/')
    expect(native.join('\n')).not.toMatch(/golangci-lint|GOOS=|bun run (?:lint|typecheck)|bun test|renovate/)
    expect(native.filter(cmd => cmd.includes('bun run test'))).toEqual(['cd frontend && bun run test scripts/ tests/e2e/helpers/'])
    // Omitting integration-tagged Docker suites also keeps them out of compilation.
    const backend = native.find(cmd => cmd.includes('cd backend && go tool gotestsum'))
    expect(backend).toContain('internal/hub/store/(cockroachdb|mysql|postgres|tidb|yugabytedb)(/|$)')
    expect(native.some(cmd => cmd.includes('build-sidecar.mjs') || cmd.includes('go build'))).toBe(true)
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
})
