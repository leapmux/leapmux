import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { appendFileSync, cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, renameSync, rmSync, utimesSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import { ALL_ICON_FILES, ICO_FILE, TRAY_ICON_FILES } from '../desktop/rust/scripts/icon-set.mjs'

const root = resolve(import.meta.dirname, '..')
const scratch = join(root, '.tmp')
const fixtures = []
const hostPlatform = {
  os: process.platform === 'win32' ? 'windows' : process.platform,
  arch: process.arch === 'x64' ? 'amd64' : process.arch,
}
const platforms = [
  { os: 'darwin', arch: 'arm64', triple: 'aarch64-apple-darwin', bundleArch: 'arm64' },
  { os: 'darwin', arch: 'amd64', triple: 'x86_64-apple-darwin', bundleArch: 'x86_64' },
  { os: 'linux', arch: 'arm64', triple: 'aarch64-unknown-linux-gnu', bundleArch: 'aarch64' },
  { os: 'linux', arch: 'amd64', triple: 'x86_64-unknown-linux-gnu', bundleArch: 'x86_64' },
  { os: 'windows', arch: 'amd64', triple: 'x86_64-pc-windows-msvc', bundleArch: 'x64' },
]

// Substitute only Task's platform functions. Task still evaluates its templates and file patterns.
// The compiler commands are replaced below, so these cases need no foreign toolchains.
function platformTemplates(value, platform) {
  if (typeof value === 'string') {
    return value.replace(/\{\{[\s\S]*?\}\}/g, template => template
      .replace(/\bOS\b/g, JSON.stringify(platform.os))
      .replace(/\bARCH\b/g, JSON.stringify(platform.arch)))
  }
  if (Array.isArray(value))
    return value.map(item => platformTemplates(item, platform))
  if (value && typeof value === 'object')
    return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, platformTemplates(item, platform)]))
  return value
}

function platformOutputs(platform) {
  const executable = platform.os === 'windows' ? '.exe' : ''
  const sidecar = `leapmux-desktop-service-${platform.triple}${executable}`
  switch (platform.os) {
    case 'darwin': return {
      sidecar,
      bundle: ['LeapMux Desktop.app/Contents/MacOS/LeapMux Desktop', 'LeapMux Desktop.app/Contents/Info.plist', `LeapMuxDesktop_1.0.0_${platform.bundleArch}.dmg`],
    }
    case 'linux': return {
      sidecar,
      bundle: ['leapmux-desktop', sidecar, `leapmux-desktop_1.0.0_${platform.arch}.deb`, `leapmux-desktop_1.0.0_${platform.bundleArch}.AppImage`],
    }
    case 'windows': return {
      sidecar,
      bundle: [`LeapMuxDesktop_1.0.0_${platform.bundleArch}.msi`, sidecar, 'desktop/rust/target/release/LeapMuxDesktop.exe'],
    }
    default: throw new Error(`Unsupported fixture platform: ${platform.os}`)
  }
}

function write(dir, path, text = 'input') {
  const target = join(dir, path)
  mkdirSync(dirname(target), { recursive: true })
  writeFileSync(target, text)
}

// Run the real dependency and fingerprint rules with inexpensive build commands.
// The fixture outputs preserve the paths that downstream tasks read.
function fixture(platform = hostPlatform) {
  mkdirSync(scratch, { recursive: true })
  const dir = mkdtempSync(join(scratch, 'task-cache-'))
  fixtures.push(dir)
  const config = platformTemplates(Bun.YAML.parse(readFileSync(join(root, 'Taskfile.yaml'), 'utf8')), platform)
  // Keep the baseline independent of the parent task's development environment.
  config.vars = { ...config.vars, VERSION: '1.0.0', COMMIT_HASH: 'abc123', COMMIT_TIME: 'commit-time', BUILD_TIME: '', BRANCH: 'main', LEAPMUX_DEV: '', NODE_ENV: '', JS_CONTEXT: 'js-test', GO_CONTEXT: 'go-test', SPINNERS_CACHE_DIR: join(dir, 'spinner-cache') }
  delete config.dotenv
  const outputs = {
    'install-frontend-deps': ['frontend/node_modules/package/index.js'],
    'generate-proto': ['backend/generated/proto/agent.pb.go', 'frontend/src/generated/proto/agent_pb.ts'],
    'generate-contracts': ['backend/generated/contracts/wire.go', 'frontend/src/generated/contracts/wire.ts', 'desktop/rust/src/generated/contracts.rs'],
    'fetch-spinners': ['spinner-cache/spinners/default.json'],
    'generate-spinners': ['frontend/src/spinners/default.json'],
    'generate-frontend-icons': ['frontend/public/icons/leapmux-icon.ico', 'frontend/public/icons/leapmux-icon.svg', 'frontend/public/icons/leapmux-icon-192.png', 'frontend/public/icons/leapmux-icon-512.png', 'frontend/public/icons/leapmux-icon-maskable-512.png', 'frontend/public/icons/leapmux-icon-square-apple-touch.png'],
    'copy-frontend-notice': ['frontend/public/NOTICE.html'],
    'generate-sqlc-hub': ['backend/internal/hub/store/sqlite/generated/db/querier.go', 'backend/internal/hub/store/postgres/generated/db/querier.go', 'backend/internal/hub/store/mysql/generated/db/querier.go'],
    'generate-sqlc-worker': ['backend/internal/worker/generated/db/querier.go'],
    'embed-frontend': ['backend/internal/hub/generated/frontend/embed.go', 'backend/internal/hub/generated/frontend/public/index.html', 'backend/internal/hub/generated/frontend/public/app.js'],
    'build-backend': [platform.os === 'windows' ? 'leapmux.exe' : 'leapmux'],
    'build-backend-docker': ['leapmux'],
    'build-frontend': ['frontend/.output/public/index.html', 'frontend/.output/public/app.js', 'frontend/.vinxi/build/client/app.js'],
    'site': ['site/public/index.html'],
  }
  // These are producer outputs, independent of the Taskfile's expected-output patterns.
  // Desktop packaging creates only the selected platform's artifacts.
  const selected = platforms.find(entry => entry.os === platform.os && entry.arch === platform.arch)
  if (selected) {
    const { sidecar, bundle } = platformOutputs(selected)
    outputs['build-desktop-sidecar'] = [`desktop/go/bin/${sidecar}`]
    outputs[`build-desktop-${platform.os}`] = bundle
  }
  outputs['generate-desktop-icons'] = [...ALL_ICON_FILES, ICO_FILE, ...TRAY_ICON_FILES.map(icon => icon.name)]
    .map(file => `desktop/rust/icons/${file}`)
  outputs['prepare-dmg-tools'] = ['frontend/node_modules/macos-alias/build/Release/volume.node']
  for (const [name, task] of Object.entries(config.tasks)) {
    for (const key of ['BUF_CONTEXT', 'RUST_CONTEXT', 'SDK_CONTEXT']) {
      if (task.vars?.[key])
        task.vars[key] = `fixture-${key}`
    }
    const commands = task.cmds ?? []
    const isAction = cmd => typeof cmd === 'string' ? !cmd.includes('scripts/task-state.mjs') : !cmd.task
    const actions = commands.filter(isAction)
    const lastAction = actions.at(-1)
    task.cmds = commands.flatMap((cmd) => {
      if (!isAction(cmd))
        return [cmd]
      return cmd === lastAction ? [`bun fixture.mjs ${name}`] : []
    })
  }
  write(dir, 'Taskfile.yaml', Bun.YAML.stringify(config))
  // Resolve output paths before creating parent directories.
  // Bun 1.4 on Windows rejects recursive mkdir for "." and "..".
  write(dir, 'fixture.mjs', `
import { appendFileSync, mkdirSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
const outputs = ${JSON.stringify(outputs)}
const task = process.argv[2]
appendFileSync('executed', task + '\\n')
for (const path of outputs[task] ?? []) {
  const output = resolve(path)
  mkdirSync(dirname(output), { recursive: true })
  writeFileSync(output, 'generated-' + task)
}
`)
  // Keep the helper at the same relative location as the production task.
  mkdirSync(join(dir, 'scripts'), { recursive: true })
  cpSync(join(root, 'scripts/task-state.mjs'), join(dir, 'scripts/task-state.mjs'))
  for (const path of ['proto/leapmux/v1/agent.proto', 'proto/leapmux/v1/scope.proto', 'buf.yaml', 'buf.gen.yaml', 'contracts/wire.json', 'NOTICE.html', 'frontend/src/app.ts', 'frontend/package.json', 'frontend/bun.lock', 'frontend/scripts/generate-icons.mjs', 'icons/leapmux-icon.svg', 'icons/leapmux-icon-square.svg', 'backend/main.go', 'backend/go.mod', 'backend/go.sum', 'go.work', 'backend/internal/hub/frontend.embed', 'backend/internal/hub/store/sqlite/db/migrations/001.sql', 'backend/internal/worker/db/migrations/001.sql'])
    write(dir, path)
  return dir
}

function run(dir, ...args) {
  write(dir, 'executed', '')
  const result = spawnSync('task', args, {
    cwd: dir,
    encoding: 'utf8',
    env: { ...process.env, TASK_TEMP_DIR: join(dir, '.task') },
  })
  expect(result.error).toBeUndefined()
  expect(result.status, result.stdout + result.stderr).toBe(0)
  return readFileSync(join(dir, 'executed'), 'utf8').trim().split('\n').filter(Boolean)
}

// Execute the real Go command recipes. The compiler stub writes the path passed through -o.
function useGoCommandRecipes(dir, platform) {
  const path = join(dir, 'Taskfile.yaml')
  const config = Bun.YAML.parse(readFileSync(path, 'utf8'))
  const production = platformTemplates(Bun.YAML.parse(readFileSync(join(root, 'Taskfile.yaml'), 'utf8')), platform)
  for (const [target, compilerPath] of [
    ['build-backend', '../compiler.mjs'],
    ['build-desktop-sidecar', '../../compiler.mjs'],
  ]) {
    config.tasks[target].cmds = production.tasks[target].cmds.map(command =>
      typeof command === 'string' ? command.replace('go build', `bun ${compilerPath} ${target}`) : command,
    )
  }
  writeFileSync(path, Bun.YAML.stringify(config))
  write(dir, 'compiler.mjs', `
import { appendFileSync, mkdirSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
const [task, ...args] = process.argv.slice(2)
const index = args.indexOf('-o')
if (index < 0 || !args[index + 1]) throw new Error('The compiler needs an output path')
const output = resolve(args[index + 1])
appendFileSync(new URL('./executed', import.meta.url), task + '\\n')
mkdirSync(dirname(output), { recursive: true })
writeFileSync(output, 'compiled-' + task)
`)
}

afterEach(() => {
  for (const dir of fixtures.splice(0))
    rmSync(dir, { recursive: true, force: true })
})

describe('task build cache', () => {
  it.each(platforms)('reuses a complete $os/$arch build without other platforms\' files', (platform) => {
    const dir = fixture(platform)
    useGoCommandRecipes(dir, platform)
    const first = run(dir, 'build')
    expect(first).toContain(`build-desktop-${platform.os}`)
    for (const other of ['darwin', 'linux', 'windows'].filter(os => os !== platform.os))
      expect(first).not.toContain(`build-desktop-${other}`)
    expect(first.includes('prepare-dmg-tools')).toBe(platform.os === 'darwin')
    expect(existsSync(join(dir, platform.os === 'windows' ? 'leapmux' : 'leapmux.exe'))).toBe(false)
    for (const optional of ['frontend/.npmrc', 'frontend/bunfig.toml', 'desktop/rust/rust-toolchain.toml', '.cargo/config.toml'])
      expect(existsSync(join(dir, optional))).toBe(false)
    expect(run(dir, 'build')).toEqual([])
  })

  it.each(platforms)('repairs each missing $os/$arch artifact and then returns to a cached build', (platform) => {
    const dir = fixture(platform)
    useGoCommandRecipes(dir, platform)
    const { sidecar, bundle } = platformOutputs(platform)
    run(dir, 'build')
    const artifacts = [
      { path: platform.os === 'windows' ? 'leapmux.exe' : 'leapmux', task: 'build-backend' },
      { path: `desktop/go/bin/${sidecar}`, task: 'build-desktop-sidecar' },
      ...bundle.map(path => ({ path, task: `build-desktop-${platform.os}` })),
    ]
    for (const { path, task } of artifacts) {
      rmSync(join(dir, path))
      expect(run(dir, 'build'), path).toContain(task)
      expect(existsSync(join(dir, path))).toBe(true)
      expect(run(dir, 'build'), path).toEqual([])
    }
  })

  it('ignores a consistently absent optional source but detects its creation and removal', () => {
    const dir = fixture()
    run(dir, 'build-frontend')
    expect(run(dir, 'build-frontend')).toEqual([])
    write(dir, 'frontend/.npmrc', '# Optional package-manager configuration\n')
    expect(run(dir, 'build-frontend')).toContain('build-frontend')
    expect(run(dir, 'build-frontend')).toEqual([])
    rmSync(join(dir, 'frontend/.npmrc'))
    expect(run(dir, 'build-frontend')).toContain('build-frontend')
    expect(run(dir, 'build-frontend')).toEqual([])
  })

  it('ignores generated server-function manifests but still repairs changed dependencies', () => {
    const dir = fixture()
    expect(run(dir, 'install-frontend-deps')).toEqual(['install-frontend-deps'])
    const manifest = 'frontend/node_modules/.tanstack-start/server-functions-manifest.json'
    write(dir, manifest, '{}')
    expect(run(dir, 'install-frontend-deps')).toEqual([])
    write(dir, manifest, '{"function":"generated"}')
    expect(run(dir, 'install-frontend-deps')).toEqual([])
    rmSync(join(dir, manifest))
    expect(run(dir, 'install-frontend-deps')).toEqual([])
    write(dir, 'frontend/node_modules/package/index.js', 'changed dependency')
    expect(run(dir, 'install-frontend-deps')).toEqual(['install-frontend-deps'])
    expect(readFileSync(join(dir, 'frontend/node_modules/package/index.js'), 'utf8')).toBe('generated-install-frontend-deps')
    expect(run(dir, 'install-frontend-deps')).toEqual([])
  })

  it.each(['generate-sqlc-hub', 'generate-sqlc-worker', 'build-backend', 'build-backend-docker', 'build-desktop-sidecar', 'lint-backend', 'lint-desktop'])('checks every Go workspace manifest for %s', (target) => {
    const dir = fixture()
    const file = join(dir, 'Taskfile.yaml')
    const config = Bun.YAML.parse(readFileSync(file, 'utf8'))
    const task = config.tasks[target]
    // Isolate this task's input check from its prerequisite producers.
    task.deps = []
    task.generates = []
    task.vars = { ...task.vars, RUST_CONTEXT: 'rust-test', OUTPUTS: [] }
    writeFileSync(file, Bun.YAML.stringify(config))
    expect(run(dir, target)).toEqual([target])
    expect(run(dir, target)).toEqual([])
    for (const input of ['go.work', 'go.work.sum', 'backend/go.mod', 'backend/go.sum', 'desktop/go/go.mod', 'desktop/go/go.sum']) {
      write(dir, input, 'changed workspace manifest')
      expect(run(dir, target), input).toEqual([target])
    }
  })

  it('does no work on the second run or after a timestamp-only change', () => {
    const dir = fixture()
    expect(run(dir, 'build-frontend')).toContain('build-frontend')
    expect(run(dir, 'build-frontend')).toEqual([])
    utimesSync(join(dir, 'frontend/src/app.ts'), new Date(0), new Date(0))
    expect(run(dir, 'build-frontend')).toEqual([])
  })

  it('checks upstream contract changes before accepting the frontend cache', () => {
    const dir = fixture()
    run(dir, 'build-frontend')
    run(dir, 'build-frontend')
    appendFileSync(join(dir, 'contracts/wire.json'), 'changed')
    expect(run(dir, 'build-frontend')).toContain('generate-contracts')
  })

  it('repairs a deleted generated file when other outputs remain', () => {
    const dir = fixture()
    run(dir, 'build-frontend')
    run(dir, 'build-frontend')
    rmSync(join(dir, 'frontend/.output/public/app.js'))
    expect(run(dir, 'build-frontend')).toContain('build-frontend')
    expect(existsSync(join(dir, 'frontend/.output/public/app.js'))).toBe(true)
  })

  it.each(['VERSION=2.0.0', 'COMMIT_HASH=def456', 'BRANCH=feature', 'LEAPMUX_DEV=1', 'BUILD_TIME=explicit'])('invalidates changed build metadata: %s', (option) => {
    const dir = fixture()
    run(dir, 'build-frontend')
    expect(run(dir, 'build-frontend', option)).toContain('build-frontend')
    expect(run(dir, 'build-frontend', option)).toEqual([])
    expect(run(dir, 'build-frontend')).toContain('build-frontend')
  })

  it('does not rebuild frontend assets for a test-only edit', () => {
    const dir = fixture()
    run(dir, 'build-frontend')
    write(dir, 'frontend/src/app.test.ts', 'test change')
    write(dir, 'frontend/tests/e2e/example.spec.ts', 'browser test change')
    expect(run(dir, 'build-frontend')).toEqual([])
  })

  it('rebuilds the site after its Hugo environment changes', () => {
    const dir = fixture()
    const before = process.env.HUGO_BASEURL
    try {
      process.env.HUGO_BASEURL = 'https://first.example/'
      expect(run(dir, 'site')).toEqual(['site'])
      expect(run(dir, 'site')).toEqual([])
      process.env.HUGO_BASEURL = 'https://second.example/'
      expect(run(dir, 'site')).toEqual(['site'])
      expect(run(dir, 'site')).toEqual([])
    }
    finally {
      if (before === undefined)
        delete process.env.HUGO_BASEURL
      else
        process.env.HUGO_BASEURL = before
    }
  })

  it('detects content changes with an unchanged timestamp and source additions and deletions', () => {
    const dir = fixture()
    run(dir, 'build-frontend')
    write(dir, 'frontend/src/app.ts', 'different bytes')
    utimesSync(join(dir, 'frontend/src/app.ts'), new Date(0), new Date(0))
    expect(run(dir, 'build-frontend')).toEqual(['build-frontend'])
    write(dir, 'frontend/src/new.ts')
    expect(run(dir, 'build-frontend')).toEqual(['build-frontend'])
    rmSync(join(dir, 'frontend/src/new.ts'))
    expect(run(dir, 'build-frontend')).toEqual(['build-frontend'])
  })

  it('repairs upstream outputs without rebuilding a consumer of identical bytes', () => {
    const dir = fixture()
    run(dir, 'build-frontend')
    rmSync(join(dir, 'frontend/src/generated/contracts/wire.ts'))
    expect(run(dir, 'build-frontend')).toEqual(['generate-contracts'])
    expect(existsSync(join(dir, 'frontend/src/generated/contracts/wire.ts'))).toBe(true)
  })

  it('ignores an unrelated task edit but rebuilds after its own recipe changes', () => {
    const dir = fixture()
    run(dir, 'build-frontend')
    const file = join(dir, 'Taskfile.yaml')
    const config = Bun.YAML.parse(readFileSync(file, 'utf8'))
    config.tasks.site.cmds.push('echo unrelated')
    writeFileSync(file, Bun.YAML.stringify(config))
    expect(run(dir, 'build-frontend')).toEqual([])
    config.tasks['build-frontend'].env = { BUILD_OPTION: 'changed' }
    writeFileSync(file, Bun.YAML.stringify(config))
    expect(run(dir, 'build-frontend')).toEqual(['build-frontend'])
  })

  it('rebuilds the backend after a migration changes but ignores a test-only edit', () => {
    const dir = fixture()
    expect(run(dir, 'build-backend')).toContain('build-backend')
    expect(run(dir, 'build-backend')).toEqual([])
    write(dir, 'backend/main_test.go', 'test edit')
    expect(run(dir, 'build-backend')).toEqual([])
    write(dir, 'backend/internal/hub/store/sqlite/db/migrations/001.sql', 'changed SQL')
    expect(run(dir, 'build-backend')).toEqual(['generate-sqlc-hub', 'build-backend'])
  })

  it('rechecks the host binary after a Docker build', () => {
    const dir = fixture()
    run(dir, 'build-backend')
    run(dir, 'build-backend-docker')
    expect(run(dir, 'build-backend')).toEqual(hostPlatform.os === 'windows' ? [] : ['build-backend'])
  })

  it('exports the Docker target and CGO options to the Go compiler', () => {
    const dir = fixture()
    const file = join(dir, 'Taskfile.yaml')
    const config = Bun.YAML.parse(readFileSync(file, 'utf8'))
    const production = Bun.YAML.parse(readFileSync(join(root, 'Taskfile.yaml'), 'utf8'))
    const dockerBuild = production.tasks['build-backend-docker'].cmds.find(cmd => typeof cmd === 'string' && cmd.includes('go build'))
    const task = config.tasks['build-backend-docker']
    task.cmds = task.cmds.map(cmd => cmd === 'bun fixture.mjs build-backend-docker' ? dockerBuild.replace('go build', 'bun record-env.mjs') : cmd)
    writeFileSync(file, Bun.YAML.stringify(config))
    write(dir, 'backend/record-env.mjs', `
import { writeFileSync } from 'node:fs'
const { CGO_ENABLED, GOOS, GOARCH } = process.env
writeFileSync('observed.json', JSON.stringify({ CGO_ENABLED, GOOS, GOARCH }))
writeFileSync('../leapmux', 'docker')
`)
    run(dir, 'build-backend-docker')
    expect(JSON.parse(readFileSync(join(dir, 'backend/observed.json'), 'utf8'))).toEqual({ CGO_ENABLED: '0', GOOS: 'linux', GOARCH: 'amd64' })
  })

  it('detects a source rename even when its content stays the same', () => {
    const dir = fixture()
    run(dir, 'build-frontend')
    renameSync(join(dir, 'frontend/src/app.ts'), join(dir, 'frontend/src/moved.ts'))
    expect(run(dir, 'build-frontend')).toEqual(['build-frontend'])
  })

  it.each(['build-frontend', 'build-backend'])('passes build metadata as data through %s', (target) => {
    const dir = fixture()
    run(dir, target)
    const file = join(dir, 'Taskfile.yaml')
    const config = Bun.YAML.parse(readFileSync(file, 'utf8'))
    const production = Bun.YAML.parse(readFileSync(join(root, 'Taskfile.yaml'), 'utf8'))
    const frontend = target === 'build-frontend'
    const commandText = frontend ? 'bun run build' : 'go build'
    const command = production.tasks[target].cmds.find(cmd => typeof cmd === 'string' && cmd.includes(commandText))
    config.tasks[target].cmds = config.tasks[target].cmds.map(cmd =>
      cmd === `bun fixture.mjs ${target}` ? command.replace(commandText, 'bun metadata.mjs') : cmd,
    )
    writeFileSync(file, Bun.YAML.stringify(config))
    const moduleDir = frontend ? 'frontend' : 'backend'
    write(dir, `${moduleDir}/metadata.mjs`, `
import { writeFileSync } from 'node:fs'
writeFileSync('metadata-result', JSON.stringify({ args: process.argv.slice(2), branch: process.env.LEAPMUX_BRANCH }))
`)
    const branch = 'literal$((1+2))$(>injected)'
    run(dir, target, `BRANCH=${branch}`)
    expect(existsSync(join(dir, moduleDir, 'injected'))).toBe(false)
    const result = JSON.parse(readFileSync(join(dir, moduleDir, 'metadata-result'), 'utf8'))
    expect(frontend ? result.branch : result.args.join(' ')).toContain(branch)
  })

  it.each(['missing', 'modified'])('repairs a %s registry dependency file with the real package manager', async (damage) => {
    const dir = fixture()
    const configPath = join(dir, 'Taskfile.yaml')
    const config = Bun.YAML.parse(readFileSync(configPath, 'utf8'))
    const production = Bun.YAML.parse(readFileSync(join(root, 'Taskfile.yaml'), 'utf8'))
    config.tasks['install-frontend-deps'].cmds = production.tasks['install-frontend-deps'].cmds
    writeFileSync(configPath, Bun.YAML.stringify(config))
    write(dir, 'package/package.json', JSON.stringify({ name: 'cache-fixture', version: '1.0.0' }))
    write(dir, 'package/index.js', 'export const value = 42')
    const archive = join(dir, 'dependency.tgz')
    const pack = spawnSync(process.execPath, ['pm', 'pack', '--ignore-scripts', '--filename', archive], {
      cwd: join(dir, 'package'),
      encoding: 'utf8',
    })
    expect(pack.status, pack.stdout + pack.stderr).toBe(0)
    const tarball = readFileSync(archive)
    const shasum = createHash('sha1').update(tarball).digest('hex')
    const registry = Bun.serve({
      hostname: '127.0.0.1',
      port: 0,
      fetch(request) {
        if (new URL(request.url).pathname.endsWith('.tgz'))
          return new Response(tarball)
        return Response.json({
          'name': 'cache-fixture',
          'dist-tags': { latest: '1.0.0' },
          'versions': { '1.0.0': {
            name: 'cache-fixture',
            version: '1.0.0',
            dist: { shasum, tarball: new URL('dependency.tgz', request.url).href },
          } },
        })
      },
    })
    async function execute(command, args, cwd) {
      const child = Bun.spawn([command, ...args], {
        cwd,
        stdout: 'pipe',
        stderr: 'pipe',
        env: { ...process.env, TASK_TEMP_DIR: join(dir, '.task'), BUN_INSTALL_CACHE_DIR: join(dir, 'package-cache') },
      })
      const [code, out, err] = await Promise.all([child.exited, new Response(child.stdout).text(), new Response(child.stderr).text()])
      expect(code, out + err).toBe(0)
    }
    try {
      write(dir, 'frontend/package.json', JSON.stringify({ private: true, dependencies: { 'cache-fixture': '1.0.0' } }))
      write(dir, 'frontend/.npmrc', `registry=${registry.url.href}`)
      rmSync(join(dir, 'frontend/bun.lock'))
      await execute(process.execPath, ['install', '--lockfile-only'], join(dir, 'frontend'))
      await execute('task', ['install-frontend-deps'], dir)
      const installed = join(dir, 'frontend/node_modules/cache-fixture/index.js')
      if (damage === 'missing') {
        rmSync(installed)
      }
      else {
        // Replace the inode so the test cannot modify the package cache through a hard link.
        write(dir, 'replacement', 'broken')
        renameSync(join(dir, 'replacement'), installed)
      }
      await execute('task', ['install-frontend-deps'], dir)
      expect(readFileSync(installed, 'utf8')).toBe('export const value = 42')
    }
    finally {
      registry.stop(true)
    }
  })
})
