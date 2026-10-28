import { spawnSync } from 'node:child_process'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { delimiter, join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import { NOTICE_INPUTS } from './notice-inputs.mjs'

const root = resolve(import.meta.dirname, '..')
const fixtures = []

afterEach(() => {
  for (const dir of fixtures.splice(0)) rmSync(dir, { recursive: true, force: true })
})

function fixture() {
  mkdirSync(join(root, '.tmp'), { recursive: true })
  const dir = mkdtempSync(join(root, '.tmp', 'actions-test-'))
  fixtures.push(dir)
  return dir
}

// Put the directory of fake commands first INSIDE the script. A fake
// directory at the front of the spawn PATH is not enough: Git for Windows'
// bin\bash.exe launcher puts its own mingw64\bin and usr\bin before the PATH
// that it receives. A fake for a command that Git ships, such as `date` or
// `base64`, then loses to the real command. cygpath converts the Windows path
// for MSYS bash. Other platforms have no cygpath and use the path as it is.
const fakeBinPrelude = 'PATH="$(cygpath -u "$ACTIONS_TEST_FAKE_BIN" 2>/dev/null || printf \'%s\' "$ACTIONS_TEST_FAKE_BIN"):$PATH"\n'

function runShell(script, dir, extra = {}, fakeBin) {
  const prelude = fakeBin === undefined ? '' : fakeBinPrelude
  return spawnSync('bash', ['--noprofile', '--norc', '-e', '-o', 'pipefail', '-c', prelude + script], {
    cwd: dir,
    encoding: 'utf8',
    env: {
      ...process.env,
      GITHUB_ENV: join(dir, 'environment'),
      GITHUB_OUTPUT: join(dir, 'output'),
      ...(fakeBin === undefined ? {} : { ACTIONS_TEST_FAKE_BIN: fakeBin }),
      ...extra,
    },
  })
}

// GitHub expressions remain literal text until the runner evaluates the workflow.
function expression(body) {
  return `\${{ ${body} }}`
}

function outputFile(dir, file = 'output') {
  const path = join(dir, file)
  return existsSync(path) ? readFileSync(path, 'utf8') : ''
}

function git(dir, ...args) {
  const result = spawnSync('git', args, { cwd: dir, encoding: 'utf8' })
  expect(result.status, result.stdout + result.stderr).toBe(0)
  return result.stdout.trim()
}

function initGit(dir, bare = false) {
  mkdirSync(dir, { recursive: true })
  git(dir, 'init', '--initial-branch=main', ...(bare ? ['--bare'] : []))
  for (const [key, value] of Object.entries({ 'core.fsmonitor': 'false', 'gc.auto': '0', 'maintenance.auto': 'false', 'commit.gpgsign': 'false', 'tag.gpgsign': 'false', 'user.name': 'Test', 'user.email': 'test@example.invalid' })) {
    git(dir, 'config', key, value)
  }
}

function yaml(relPath) {
  return Bun.YAML.parse(readFileSync(join(root, relPath), 'utf8'))
}

function raw(relPath) {
  return readFileSync(join(root, relPath), 'utf8')
}

const workflowFiles = readdirSync(join(root, '.github/workflows'))
  .filter(name => name.endsWith('.yaml'))
  .map(name => `.github/workflows/${name}`)

const actionFiles = readdirSync(join(root, '.github/actions'), { withFileTypes: true })
  .filter(entry => entry.isDirectory())
  .map(entry => `.github/actions/${entry.name}/action.yaml`)

function jobSteps(doc) {
  const out = []
  for (const [jobName, job] of Object.entries(doc.jobs ?? {})) {
    for (const step of job.steps ?? []) out.push({ jobName, step })
  }
  return out
}

/** Every step of a workflow or of a composite action (`runs.steps`). */
function allSteps(doc) {
  if (doc.jobs)
    return jobSteps(doc)
  return (doc.runs?.steps ?? []).map(step => ({ jobName: '', step }))
}

/** The steps of a composite action. */
function actionSteps(relPath) {
  return yaml(relPath).runs?.steps ?? []
}

/** The lines of one Dockerfile stage, keyed by its `AS <name>`. */
function dockerStage(name) {
  const lines = raw('docker/Dockerfile').split('\n')
  const starts = []
  for (const [index, line] of lines.entries()) {
    const match = line.match(/^FROM(?:\s+--platform=\S+)?\s+\S+(?:\s+AS\s+(\S+))?\s*$/i)
    if (match)
      starts.push({ name: match[1], index })
  }
  const stage = starts.find(entry => entry.name === name)
  expect(stage).toBeDefined()
  const next = starts.find(entry => entry.index > stage.index)
  return lines.slice(stage.index + 1, next ? next.index : lines.length)
}

function argLine(lines, arg) {
  return lines.findIndex(line => new RegExp(`^ARG\\s+${arg}(\\s|=|$)`).test(line))
}

function cachePaths(step) {
  return String(step.with.path).split('\n').map(line => line.trim()).filter(Boolean)
}

describe('runShell', () => {
  it('runs a fake command before the system command of the same name', () => {
    const dir = fixture()
    const bin = join(dir, 'bin')
    mkdirSync(bin)
    writeFileSync(join(bin, 'date'), '#!/bin/sh\nprintf "fake date\\n"\n')
    chmodSync(join(bin, 'date'), 0o755)
    // Start the shell with the system directories first, as Git for Windows'
    // bin\bash.exe launcher does with its own usr\bin. On Windows that launcher
    // reorders PATH by itself.
    const systemFirst = process.platform === 'win32'
      ? process.env.PATH
      : ['/usr/bin', '/bin', process.env.PATH].join(delimiter)
    const result = runShell('date', dir, { PATH: systemFirst }, bin)
    expect(result.status, result.stderr).toBe(0)
    expect(result.stdout).toBe('fake date\n')
  })
})

describe('cache and pin invariants of the GitHub Actions workflows', () => {
  it('runs container build tools on the builder platform and cross-compiles the requested target', () => {
    const dockerfile = raw('docker/Dockerfile')
    for (const stage of ['node', 'tools']) {
      const declaration = dockerfile.split('\n').find(line => new RegExp(`\\sAS ${stage}$`).test(line))
      expect(declaration).toContain(`--platform=\${BUILDPLATFORM}`)
    }
    expect(dockerfile).toContain(`FROM \${BASE_IMAGE}`)
    expect(dockerfile).toContain(`GOOS=\${TARGETOS} GOARCH=\${TARGETARCH}`)
    expect(String(yaml('Taskfile.yaml').tasks['build-backend-docker'].cmds)).toContain('CGO_ENABLED=0')
  })
  it('uses the prepared builder for every Docker cache and build operation', () => {
    const steps = actionSteps('.github/actions/build-docker-image/action.yaml')
    const operations = steps.filter(step => String(step.uses).startsWith('docker/build-push-action@') || String(step.uses).startsWith('reproducible-containers/buildkit-cache-dance@'))
    expect(operations).toHaveLength(4)
    for (const step of operations) expect(step.with.builder).toBe(expression('inputs.builder'))
  })

  it('supplies each local action input and reads only its declared outputs', () => {
    for (const file of [...workflowFiles, ...actionFiles]) {
      const steps = allSteps(yaml(file))
      for (const { jobName, step } of steps) {
        if (!String(step.uses).startsWith('./.github/actions/'))
          continue
        const target = yaml(`${step.uses.slice(2)}/action.yaml`)
        for (const name of Object.keys(step.with ?? {})) {
          expect(target.inputs, `${file}: ${step.name}: ${name}`).toHaveProperty(name)
        }
        for (const [name, input] of Object.entries(target.inputs ?? {})) {
          if (input.required && input.default === undefined)
            expect(step.with, `${file}: ${step.name}: ${name}`).toHaveProperty(name)
        }
        if (!step.id)
          continue
        const pattern = new RegExp(`steps\\.${step.id}\\.outputs\\.([\\w-]+)`, 'g')
        for (const sibling of steps.filter(entry => entry.jobName === jobName)) {
          for (const match of JSON.stringify(sibling.step).matchAll(pattern)) {
            expect(target.outputs, `${file}: ${step.id}: ${match[1]}`).toHaveProperty(match[1])
          }
        }
      }
    }
  })

  it('serializes native and Docker cache writers through their post steps', () => {
    const ci = yaml('.github/workflows/ci.yaml')
    const groups = []
    for (const name of ['linux', 'macos', 'windows']) {
      const job = ci.jobs[name]
      expect(job.permissions).toEqual({ contents: 'read', actions: 'write' })
      expect(job.concurrency['cancel-in-progress']).toBe(false)
      expect(job.concurrency.queue).toBe('max')
      expect(job.concurrency.group).toContain(expression('github.ref'))
      groups.push(job.concurrency.group)
      const writer = job.steps.find(step => step.id === 'go-cache')
      const prune = job.steps.find(step => step.uses === './.github/actions/prune-build-cache')
      expect(writer.if).toBe('github.ref == \'refs/heads/main\'')
      expect(prune.if).toBe('github.ref == \'refs/heads/main\' && steps.go-cache.outputs.cache-hit != \'true\'')
      expect(writer.with.save).toBe('true')
      expect(prune['continue-on-error'] ?? false).toBe(false)
      expect(job.steps.indexOf(prune)).toBeGreaterThan(job.steps.indexOf(writer))
    }
    expect(new Set(groups).size).toBe(3)
    expect(ci.jobs.linux.concurrency.group).toContain(expression('matrix.arch'))
    expect(ci.jobs.docker.permissions).toEqual({ contents: 'read' })
    const docker = yaml('.github/workflows/docker.yaml').jobs.build
    const release = yaml('.github/workflows/release.yaml').jobs.docker
    expect(docker.concurrency).toEqual(release.concurrency)
    expect(docker.concurrency).toEqual({ 'group': `compiled-go-docker-${expression('matrix.variant')}-${expression('matrix.arch')}`, 'cancel-in-progress': false, 'queue': 'max' })
    for (const job of [docker, release]) expect(job.permissions.actions).toBe('write')
    const nativeRestore = actionSteps('.github/actions/setup-build/action.yaml').find(step => step.name === 'Restore Go build outputs')
    expect(nativeRestore.if).toBe('inputs.save-build-caches != \'true\'')
    expect(nativeRestore.with.save).toBe('false')
  })

  for (const [file, stepName] of [
    ['setup-build', 'Locate the Go build caches'],
    ['setup-docker-build', 'Define the build inputs'],
  ]) {
    it(`refreshes the ${file} Go cache key each UTC week`, () => {
      const step = actionSteps(`.github/actions/${file}/action.yaml`).find(entry => entry.name === stepName)
      const keys = []
      const prefixes = []
      for (const week of ['2026-W53', '2027-W01', '2027-W01']) {
        const dir = fixture()
        const bin = join(dir, 'bin')
        mkdirSync(bin)
        for (const [name, body] of Object.entries({
          go: 'printf "%s\\n" "$FIXTURE_GOCACHE"',
          date: 'printf "%s\\n" "$FIXTURE_WEEK"',
        })) {
          writeFileSync(join(bin, name), `#!/bin/sh\n${body}\n`)
          chmodSync(join(bin, name), 0o755)
        }
        const run = step.run.replace(/\$\{\{\s*hashFiles\([^)]*\)\s*\}\}/g, 'a'.repeat(64))
        const result = runShell(run, dir, {
          FIXTURE_GOCACHE: join(dir, 'go-cache'),
          FIXTURE_WEEK: week,
          CACHE_WEEK: week,
          CACHE_IMAGE: 'ubuntu24',
          RUNNER_OS: 'Linux',
          RUNNER_ARCH: 'ARM64',
          GOLANG_VERSION: '1.27.1',
          GITHUB_REF: 'refs/heads/main',
          GITHUB_EVENT_NAME: 'push',
          RUNNER_TEMP: dir,
          COMMIT_HASH: `fixture-${keys.length}`,
          ARCH: 'arm64',
          VARIANT: 'alpine',
          SAVE_CACHES: 'true',
          ALPINE_IMAGE: 'alpine:fixture',
        }, bin)
        expect(result.status, result.stderr).toBe(0)
        const output = outputFile(dir)
        keys.push(/^key=(.*)$/m.exec(output)?.[1])
        prefixes.push(/^restore-prefix=(.*)$/m.exec(output)?.[1])
        expect(keys.at(-1)).toEndWith(`-${week}`)
      }
      expect(keys[0]).not.toBe(keys[1])
      expect(keys[1]).toBe(keys[2])
      expect(new Set(prefixes).size).toBe(1)
      expect(prefixes[0]).not.toContain('2026-W53')
      expect(prefixes[0]).not.toContain('2027-W01')
    })
  }

  it('ends the builder stage at the unnamed runtime stage', () => {
    expect(dockerStage('builder').some(line => line.startsWith('ARG S6_OVERLAY_VERSION'))).toBe(false)
  })

  it('preserves complete diagnostics and continues after command failures', () => {
    const dir = fixture()
    mkdirSync(join(dir, 'scripts'))
    writeFileSync(join(dir, 'scripts', 'diagnose-linuxdeploy.sh'), raw('scripts/diagnose-linuxdeploy.sh'))
    const bin = join(dir, 'bin')
    mkdirSync(bin)
    for (const [name, body] of Object.entries({
      gcc: 'for ((i=0; i<150; i++)); do printf "diagnostic-%s\\n" "$i"; done\nexit 17',
      go: 'printf "CGO fixture\\n"\nexit 18',
    })) {
      writeFileSync(join(bin, name), `#!/usr/bin/env bash\n${body}\n`)
      chmodSync(join(bin, name), 0o755)
    }
    const result = runShell('bash scripts/diagnose-linuxdeploy.sh', dir, { HOME: dir }, bin)
    expect(result.status, result.stderr).toBe(0)
    expect(result.stdout).toContain('No AppDir exists')
    expect(result.stdout).toContain('No sidecar exists')
    const logs = join(dir, '.tmp', 'linuxdeploy-diagnostics')
    const compiler = readFileSync(join(logs, 'gcc-version.log'), 'utf8')
    expect(compiler).toContain('diagnostic-0\n')
    expect(compiler).toContain('diagnostic-149\n')
    expect(compiler).toEndWith('EXIT: 17\n')
    expect(readFileSync(join(logs, 'cgo.log'), 'utf8')).toBe('CGO fixture\nEXIT: 18\n')
    expect(readFileSync(join(logs, 'arm64-loader.log'), 'utf8')).toMatch(/EXIT: \d+\n$/)
  })
  it('restores build outputs before Docker extracts and saves them', () => {
    const helper = actionSteps('.github/actions/build-output-cache/action.yaml')
    expect(helper).toHaveLength(2)
    expect(helper.map(step => step.if)).toEqual(['inputs.save == \'true\'', 'inputs.save != \'true\''])
    for (const step of helper) {
      expect(step.with).toEqual({ 'path': expression('inputs.path'), 'key': expression('inputs.key'), 'restore-keys': expression('inputs.restore-keys') })
    }
    expect(helper[0].uses).toStartWith('actions/cache@')
    expect(helper[1].uses).toStartWith('actions/cache/restore@')
    const buildSteps = actionSteps('.github/actions/build-docker-image/action.yaml')
    expect(buildSteps.some(step => step.uses === './.github/actions/build-output-cache')).toBe(false)
    expect(buildSteps.some(step => step.uses === './.github/actions/prune-build-cache')).toBe(false)
    const pruning = yaml('.github/actions/prune-build-cache/action.yaml')
    expect(pruning.runs['post-if']).toBe('success()')
    expect(pruning.runs.using).toBe('node24')
    for (const phase of ['main', 'post']) {
      expect(existsSync(resolve(root, '.github/actions/prune-build-cache', pruning.runs[phase]))).toBe(true)
    }
    for (const [file, jobName] of [['ci.yaml', 'docker'], ['docker.yaml', 'build'], ['release.yaml', 'docker']]) {
      const steps = yaml(`.github/workflows/${file}`).jobs[jobName].steps
      const setupIndex = steps.findIndex(step => step.uses === './.github/actions/setup-docker-build')
      const cacheIndex = steps.findIndex(step => step.uses === './.github/actions/build-output-cache')
      const pruneIndex = steps.findIndex(step => step.uses === './.github/actions/prune-build-cache')
      const buildIndex = steps.findIndex(step => step.uses === './.github/actions/build-docker-image')
      expect(setupIndex).toBeGreaterThanOrEqual(0)
      expect(cacheIndex).toBeGreaterThan(setupIndex)
      expect(pruneIndex).toBeGreaterThan(cacheIndex)
      expect(buildIndex).toBeGreaterThan(pruneIndex)
      expect(steps[pruneIndex]['continue-on-error'] ?? false).toBe(false)
      expect(steps[buildIndex]['continue-on-error'] ?? false).toBe(false)
      expect(steps[pruneIndex].if).toBe('steps.setup.outputs.writes-shared == \'true\' && steps.go-cache.outputs.cache-hit != \'true\'')
      expect(steps[cacheIndex].with.save).toBe(expression('steps.setup.outputs.writes-shared'))
      expect(steps[cacheIndex].with.key).toBe(expression('steps.setup.outputs.cache-key'))
      expect(steps[pruneIndex].with).toEqual({ key: expression('steps.setup.outputs.cache-key'), prefix: expression('steps.setup.outputs.cache-prefix') })
      expect(steps[buildIndex].with['go-cache-hit']).toBe(expression('steps.go-cache.outputs.cache-hit'))
      expect(steps[buildIndex].with['cache-path']).toBe(steps[cacheIndex].with.path)
      expect(steps[buildIndex].with.builder).toBe(expression('steps.setup.outputs.builder'))
      expect(steps[buildIndex].with['build-args']).toBe(expression('steps.setup.outputs.build-args'))
    }
  })

  it('uses a Cargo lookup before an exact Rust cache hit', () => {
    const steps = actionSteps('.github/actions/setup-build/action.yaml')
    const dependency = steps.find(step => step.id === 'deps')
    const rust = steps.find(step => step.id === 'rust-cache')
    const fallback = steps.find(step => step.name === 'Restore Cargo downloads after a Rust cache miss')
    expect(dependency.with['cargo-lookup-only']).toBe(expression('inputs.setup-desktop == \'true\''))
    expect(fallback.if).toBe('inputs.setup-desktop == \'true\' && steps.deps.outputs.cargo-cache-hit == \'true\' && steps.rust-cache.outputs.cache-hit != \'true\'')
    expect(fallback.with).toEqual({ go: 'false', bun: 'false', cargo: 'true', populate: 'false' })
    expect(steps.indexOf(dependency)).toBeLessThan(steps.indexOf(rust))
    expect(steps.indexOf(rust)).toBeLessThan(steps.indexOf(fallback))
    const cargoRestore = actionSteps('.github/actions/dependency-caches/action.yaml').find(step => step.id === 'cargo')
    expect(cargoRestore.with['lookup-only']).toBe(expression('inputs.cargo-lookup-only'))
  })

  it('sets one shared Cargo debug level before cache restoration', () => {
    const steps = actionSteps('.github/actions/setup-build/action.yaml')
    const setting = steps.find(step => step.name === 'Set the Cargo debug information level')
    expect(steps.indexOf(setting)).toBeLessThan(steps.findIndex(step => step.id === 'rust-cache'))
    const dir = fixture()
    expect(runShell(setting.run, dir).status).toBe(0)
    expect(outputFile(dir, 'environment')).toBe('CARGO_PROFILE_DEV_DEBUG=line-tables-only\n')
    for (const file of ['ci.yaml', 'desktop-artifacts.yaml']) {
      expect(yaml(`.github/workflows/${file}`).env?.CARGO_PROFILE_DEV_DEBUG).toBeUndefined()
    }
  })

  it('writes validated image digests under their original artifact layout', () => {
    const action = actionSteps('.github/actions/upload-docker-digest/action.yaml')
    const digest = `sha256:${'a'.repeat(64)}`
    for (const variant of ['alpine', 'ubuntu']) {
      for (const arch of ['amd64', 'arm64']) {
        const dir = fixture()
        expect(runShell(action[0].run, dir, { DIGEST: digest, VARIANT: variant, ARCH: arch, RUNNER_TEMP: dir }).status).toBe(0)
        expect(readFileSync(join(dir, 'digests', variant, arch), 'utf8')).toBe(`${digest}\n`)
      }
    }
    for (const invalid of [{ DIGEST: '' }, { DIGEST: `${digest}\ninjected` }, { VARIANT: '../outside' }, { ARCH: 'x86' }]) {
      const dir = fixture()
      expect(runShell(action[0].run, dir, { DIGEST: digest, VARIANT: 'alpine', ARCH: 'amd64', RUNNER_TEMP: dir, ...invalid }).status).not.toBe(0)
      expect(existsSync(join(dir, 'digests'))).toBe(false)
    }
    expect(action[1].with['retention-days']).toBe(1)
    for (const [file, prefix, job] of [['docker.yaml', 'digest', 'build'], ['release.yaml', 'release-digest', 'docker']]) {
      const caller = yaml(`.github/workflows/${file}`).jobs[job].steps.find(step => step.uses === './.github/actions/upload-docker-digest')
      expect(caller.with['artifact-name']).toBe(`${prefix}-\${{ matrix.variant }}-\${{ matrix.arch }}`)
    }
  })

  it('continues credential cleanup after a keychain deletion failure', () => {
    const dir = fixture()
    const bin = join(dir, 'bin')
    mkdirSync(bin)
    writeFileSync(join(bin, 'security'), '#!/bin/sh\nexit 19\n')
    chmodSync(join(bin, 'security'), 0o755)
    const keychain = join(dir, 'test.keychain')
    const key = join(dir, 'test.p8')
    writeFileSync(keychain, 'fixture')
    writeFileSync(key, 'fixture')
    expect(runShell(raw('scripts/cleanup-apple-signing.sh'), dir, {
      KEYCHAIN_PATH: keychain,
      APPLE_API_KEY_P8_PATH: key,
    }, bin).status).toBe(0)
    expect(existsSync(key)).toBe(false)
    expect(runShell(raw('scripts/cleanup-apple-signing.sh'), dir, { KEYCHAIN_PATH: '', APPLE_API_KEY_P8_PATH: '' }).status).toBe(0)
  })
  it('serializes complete releases and checks out the current main branch', () => {
    const release = yaml('.github/workflows/release.yaml')
    expect(release.concurrency).toEqual({ 'group': 'release', 'cancel-in-progress': false, 'queue': 'max' })
    const checkout = release.jobs.release.steps.find(step => String(step.uses).startsWith('actions/checkout@'))
    expect(checkout.with.ref).toBe('refs/heads/main')
  })

  for (const conflict of ['branch', 'tag', 'none']) {
    it(`publishes the release refs atomically with ${conflict} conflict`, () => {
      const dir = fixture()
      const remote = join(dir, 'remote')
      const local = join(dir, 'release')
      initGit(remote, true)
      initGit(local)
      writeFileSync(join(local, 'versions.env'), 'VERSION=1.2.3-dev\n')
      git(local, 'add', 'versions.env')
      git(local, 'commit', '-m', 'chore: Set the initial version')
      const initial = git(local, 'rev-parse', 'HEAD')
      git(local, 'remote', 'add', 'origin', remote)
      git(local, 'push', '-u', 'origin', 'main')
      writeFileSync(join(local, 'versions.env'), 'VERSION=1.2.3\n')
      git(local, 'commit', '-am', 'chore: Release v1.2.3')
      const releaseCommit = git(local, 'rev-parse', 'HEAD')
      git(local, 'tag', 'v1.2.3')
      writeFileSync(join(local, 'versions.env'), 'VERSION=1.2.4-dev\n')
      git(local, 'commit', '-am', 'chore: Bump version to 1.2.4-dev')
      let expectedMain = initial
      if (conflict === 'branch') {
        const other = join(dir, 'other')
        initGit(other)
        git(other, 'remote', 'add', 'origin', remote)
        git(other, 'fetch', 'origin', 'main')
        git(other, 'reset', '--hard', 'origin/main')
        writeFileSync(join(other, 'unrelated'), 'another writer\n')
        git(other, 'add', 'unrelated')
        git(other, 'commit', '-m', 'test: Add another change')
        git(other, 'push', 'origin', 'main')
        expectedMain = git(other, 'rev-parse', 'HEAD')
      }
      if (conflict === 'tag')
        git(remote, 'update-ref', 'refs/tags/v1.2.3', initial)
      const steps = yaml('.github/workflows/release.yaml').jobs.release.steps
      const push = steps.find(step => step.name === 'Push version commits and release tag')
      const result = runShell(push.run.replaceAll(expression('inputs.version'), '1.2.3'), local, { RELEASE_ADMIN_TOKEN: 'fixture-token' })
      expect(result.status === 0).toBe(conflict === 'none')
      if (conflict === 'none') {
        expect(git(remote, 'rev-parse', 'refs/heads/main')).toBe(git(local, 'rev-parse', 'HEAD'))
        expect(git(remote, 'rev-parse', 'refs/tags/v1.2.3')).toBe(releaseCommit)
        expect(git(remote, 'show', 'main:versions.env')).toBe('VERSION=1.2.4-dev')
        expect(git(remote, 'show', 'v1.2.3:versions.env')).toBe('VERSION=1.2.3')
      }
      else {
        expect(git(remote, 'rev-parse', 'refs/heads/main')).toBe(expectedMain)
        const tag = spawnSync('git', ['rev-parse', '--verify', 'refs/tags/v1.2.3'], { cwd: remote, encoding: 'utf8' })
        if (conflict === 'tag')
          expect(tag.stdout.trim()).toBe(initial)
        else expect(tag.status).not.toBe(0)
      }
    })
  }
  it('keeps the patch fingerprint in every Tauri restore prefix', () => {
    const step = actionSteps('.github/actions/setup-build/action.yaml')
      .find(entry => entry.name === 'Cache Tauri downloads')
    const prefix = step.with['restore-keys']
    for (const file of ['patch-linuxdeploy.sh', 'linuxdeploy-wrapper.c']) {
      expect(prefix).toContain(file)
    }
    expect(prefix).not.toContain('CACHE_WEEK')
    expect(step.with.key).toBe(`${prefix}\${{ env.CACHE_WEEK }}`)
  })

  it('deploys changes to every external Hugo mount and the tool versions', () => {
    const paths = yaml('.github/workflows/publish-site.yaml').on.push.paths
    const matches = file => paths.some(pattern => new Bun.Glob(pattern).match(file))
    for (const mount of yaml('site/hugo.yaml').module.mounts) {
      if (!mount.source.startsWith('../'))
        continue
      expect(matches(`${mount.source.slice(3)}/changed.json`)).toBe(true)
    }
    expect(matches('versions.env')).toBe(true)
    expect(matches('site/content/docs/using/coding-agents.md')).toBe(true)
    expect(matches('backend/cmd/leapmux/main.go')).toBe(false)
  })

  it('requires the exact main branch before checkout or release writes', () => {
    const steps = yaml('.github/workflows/release.yaml').jobs.release.steps
    expect(steps[0].name).toBe('Validate release branch')
    for (const ref of ['refs/heads/main', 'refs/heads/Main', 'refs/heads/MAIN', 'refs/tags/main', 'refs/heads/main-feature', '']) {
      const result = runShell(steps[0].run, fixture(), { GITHUB_REF: ref })
      expect(result.error).toBeUndefined()
      expect(result.status === 0).toBe(ref === 'refs/heads/main')
    }
  })

  it('validates canonical release versions before it derives the next version', () => {
    const steps = yaml('.github/workflows/release.yaml').jobs.release.steps
    const validate = steps.find(step => step.name === 'Validate version format')
    const derive = steps.find(step => step.id === 'next')
    expect(steps.indexOf(validate)).toBeLessThan(steps.findIndex(step => String(step.run).includes('git commit')))
    const cases = [
      ['0.0.0', '0.0.1-dev'],
      ['1.2.8', '1.2.9-dev'],
      ['1.2.77', '1.2.78-dev'],
      ['255.255.65534', '255.255.65535-dev'],
      ...['', '1.2.08', '1.2.077', '01.2.3', '1.02.3', '256.0.0', '0.256.0', '0.0.65535', '1.2.9223372036854775807', '1.2.-1', ' 1.2.3', '1.2.3\n2.3.4', '1.2.$(false)'].map(value => [value, null]),
    ]
    for (const [version, expected] of cases) {
      const dir = fixture()
      const script = `${validate.run}\n${derive.run.replaceAll(expression('inputs.version'), `\${VERSION_INPUT}`)}`
      const result = runShell(script, dir, { VERSION_INPUT: version })
      expect(result.error).toBeUndefined()
      expect(result.status === 0, version).toBe(expected !== null)
      expect(outputFile(dir)).toBe(expected === null ? '' : `dev_version=${expected}\n`)
    }
  })

  it('registers the API key for cleanup before a failed decode', () => {
    const dir = fixture()
    const bin = join(dir, 'bin')
    mkdirSync(bin)
    writeFileSync(join(bin, 'base64'), '#!/bin/sh\nprintf fake-private-prefix\nexit 17\n')
    chmodSync(join(bin, 'base64'), 0o755)
    const steps = actionSteps('.github/actions/apple-signing-setup/action.yaml')
    const register = steps.find(step => String(step.run).includes('KEYCHAIN_PATH=') && !String(step.run).includes('security '))
    const key = steps.find(step => String(step.run).includes('P8='))
    const certificate = steps.find(step => step.name === 'Import Developer ID certificate')
    expect(steps.indexOf(register)).toBeLessThan(steps.indexOf(key))
    expect(steps.indexOf(register)).toBeLessThan(steps.indexOf(certificate))
    const env = { RUNNER_TEMP: dir, APPLE_API_KEY_P8_BASE64: 'fixture' }
    expect(runShell(register.run, dir, env, bin).status).toBe(0)
    const exported = Object.fromEntries(outputFile(dir, 'environment').trim().split('\n').map((line) => {
      const index = line.indexOf('=')
      return [line.slice(0, index), line.slice(index + 1)]
    }))
    const result = runShell(key.run, dir, { ...env, ...exported }, bin)
    expect(result.status).not.toBe(0)
    // The fake decoder wrote this partial output, so the step ran the fake.
    expect(readFileSync(join(dir, 'apple_api.p8'), 'utf8')).toBe('fake-private-prefix')
    if (process.platform !== 'win32')
      expect(statSync(join(dir, 'apple_api.p8')).mode & 0o777).toBe(0o600)
    const cleanup = yaml('.github/workflows/desktop-artifacts.yaml').jobs['macos-app-sign'].steps.find(step => step.name === 'Cleanup')
    const cleanupScript = cleanup.run.startsWith('bash scripts/')
      ? raw(cleanup.run.slice('bash '.length).trim())
      : cleanup.run
    expect(runShell(cleanupScript, dir, { ...env, ...exported }, bin).status).toBe(0)
    expect(existsSync(join(dir, 'apple_api.p8'))).toBe(false)
    for (const job of ['macos-app-sign', 'macos-cli-sign']) {
      const step = yaml('.github/workflows/desktop-artifacts.yaml').jobs[job].steps.find(entry => entry.name === 'Cleanup')
      expect(step.if).toBe('always()')
      expect(step.run).toBe('bash scripts/cleanup-apple-signing.sh')
    }
  })
  it('stores the spinner cache in the directory that Task clones into', () => {
    const step = actionSteps('.github/actions/setup-build/action.yaml')
      .find(entry => entry.name === 'Cache awesome-claude-spinners')
    const path = String(step?.with?.path ?? '')
    const taskSuffix = String(yaml('Taskfile.yaml').vars.SPINNERS_CACHE_DIR.sh).match(/\/\.cache\/[\w-]+/)?.[0]
    expect(taskSuffix).toBe('/.cache/awesome-claude-spinners')
    expect(path.endsWith(taskSuffix)).toBe(true)
  })

  it('builds desktop artifacts on the images that CI tests', () => {
    const ci = yaml('.github/workflows/ci.yaml')
    const desktop = yaml('.github/workflows/desktop-artifacts.yaml')
    for (const jobName of ['linux', 'macos', 'windows']) {
      expect(desktop.jobs[jobName]['runs-on']).toBe(ci.jobs[jobName]['runs-on'])
    }
  })

  it('checks out the supplied release ref in every artifact job', () => {
    const jobs = Object.values(yaml('.github/workflows/desktop-artifacts.yaml').jobs)
    expect(jobs).toHaveLength(5)
    for (const job of jobs) {
      const checkout = job.steps.find(step => String(step.uses).startsWith('actions/checkout@'))
      expect(checkout.with.ref).toBe(expression('inputs.ref'))
    }
  })

  it('saves compiled caches only from the CI platform jobs', () => {
    const savers = []
    for (const file of workflowFiles) {
      for (const { jobName, step } of jobSteps(yaml(file))) {
        if (step.uses !== './.github/actions/setup-build')
          continue
        expect(step.with?.['rust-save-if']).toBeUndefined()
        if ('save-build-caches' in (step.with ?? {})) {
          savers.push({ job: `${file}:${jobName}`, value: step.with['save-build-caches'] })
        }
      }
    }
    expect(savers.map(saver => saver.job).sort()).toEqual([
      '.github/workflows/ci.yaml:linux',
      '.github/workflows/ci.yaml:macos',
      '.github/workflows/ci.yaml:windows',
    ])
    const mainOnlySave = /^\$\{\{ github\.ref == 'refs\/heads\/main' \}\}$/
    for (const saver of savers) {
      expect(saver.value).toMatch(mainOnlySave)
    }
    for (const file of [...workflowFiles, ...actionFiles]) {
      expect(raw(file)).not.toContain('rust-save-if')
    }
  })

  it('keeps per-commit build args out of the toolchain layers', () => {
    const copyIndex = dockerStage('builder').findIndex(line => /^COPY\s+\.\s+\.\s*$/.test(line))
    expect(copyIndex).toBeGreaterThanOrEqual(0)
    for (const arg of ['VERSION', 'COMMIT_HASH', 'COMMIT_TIME']) {
      expect(argLine(dockerStage('builder'), arg)).toBeGreaterThan(copyIndex)
      expect(argLine(dockerStage('tools'), arg)).toBe(-1)
    }
  })

  it('fills only cache mounts that the Dockerfile declares', () => {
    const targets = new Set()
    for (const step of actionSteps('.github/actions/build-docker-image/action.yaml')) {
      if (!String(step.uses ?? '').startsWith('reproducible-containers/buildkit-cache-dance'))
        continue
      for (const target of Object.values(JSON.parse(step.with['cache-map']))) targets.add(target)
    }
    expect(targets.size).toBeGreaterThan(0)
    const mounts = new Set()
    for (const line of dockerStage('builder')) {
      for (const match of line.matchAll(/--mount=type=cache,target=([^,\s]+)/g)) mounts.add(match[1])
    }
    for (const target of targets) expect(mounts.has(target)).toBe(true)
  })

  it('exports only the final-image layers for the Docker image cache', () => {
    const steps = actionSteps('.github/actions/build-docker-image/action.yaml')
    const byName = new Map(steps.map(step => [step.name, step]))
    const tools = byName.get('Build the tools stage')
    expect(tools?.with?.target).toBe('tools')
    expect(String(tools?.with?.['cache-to'] ?? '')).toContain('mode=max')
    const image = byName.get('Build the image')
    expect(String(image?.with?.['cache-to'] ?? '')).toContain('mode=min')
    for (const step of steps) {
      if (String(step.with?.['cache-to'] ?? '').includes('mode=max')) {
        expect(step.name).toBe('Build the tools stage')
      }
    }
  })

  it('allows cache export failures while retaining fatal Docker build failures', () => {
    const steps = actionSteps('.github/actions/build-docker-image/action.yaml')
    const builds = steps.filter(step => String(step.uses).startsWith('docker/build-push-action@'))
    expect(builds).toHaveLength(2)
    for (const step of builds) {
      expect(String(step.with['cache-to'])).toContain('ignore-error=true')
      expect(step['continue-on-error']).not.toBe(true)
    }
  })

  it('keeps cross-OS archives inside the workspace', () => {
    for (const file of [...workflowFiles, ...actionFiles]) {
      for (const { step } of allSteps(yaml(file))) {
        if (String(step.with?.enableCrossOsArchive ?? '') !== 'true')
          continue
        for (const path of cachePaths(step)) {
          expect(path.startsWith('~')).toBe(false)
          expect(path.startsWith('/')).toBe(false)
          expect(path.includes('${{')).toBe(false)
        }
      }
    }
  })

  it('saves only package files in the APT cache', () => {
    for (const file of [...workflowFiles, ...actionFiles]) {
      for (const { step } of allSteps(yaml(file))) {
        if (!String(step.uses ?? '').startsWith('actions/cache'))
          continue
        if (!String(step.with?.path ?? '').toLowerCase().includes('apt'))
          continue
        for (const path of cachePaths(step)) {
          expect(path.endsWith('*.deb')).toBe(true)
        }
      }
    }
  })

  it('leaves Go module caching out of setup-go', () => {
    const step = actionSteps('.github/actions/setup-build/action.yaml')
      .find(entry => entry.name === 'Setup Go')
    expect(String(step?.with?.cache ?? '')).toBe('false')
  })

  it('removes the ambient Rust toolchains before rust-cache computes its key', () => {
    const steps = actionSteps('.github/actions/setup-build/action.yaml')
    const names = steps.map(entry => entry.name)
    const removeIndex = names.indexOf('Remove ambient Rust toolchains')
    const cacheIndex = steps.findIndex(entry =>
      String(entry.uses ?? '').startsWith('Swatinem/rust-cache'))
    expect(removeIndex).toBeGreaterThanOrEqual(0)
    expect(cacheIndex).toBeGreaterThan(removeIndex)
    const run = String(steps[removeIndex].run ?? '')
    expect(run).toContain('rustup default "$RUST_VERSION"')
    expect(run).toContain('rustup toolchain uninstall')
  })

  it('saves each dependency archive right after its full download', () => {
    const steps = actionSteps('.github/actions/dependency-caches/action.yaml')
    const commands = ['go mod download', 'bun install --frozen-lockfile --ignore-scripts', 'cargo fetch --locked']
    const downloaded = new Set()
    for (const [index, step] of steps.entries()) {
      if (!String(step.uses ?? '').startsWith('actions/cache/save'))
        continue
      const download = steps[index - 1]
      const command = commands.find(candidate => String(download?.run ?? '').includes(candidate))
      expect(command).toBeDefined()
      downloaded.add(command)
      expect(step.if).toBe(download.if)
      const key = String(step.with?.key ?? '')
      const match = key.match(/^\$\{\{\s*steps\.(\w+)\.outputs\.cache-primary-key\s*\}\}$/)
      expect(match).not.toBeNull()
      const restore = steps.find(entry => entry.id === match[1])
      expect(String(restore?.uses ?? '').startsWith('actions/cache/restore')).toBe(true)
    }
    for (const command of commands) expect(downloaded.has(command)).toBe(true)
    for (const step of steps) expect(step.with?.['restore-keys']).toBeUndefined()
  })

  it('runs NOTICE again when an action that it uses changes', () => {
    const paths = NOTICE_INPUTS
    const needed = new Set(['.github/actions/setup-build/action.yaml'])
    const queue = [...needed]
    while (queue.length > 0) {
      for (const step of actionSteps(queue.pop())) {
        const uses = String(step.uses ?? '')
        if (!uses.startsWith('./.github/actions/'))
          continue
        const file = `${uses.slice(2)}/action.yaml`
        if (needed.has(file))
          continue
        needed.add(file)
        queue.push(file)
      }
    }
    for (const file of needed) expect(paths).toContain(file)
  })

  it('pins every remote action to a full commit SHA with a version comment', () => {
    const pin = /^\s*(?:-\s*)?uses:\s*[\w.-]+\/[\w./-]+@[0-9a-f]{40}\s+#\s*\S+\s*$/
    const violations = []
    for (const file of [...workflowFiles, ...actionFiles]) {
      for (const line of raw(file).split('\n')) {
        const match = line.match(/^\s*(?:-\s*)?uses:\s*(\S+)/)
        if (!match)
          continue
        if (line.trimStart().startsWith('#'))
          continue
        if (match[1].startsWith('./') || match[1].startsWith('/'))
          continue
        if (!pin.test(line))
          violations.push(`${file}: ${line.trim()}`)
      }
    }
    expect(violations).toEqual([])
  })

  it('covers each workflow and composite action with Renovate', () => {
    const config = JSON.parse(raw('renovate.json'))
    const patterns = config['github-actions'].managerFilePatterns.map(pattern => new RegExp(pattern.slice(1, -1)))
    expect(config.enabledManagers).toContain('github-actions')
    for (const file of [...workflowFiles, ...actionFiles])
      expect(patterns.some(pattern => pattern.test(file)), file).toBe(true)
  })
})

describe('renovate workflow and protection', () => {
  it('builds the proposed Hugo and Hextra site before Required CI can succeed', () => {
    const linux = yaml('.github/workflows/ci.yaml').jobs.linux
    const site = linux.steps.find(step => step.run === 'task site')
    expect(site).toBeDefined()
    expect(site.if).toBe('matrix.arch == \'amd64\'')
    expect(site['continue-on-error']).toBeUndefined()
    const checkout = linux.steps.find(step => String(step.uses).startsWith('actions/checkout@'))
    expect(checkout.with['fetch-depth']).toBe(expression('matrix.arch == \'amd64\' && \'0\' || \'1\''))
    const noticeCheckout = yaml('.github/workflows/notice.yaml').jobs.check.steps.find(step => String(step.uses).startsWith('actions/checkout@'))
    expect(noticeCheckout.with['fetch-depth']).toBe(1)
    expect(noticeCheckout.with['fetch-tags']).toBe(false)
  })
  it('excludes installed JavaScript dependencies from every Docker context location', () => {
    const patterns = raw('.dockerignore').split('\n').filter(line => line.includes('node_modules')).map(line => `${line.replace(/\/$/, '')}/**`)
    for (const file of ['frontend/node_modules/bun/package.json', '.github/renovate/node_modules/renovate/package.json', 'tools/nested/node_modules/package/index.js'])
      expect(patterns.some(pattern => new Bun.Glob(pattern).match(file)), file).toBe(true)
  })
  it('installs and validates before it creates installation tokens', () => {
    const workflow = yaml('.github/workflows/renovate.yaml')
    const steps = workflow.jobs.renovate.steps
    const position = name => steps.findIndex(step => step.name === name)
    expect(workflow.permissions).toEqual({ contents: 'read' })
    expect(workflow.jobs.renovate.env.LOG_FORMAT).toBe('json')
    expect(workflow.concurrency).toEqual({ 'group': 'renovate', 'cancel-in-progress': false, 'queue': 'max' })
    expect(workflow.on.workflow_dispatch.inputs.mode.default).toBe('dry-run')
    expect(workflow.jobs.renovate.if).toContain('github.repository == \'leapmux/leapmux\'')
    for (const name of ['Install pinned Renovate without scripts', 'Validate configuration', 'Compile trusted import helper', 'Validate PR detail renderer'])
      expect(position(name)).toBeLessThan(position('Create read-only installation token'))
    expect(position('Verify main protection before publishing')).toBeLessThan(position('Create publishing installation token'))
    const protection = steps.find(step => step.name === 'Verify main protection before publishing')
    expect(protection.env.RENOVATE_PROTECTION_SNAPSHOT).toBe(expression('vars.RENOVATE_PROTECTION_SNAPSHOT'))
    expect(steps.filter(step => step.env?.RENOVATE_PROTECTION_SNAPSHOT)).toEqual([protection])
    expect(steps.find(step => step.uses === './.github/actions/setup-build').with['setup-desktop']).toBe('false')
    const read = steps.find(step => step.id === 'read')
    const write = steps.find(step => step.id === 'write')
    for (const [key, value] of Object.entries(read.with)) {
      if (key.startsWith('permission-'))
        expect(value).toBe('read')
    }
    expect(write.with['permission-administration']).toBe('read')
    expect(write.with['permission-workflows']).toBe('write')
    for (const step of [read, write]) {
      expect(step.with['client-id']).toBe(expression('vars.RENOVATE_APP_CLIENT_ID'))
      expect(step.with['app-id']).toBeUndefined()
      expect(step.with.owner).toBe('leapmux')
      expect(step.with.repositories).toBe('leapmux')
      expect(step.with['permission-organization-members']).toBeUndefined()
      expect(step.with['permission-merge-queues']).toBeUndefined()
    }
    const dry = steps.find(step => step.name === 'Run read-only dry run')
    expect(dry['timeout-minutes']).toBe(50)
    expect(steps.find(step => step.name === 'Publish dependency updates')['timeout-minutes']).toBe(50)
    expect(dry.env.RENOVATE_TOKEN).toBe(expression('steps.read.outputs.token'))
    expect(dry.run).toBe('task renovate-dry-run')
    const dryEnvironment = yaml('Taskfile.yaml').tasks['renovate-dry-run'].env
    expect(dryEnvironment.RENOVATE_DRY_RUN).toBe('full')
    expect(JSON.parse(dryEnvironment.RENOVATE_FORCE)).toEqual({ schedule: [] })
    const rebase = steps.find(step => step.name === 'Measure representative rebases')
    expect(rebase.if).toBe(dry.if)
    expect(rebase.run).toBe('task renovate-rebase-benchmark')
    expect(rebase['timeout-minutes']).toBe(10)
    expect(rebase.env?.RENOVATE_TOKEN).toBeUndefined()
    expect(rebase.env?.GH_TOKEN).toBeUndefined()
    expect(read.with['permission-vulnerability-alerts']).toBe('read')
    const comments = steps.find(step => step.name === 'Publish PR detail comments')
    expect(position(comments.name)).toBeGreaterThan(position('Publish dependency updates'))
    expect(comments.if).toContain('steps.write.outcome == \'success\'')
    expect(comments.if).toContain('!cancelled()')
    expect(comments.env.GH_TOKEN).toBe(expression('steps.write.outputs.token'))
    expect(comments.run).toBe('task publish-renovate-details')
    const renderer = steps.find(step => step.name === 'Validate PR detail renderer')
    expect(renderer.run).toBe('task test-scripts -- ./scripts/renovate-pr-details.test.mjs')
    expect(renderer.env?.GH_TOKEN).toBeUndefined()
    expect(steps.some(step => step.name === 'Validate PR detail comments')).toBe(false)
  })
  it('rejects publishing before activation and rejects every ref except upstream main', () => {
    const step = yaml('.github/workflows/renovate.yaml').jobs.renovate.steps[0]
    for (const [repository, ref, mode, ready, expected] of [
      ['leapmux/leapmux', 'refs/heads/main', 'dry-run', 'false', true],
      ['leapmux/leapmux', 'refs/heads/main', 'publish', 'false', false],
      ['leapmux/leapmux', 'refs/heads/main', 'publish', 'true', true],
      ['fork/leapmux', 'refs/heads/main', 'dry-run', 'true', false],
      ['leapmux/leapmux', 'refs/heads/Main', 'dry-run', 'true', false],
      ['leapmux/leapmux', 'refs/tags/main', 'publish', 'true', false],
      ['leapmux/leapmux', 'refs/heads/main', 'invalid', 'true', false],
    ]) {
      const result = runShell(step.run, fixture(), { GITHUB_REPOSITORY: repository, GITHUB_REF: ref, MODE: mode, PUBLISH_READY: ready })
      expect(result.status === 0).toBe(expected)
    }
  })
  it('keeps the administrator credential in the release environment and atomic push only', () => {
    const release = yaml('.github/workflows/release.yaml').jobs.release
    expect(release.environment).toBe('release-admin')
    const checkout = release.steps.find(step => String(step.uses).startsWith('actions/checkout@'))
    expect(checkout.with['persist-credentials']).toBe(false)
    const readers = release.steps.filter(step => JSON.stringify(step).includes('secrets.RELEASE_ADMIN_TOKEN'))
    expect(readers.map(step => step.name)).toEqual(['Push version commits and release tag'])
    for (const step of release.steps) {
      expect(String(step.run ?? '')).not.toMatch(/(?:bun install|go tool|task |cargo )/)
    }
    for (const file of workflowFiles.filter(file => file !== '.github/workflows/release.yaml'))
      expect(raw(file)).not.toContain('RELEASE_ADMIN_TOKEN')
    expect(release.steps.find(step => step.name === 'Create GitHub Release (draft)').env.GH_TOKEN).toBe(expression('secrets.GITHUB_TOKEN'))
  })
  it('reports required checks for failures and cancellations and builds both container variants', () => {
    const ci = yaml('.github/workflows/ci.yaml')
    expect(ci.jobs.docker.strategy.matrix.variant).toEqual(['alpine', 'ubuntu'])
    for (const step of ci.jobs.docker.steps.filter(step => String(step.uses).includes('docker-build') || String(step.uses).includes('build-docker-image')))
      expect(step.with.variant).toBe(expression('matrix.variant'))
    expect(ci.jobs.linux.steps.find(step => step.run === 'task lint').if).toBe('matrix.arch == \'amd64\'')
    expect(ci.jobs.linux.steps.find(step => step.run === 'task validate-standalone-tools').if).toBe('matrix.arch == \'amd64\'')
    expect(ci.jobs.linux.steps.find(step => step.run === 'task renovate-extract').if).toBe('matrix.arch == \'amd64\'')
    expect(yaml('Taskfile.yaml').tasks['validate-standalone-tools'].cmds).toEqual([
      'bunx node-gyp@{{.NODE_GYP_VERSION}} --version',
      'bunx wrangler@{{.WRANGLER_VERSION}} --version',
    ])
    const required = ci.jobs['required-ci']
    expect(required.name).toBe('Required CI')
    expect(required.if).toBe('always()')
    expect(required.needs).toEqual(['linux', 'macos', 'windows', 'docker'])
    for (const result of ['success', 'failure', 'cancelled', 'skipped']) {
      const needs = Object.fromEntries(required.needs.map(job => [job, { result: job === 'linux' ? result : 'success' }]))
      expect(runShell(required.steps[0].run, fixture(), { RESULTS: JSON.stringify(needs), EVENT_NAME: 'pull_request' }).status === 0).toBe(result === 'success')
    }
    const notice = yaml('.github/workflows/notice.yaml')
    expect(notice.on.pull_request.paths).toBeUndefined()
    expect(notice.on.push.paths).toBeUndefined()
    expect(notice.jobs.check.name).toBe('NOTICE')
    expect(notice.jobs.check.steps.find(step => step.name === 'Regenerate NOTICE').if).toBe('steps.inputs.outputs.needed == \'true\'')
  })
})
