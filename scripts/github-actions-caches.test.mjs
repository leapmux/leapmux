import { spawnSync } from 'node:child_process'
import { chmodSync, existsSync, mkdirSync, readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import { delimiter, join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import { cacheKinds, rootActivePath, validateCacheKey } from './cache-policy.mjs'
import { NOTICE_INPUTS } from './notice-inputs.mjs'
import { git, gitRepository, scratchDirectories } from './test-support.mjs'

const root = resolve(import.meta.dirname, '..')
const scratch = scratchDirectories()
afterEach(scratch.cleanup)
const fixture = () => scratch.create('actions-test')

// Put the directory of fake commands first INSIDE the script. A fake
// directory at the front of the spawn PATH is not enough: Git for Windows'
// bin\bash.exe launcher puts its own mingw64\bin and usr\bin before the PATH
// that it receives. A fake for a command that Git ships, such as `date` or
// `base64`, then loses to the real command. cygpath converts the Windows path
// for MSYS bash. Other platforms have no cygpath and use the path as it is.
const fakeBinPrelude = 'PATH="$(cygpath -u "$ACTIONS_TEST_FAKE_BIN" 2>/dev/null || printf \'%s\' "$ACTIONS_TEST_FAKE_BIN"):$PATH"\n'

function runShell(script, dir, extra = {}, fakeBin) {
  const prelude = fakeBin === undefined
    ? ''
    : fakeBinPrelude
  return spawnSync('bash', [
    '--noprofile',
    '--norc',
    '-e',
    '-o',
    'pipefail',
    '-c',
    prelude + script,
  ], {
    cwd: dir,
    encoding: 'utf8',
    env: {
      ...process.env,
      GITHUB_ENV: join(dir, 'environment'),
      GITHUB_OUTPUT: join(dir, 'output'),
      ...(fakeBin === undefined
        ? {}
        : { ACTIONS_TEST_FAKE_BIN: fakeBin }),
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
  return existsSync(path)
    ? readFileSync(path, 'utf8')
    : ''
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
    // Two cache-mount injections, the tools stage, the image, and the Go
    // cache export.
    expect(operations).toHaveLength(5)
    for (const step of operations) expect(step.with.builder).toBe(expression('inputs.builder'))
  })

  it('supplies each local action input and reads only its declared outputs', () => {
    for (const file of [
      ...workflowFiles,
      ...actionFiles,
    ]) {
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
      // Each platform job can publish its resource monitor to a check run.
      expect(job.permissions).toEqual({ contents: 'read', actions: 'write', checks: 'write' })
      expect(job.concurrency['cancel-in-progress']).toBe(false)
      expect(job.concurrency.queue).toBe('max')
      expect(job.concurrency.group).toContain(expression('github.ref'))
      groups.push(job.concurrency.group)
      const prune = job.steps.find(step => step.uses === './.github/actions/prune-build-cache')
      expect(prune['continue-on-error'] ?? false).toBe(false)
    }
    expect(new Set(groups).size).toBe(3)
    expect(ci.jobs.linux.concurrency.group).toContain(expression('matrix.arch'))
    expect(ci.jobs.docker.permissions).toEqual({ contents: 'read' })
    const docker = yaml('.github/workflows/docker.yaml').jobs.build
    const release = yaml('.github/workflows/release.yaml').jobs.docker
    expect(docker.concurrency).toEqual(release.concurrency)
    expect(docker.concurrency).toEqual({ 'group': `compiled-go-docker-${expression('matrix.variant')}-${expression('matrix.arch')}`, 'cancel-in-progress': false, 'queue': 'max' })
    for (const job of [docker, release]) expect(job.permissions.actions).toBe('write')
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
        const run = step.run
          .replaceAll(/\$\{\{\s*hashFiles\([^)]*\)\s*\}\}/g, 'a'.repeat(64))
          .replaceAll('node scripts/cache-snapshot.mjs', `node '${root}/scripts/cache-snapshot.mjs'`)
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
  it('restores the Docker Go snapshot before the build and saves it only after the export', () => {
    const helper = actionSteps('.github/actions/build-output-cache/action.yaml')
    // The restore action is restore-only: publication lives in the save
    // action, so no implicit post step can publish an archive.
    expect(helper).toHaveLength(2)
    expect(helper[0].uses).toStartWith('actions/cache/restore@')
    expect(helper[1].id).toBe('activate')
    expect(helper[1].run).toContain('scripts/cache-snapshot.mjs activate')
    const buildSteps = actionSteps('.github/actions/build-docker-image/action.yaml')
    const dances = buildSteps.filter(step => String(step.uses ?? '').startsWith('reproducible-containers/buildkit-cache-dance'))
    // Injection only: the explicit export stage replaces the dance's
    // automatic post extraction.
    expect(dances).toHaveLength(2)
    for (const dance of dances)
      expect(dance.with['skip-extraction']).toBe(true)
    const exportStep = buildSteps.find(step => step.name === 'Export the Go build cache')
    expect(exportStep.with.target).toBe('go-cache-files')
    expect(exportStep.with['no-cache-filters']).toBe('go-cache-export')
    const exportOutput = ['type=local,dest=${', '{ inputs.cache-path }}'].join('')
    expect(exportStep.with.outputs).toBe(exportOutput)
    expect(exportStep.if).toBe('inputs.writes-shared == \'true\' && inputs.go-cache-hit != \'true\'')
    const testIndex = buildSteps.findIndex(step => step.name === 'Test the image')
    expect(buildSteps.indexOf(exportStep)).toBeGreaterThan(testIndex)
    for (const [file, jobName, reader] of [
      ['ci.yaml', 'docker', true],
      ['docker.yaml', 'build', false],
      ['release.yaml', 'docker', false],
    ]) {
      const steps = yaml(`.github/workflows/${file}`).jobs[jobName].steps
      const setupIndex = steps.findIndex(step => step.uses === './.github/actions/setup-docker-build')
      const restoreIndex = steps.findIndex(step => step.uses === './.github/actions/build-output-cache')
      const buildIndex = steps.findIndex(step => step.uses === './.github/actions/build-docker-image')
      const saveIndex = steps.findIndex(step => step.name === 'Save the Docker Go build outputs snapshot')
      const pruneIndex = steps.findIndex(step => step.uses === './.github/actions/prune-build-cache')
      expect(setupIndex).toBeGreaterThanOrEqual(0)
      expect(restoreIndex).toBeGreaterThan(setupIndex)
      expect(buildIndex).toBeGreaterThan(restoreIndex)
      const restore = steps[restoreIndex]
      expect(restore.with.kind).toBe('docker-go')
      expect(restore.with.key).toBe(expression('steps.setup.outputs.cache-key'))
      if (reader) {
        // The pull-request job is a pure reader with no save and no prune.
        expect(restore.with.writer).toBe(expression('steps.setup.outputs.writes-shared'))
        expect(restore.with['restore-keys']).toBe(expression('steps.setup.outputs.cache-prefix'))
        expect(saveIndex).toBe(-1)
        expect(pruneIndex).toBe(-1)
        continue
      }
      // A writer passes no fallback and publishes after the export, and the
      // pruner follows a ready save only.
      expect(restore.with.writer).toBe(expression('steps.setup.outputs.writes-shared'))
      expect(restore.with['restore-keys']).toBeUndefined()
      expect(saveIndex).toBeGreaterThan(buildIndex)
      expect(steps[saveIndex].with).toEqual({
        kind: 'docker-go',
        key: expression('steps.setup.outputs.cache-key'),
      })
      expect(steps[saveIndex].if).toBe('steps.setup.outputs.writes-shared == \'true\' && steps.go-cache.outputs.cache-hit != \'true\'')
      expect(pruneIndex).toBeGreaterThan(saveIndex)
      expect(steps[pruneIndex].if).toBe('steps.setup.outputs.writes-shared == \'true\' && steps.go-cache-save.outputs.result == \'ready\'')
      expect(steps[pruneIndex].with).toEqual({
        key: expression('steps.setup.outputs.cache-key'),
        prefix: expression('steps.setup.outputs.cache-prefix'),
      })
      const build = steps[buildIndex]
      expect(build.with['go-cache-hit']).toBe(expression('steps.go-cache.outputs.cache-hit'))
      expect(build.with['cache-path']).toBe(expression('steps.setup.outputs.cache-path'))
      expect(build.with.builder).toBe(expression('steps.setup.outputs.builder'))
      expect(build.with['build-args']).toBe(expression('steps.setup.outputs.build-args'))
    }
    // The pruner remains a node action whose post step deletes by ID.
    const pruning = yaml('.github/actions/prune-build-cache/action.yaml')
    expect(pruning.runs['post-if']).toBe('success()')
    expect(pruning.runs.using).toBe('node24')
    for (const phase of ['main', 'post'])
      expect(existsSync(resolve(root, '.github/actions/prune-build-cache', pruning.runs[phase]))).toBe(true)
  })
  it('writes validated image digests under their original artifact layout', () => {
    const action = actionSteps('.github/actions/upload-docker-digest/action.yaml')
    const digest = `sha256:${'a'.repeat(64)}`
    for (const variant of ['alpine', 'ubuntu']) {
      for (const arch of ['amd64', 'arm64']) {
        const dir = fixture()
        expect(runShell(action[0].run, dir, {
          DIGEST: digest,
          VARIANT: variant,
          ARCH: arch,
          RUNNER_TEMP: dir,
        }).status).toBe(0)
        expect(readFileSync(join(dir, 'digests', variant, arch), 'utf8')).toBe(`${digest}\n`)
      }
    }
    for (const invalid of [
      { DIGEST: '' },
      { DIGEST: `${digest}\ninjected` },
      { VARIANT: '../outside' },
      { ARCH: 'x86' },
    ]) {
      const dir = fixture()
      expect(runShell(action[0].run, dir, {
        DIGEST: digest,
        VARIANT: 'alpine',
        ARCH: 'amd64',
        RUNNER_TEMP: dir,
        ...invalid,
      }).status).not.toBe(0)
      expect(existsSync(join(dir, 'digests'))).toBe(false)
    }
    expect(action[1].with['retention-days']).toBe(1)
    for (const [file, prefix, job] of [
      ['docker.yaml', 'digest', 'build'],
      ['release.yaml', 'release-digest', 'docker'],
    ]) {
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
      gitRepository(remote, { bare: true })
      gitRepository(local)
      writeFileSync(join(local, 'versions.env'), 'VERSION=1.2.3-dev\n')
      git(local, ['add', 'versions.env'])
      git(local, ['commit', '-m', 'chore: Set the initial version'])
      const initial = git(local, ['rev-parse', 'HEAD'])
      git(local, ['remote', 'add', 'origin', remote])
      git(local, ['push', '-u', 'origin', 'main'])
      writeFileSync(join(local, 'versions.env'), 'VERSION=1.2.3\n')
      git(local, ['commit', '-am', 'chore: Release v1.2.3'])
      const releaseCommit = git(local, ['rev-parse', 'HEAD'])
      git(local, ['tag', 'v1.2.3'])
      writeFileSync(join(local, 'versions.env'), 'VERSION=1.2.4-dev\n')
      git(local, ['commit', '-am', 'chore: Bump version to 1.2.4-dev'])
      let expectedMain = initial
      if (conflict === 'branch') {
        const other = join(dir, 'other')
        gitRepository(other)
        git(other, ['remote', 'add', 'origin', remote])
        git(other, ['fetch', 'origin', 'main'])
        git(other, ['reset', '--hard', 'origin/main'])
        writeFileSync(join(other, 'unrelated'), 'another writer\n')
        git(other, ['add', 'unrelated'])
        git(other, ['commit', '-m', 'test: Add another change'])
        git(other, ['push', 'origin', 'main'])
        expectedMain = git(other, ['rev-parse', 'HEAD'])
      }
      if (conflict === 'tag')
        git(remote, ['update-ref', 'refs/tags/v1.2.3', initial])
      const steps = yaml('.github/workflows/release.yaml').jobs.release.steps
      const push = steps.find(step => step.name === 'Push version commits and release tag')
      const result = runShell(push.run.replaceAll(expression('inputs.version'), '1.2.3'), local, { RELEASE_ADMIN_TOKEN: 'fixture-token' })
      expect(result.status === 0).toBe(conflict === 'none')
      if (conflict === 'none') {
        expect(git(remote, ['rev-parse', 'refs/heads/main'])).toBe(git(local, ['rev-parse', 'HEAD']))
        expect(git(remote, ['rev-parse', 'refs/tags/v1.2.3'])).toBe(releaseCommit)
        expect(git(remote, ['show', 'main:versions.env'])).toBe('VERSION=1.2.4-dev')
        expect(git(remote, ['show', 'v1.2.3:versions.env'])).toBe('VERSION=1.2.3')
      }
      else {
        expect(git(remote, ['rev-parse', 'refs/heads/main'])).toBe(expectedMain)
        const tag = spawnSync('git', ['rev-parse', '--verify', 'refs/tags/v1.2.3'], { cwd: remote, encoding: 'utf8' })
        if (conflict === 'tag')
          expect(tag.stdout.trim()).toBe(initial)
        else expect(tag.status).not.toBe(0)
      }
    })
  }
  it('deploys the site with the locked wrangler, installed without install scripts', () => {
    const steps = yaml('.github/workflows/publish-site.yaml').jobs.publish.steps
    const index = name => steps.findIndex(step => step.name === name)
    const install = steps[index('Install the locked site tools')]
    const deploy = steps[index('Deploy to Cloudflare Workers')]
    expect(index('Install the locked site tools')).toBeGreaterThan(index('Build site'))
    expect(index('Deploy to Cloudflare Workers')).toBeGreaterThan(index('Install the locked site tools'))
    expect(install).toMatchObject({ 'working-directory': 'site', 'run': 'bun install --frozen-lockfile --ignore-scripts' })
    expect(deploy).toMatchObject({ 'working-directory': 'site', 'run': './node_modules/.bin/wrangler deploy' })
    // The site snapshots replace setup-go's combined cache: downloads and
    // build outputs restore separately, a writer builds from an empty build
    // cache on a miss, and both saves run before the deploy step receives
    // the Cloudflare token.
    const setupGo = steps.find(step => String(step.uses).startsWith('actions/setup-go@'))
    expect(String(setupGo.with.cache)).toBe('false')
    const moduleRestore = steps.find(step => step.name === 'Restore the site module downloads')
    expect(moduleRestore.uses).toBe('./.github/actions/dependency-caches')
    expect(moduleRestore.with).toEqual({
      'go': 'true',
      'go-scope': 'site',
      'bun': 'false',
      'populate': 'true',
    })
    const buildRestore = steps.find(step => step.name === 'Restore the site build outputs')
    expect(buildRestore.uses).toBe('./.github/actions/build-output-cache')
    expect(buildRestore.with.kind).toBe('site-go')
    expect(buildRestore.with.key).toContain('site-go-2-')
    expect(buildRestore.with.key).toContain(expression('hashFiles(\'site/go.mod\', \'site/go.sum\')'))
    const weeklySuffix = ['-${', '{ env.CACHE_WEEK }}'].join('')
    expect(buildRestore.with.key).toEndWith(weeklySuffix)
    expect(buildRestore.with.writer).toBe('true')
    // The workflow runs only on main, so every job is a writer and passes
    // no fallback.
    expect(buildRestore.with['restore-keys']).toBeUndefined()
    const buildSave = steps.find(step => step.name === 'Save the site build outputs snapshot')
    expect(buildSave.uses).toBe('./.github/actions/save-cache-snapshot')
    expect(buildSave.with).toEqual({ kind: 'site-go', key: buildRestore.with.key })
    expect(buildSave.if).toBe('steps.site-go-restore.outputs.cache-hit != \'true\'')
    expect(index('Build site')).toBeGreaterThan(index('Restore the site build outputs'))
    expect(index('Save the site build outputs snapshot')).toBeGreaterThan(index('Build site'))
    expect(deploy.env).toEqual({
      CLOUDFLARE_API_TOKEN: expression('secrets.CLOUDFLARE_API_TOKEN'),
      CLOUDFLARE_ACCOUNT_ID: expression('secrets.CLOUDFLARE_ACCOUNT_ID'),
      WRANGLER_SEND_METRICS: 'false',
    })
    // site/bun.lock pins the whole tree, and the release-age check reads it.
    expect(JSON.parse(raw('site/package.json')).devDependencies.wrangler).toMatch(/^\d+\.\d+\.\d+$/)
  })

  it('builds the macOS disk image tools with the node-gyp that the frontend lockfile pins', () => {
    const task = yaml('Taskfile.yaml').tasks['prepare-dmg-tools']
    expect(task.sources).toContain('frontend/bun.lock')
    expect(task.cmds).toContain('cd frontend/node_modules/macos-alias && node ../node-gyp/bin/node-gyp.js rebuild')
    expect(JSON.parse(raw('frontend/package.json')).devDependencies).toHaveProperty('node-gyp')
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
    for (const ref of [
      'refs/heads/main',
      'refs/heads/Main',
      'refs/heads/MAIN',
      'refs/tags/main',
      'refs/heads/main-feature',
      '',
    ]) {
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
      ...[
        '',
        '1.2.08',
        '1.2.077',
        '01.2.3',
        '1.02.3',
        '256.0.0',
        '0.256.0',
        '0.0.65535',
        '1.2.9223372036854775807',
        '1.2.-1',
        ' 1.2.3',
        '1.2.3\n2.3.4',
        '1.2.$(false)',
      ].map(value => [value, null]),
    ]
    for (const [version, expected] of cases) {
      const dir = fixture()
      const script = `${validate.run}\n${derive.run.replaceAll(expression('inputs.version'), `\${VERSION_INPUT}`)}`
      const result = runShell(script, dir, { VERSION_INPUT: version })
      expect(result.error).toBeUndefined()
      expect(result.status === 0, version).toBe(expected !== null)
      expect(outputFile(dir)).toBe(expected === null
        ? ''
        : `dev_version=${expected}\n`)
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
      return [
        line.slice(0, index),
        line.slice(index + 1),
      ]
    }))
    const result = runShell(key.run, dir, {
      ...env,
      ...exported,
    }, bin)
    expect(result.status).not.toBe(0)
    // The fake decoder wrote this partial output, so the step ran the fake.
    expect(readFileSync(join(dir, 'apple_api.p8'), 'utf8')).toBe('fake-private-prefix')
    if (process.platform !== 'win32')
      expect(statSync(join(dir, 'apple_api.p8')).mode & 0o777).toBe(0o600)
    const cleanup = yaml('.github/workflows/desktop-artifacts.yaml').jobs['macos-app-sign'].steps.find(step => step.name === 'Cleanup')
    const cleanupScript = cleanup.run.startsWith('bash scripts/')
      ? raw(cleanup.run.slice('bash '.length).trim())
      : cleanup.run
    expect(runShell(cleanupScript, dir, {
      ...env,
      ...exported,
    }, bin).status).toBe(0)
    expect(existsSync(join(dir, 'apple_api.p8'))).toBe(false)
    for (const job of ['macos-app-sign', 'macos-cli-sign']) {
      const step = yaml('.github/workflows/desktop-artifacts.yaml').jobs[job].steps.find(entry => entry.name === 'Cleanup')
      expect(step.if).toBe('always()')
      expect(step.run).toBe('bash scripts/cleanup-apple-signing.sh')
    }
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
    // The tools stage, the image, and the Go cache export.
    expect(builds).toHaveLength(3)
    for (const step of builds) {
      if (step.with['cache-to'] !== undefined)
        expect(String(step.with['cache-to'])).toContain('ignore-error=true')
      expect(step['continue-on-error']).not.toBe(true)
    }
  })

  it('keeps cross-OS archives inside the workspace', () => {
    for (const file of [
      ...workflowFiles,
      ...actionFiles,
    ]) {
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

  it('leaves Go module caching out of setup-go', () => {
    const step = actionSteps('.github/actions/setup-build/action.yaml')
      .find(entry => entry.name === 'Setup Go')
    expect(String(step?.with?.cache ?? '')).toBe('false')
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
    for (const file of [
      ...workflowFiles,
      ...actionFiles,
    ]) {
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
    for (const file of [
      ...workflowFiles,
      ...actionFiles,
    ])
      expect(patterns.some(pattern => pattern.test(file)), file).toBe(true)
  })

  it('excludes an inherited obsolete dependency from the prepared archive', async () => {
    // The configured archive-preparation path. The save action must own it:
    // preparation runs before the pinned save and decides what the archive holds.
    const saveAction = '.github/actions/save-cache-snapshot/action.yaml'
    expect(existsSync(resolve(root, saveAction)), `The archive preparation action is absent: ${saveAction}`).toBe(true)
    const steps = actionSteps(saveAction)
    const prepare = steps.find(step => step.id === 'prepare')
    const save = steps.find(step => step.id === 'save')
    expect(prepare, 'The save action has no archive preparation step').toBeDefined()
    expect(save, 'The save action has no archive save step').toBeDefined()
    expect(steps.indexOf(save)).toBeGreaterThan(steps.indexOf(prepare))
    expect(save.if).toBe('steps.prepare.outputs.result == \'ready\'')
    const script = String(prepare.run ?? '').match(/(scripts\/[\w-]+\.mjs)/)?.[1]
    expect(script, 'The preparation step does not run a project script').toBeDefined()
    const { prepareCacheSnapshot, activateCacheSnapshot, cleanupSnapshotStaging } = await import(`../${script}`)
    const { rootPayloadPath } = await import('./cache-policy.mjs')

    // A writer missed its exact key, so activation created an empty payload.
    // The restored active directory still holds an obsolete module beside the
    // current one, and the population writes only the current module into the
    // isolated payload. The recorded operations show which steps ran.
    const dir = fixture()
    const operations = []
    const active = join(dir, 'gomod-active')
    const moduleEnv = {
      ...process.env,
      RUNNER_OS: 'Linux',
      RUNNER_ARCH: 'X64',
      GOMODCACHE: active,
    }
    const moduleDir = name => join(active, 'cache', 'download', name, '@v')
    mkdirSync(moduleDir('obsolete.mod'), { recursive: true })
    writeFileSync(join(moduleDir('obsolete.mod'), 'v1.0.0.info'), 'obsolete\n')
    mkdirSync(moduleDir('current.mod'), { recursive: true })
    writeFileSync(join(moduleDir('current.mod'), 'v2.0.0.info'), 'current\n')
    const activation = await activateCacheSnapshot({
      kind: 'go-modules',
      key: `go-modules-2-Linux-${'a'.repeat(64)}`,
      writer: true,
      exactHit: false,
      workspace: root,
      env: moduleEnv,
      log: message => operations.push(`activate: ${message}`),
    })
    expect(activation.outcome).toBe('writer-empty')
    const payloadRoot = rootPayloadPath('go-modules', 'gomod', moduleEnv, root)
    operations.push('populate: go mod download into the isolated payload root')
    // The population points GOMODCACHE at the payload root of the gomod
    // active root, so the download tree grows inside it.
    mkdirSync(join(payloadRoot, 'cache', 'download', 'current.mod', '@v', 'v2.0.0'), { recursive: true })
    writeFileSync(join(payloadRoot, 'cache', 'download', 'current.mod', '@v', 'v2.0.0', 'v2.0.0.info'), 'current\n')
    const prepared = await prepareCacheSnapshot({
      kind: 'go-modules',
      key: `go-modules-2-Linux-${'a'.repeat(64)}`,
      workspace: root,
      env: moduleEnv,
      log: message => operations.push(`prepare: ${message}`),
    })
    expect(prepared.result).toBe('ready')
    expect(operations.some(entry => entry.startsWith('populate: '))).toBe(true)
    const archive = readdirSync(join(prepared.archivePath, 'gomod', 'cache', 'download'), { recursive: true })
      .map(entry => entry.split(join.sep).join('/'))
    expect(archive.some(entry => entry.includes('obsolete.mod'))).toBe(false)
    expect(archive.some(entry => entry.includes('current.mod'))).toBe(true)
    await cleanupSnapshotStaging({ kind: 'go-modules', workspace: root })
    expect(existsSync(join(root, '.tmp', 'ci-cache', 'snapshots', 'go-modules'))).toBe(false)
  })

  it('prevents the save when archive preparation fails', async () => {
    const saveAction = '.github/actions/save-cache-snapshot/action.yaml'
    expect(existsSync(resolve(root, saveAction)), `The archive preparation action is absent: ${saveAction}`).toBe(true)
    const steps = actionSteps(saveAction)
    const prepare = steps.find(step => step.id === 'prepare')
    const save = steps.find(step => step.id === 'save')
    const cleanup = steps.find(step => step.id === 'cleanup')
    expect(prepare, 'The save action has no archive preparation step').toBeDefined()
    expect(save, 'The save action has no archive save step').toBeDefined()
    expect(cleanup, 'The save action has no staging cleanup step').toBeDefined()
    // Preparation failure must fail its step: no continue-on-error, no always().
    expect(prepare['continue-on-error'] ?? false).toBe(false)
    expect(prepare.if).toBeUndefined()
    expect(save.if).toBe('steps.prepare.outputs.result == \'ready\'')
    expect(cleanup.if).toBe('always()')
    const script = String(prepare.run ?? '').match(/(scripts\/[\w-]+\.mjs)/)?.[1]
    expect(script, 'The preparation step does not run a project script').toBeDefined()
    const { prepareCacheSnapshot, cleanupSnapshotStaging } = await import(`../${script}`)

    // The fixture records which operations actually execute. A native Go
    // writer whose required build-output root is absent must fail
    // preparation, so the conditional save never runs and the staging
    // payload is removed.
    const dir = fixture()
    const operations = []
    const executed = { prepare: 0, save: 0, cleanup: 0 }
    const gocache = join(dir, 'go-build-absent')
    let prepareOutcome
    try {
      executed.prepare++
      const prepared = await prepareCacheSnapshot({
        kind: 'native-go',
        key: `go-build-2-Linux-X64-ubuntu24-go1.27.1-${'a'.repeat(64)}-2027-W01`,
        workspace: root,
        env: {
          ...process.env,
          RUNNER_OS: 'Linux',
          RUNNER_ARCH: 'X64',
          CACHE_IMAGE: 'ubuntu24',
          GOLANG_VERSION: '1.27.1',
          GITHUB_REF: 'refs/heads/main',
          GITHUB_EVENT_NAME: 'push',
          GOCACHE: gocache,
          GOLANGCI_LINT_CACHE: '',
        },
        log: message => operations.push(`prepare: ${message}`),
      })
      prepareOutcome = prepared
    }
    catch (error) {
      prepareOutcome = { failed: error.message.split('\n')[0] }
    }
    expect(prepareOutcome.failed, `Preparation unexpectedly succeeded: ${JSON.stringify(prepareOutcome)}`).toBeString()
    // The action runs its save only on a ready preparation result.
    if (prepareOutcome.result === 'ready')
      executed.save++
    await cleanupSnapshotStaging({ kind: 'native-go', workspace: root })
    executed.cleanup++
    expect(executed).toEqual({ prepare: 1, save: 0, cleanup: 1 })
    expect(existsSync(join(root, '.tmp', 'ci-cache', 'snapshots', 'native-go'))).toBe(false)
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
    for (const name of [
      'Install pinned Renovate without scripts',
      'Validate configuration',
      'Compile trusted import helper',
      'Validate Renovate renderers',
    ])
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
    const tasks = yaml('Taskfile.yaml').tasks
    const dryEnvironment = tasks['renovate-dry-run'].env
    expect(dryEnvironment.RENOVATE_DRY_RUN).toBe('full')
    expect(dryEnvironment.RENOVATE_FORCE).toBeUndefined()
    // A local run has no workflow environment, so both targets supply these themselves.
    for (const target of ['renovate', 'renovate-dry-run']) {
      expect(tasks[target].env.RENOVATE_TRUSTED_ROOT, target).toBe('{{.ROOT_DIR}}')
      expect(tasks[target].env.LOG_LEVEL, target).toBe('debug')
    }
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
    const renderer = steps.find(step => step.name === 'Validate Renovate renderers')
    expect(renderer.run).toBe('task test-scripts -- ./scripts/renovate-pr-details.test.mjs ./scripts/renovate-dashboard.test.mjs')
    expect(renderer.env?.GH_TOKEN).toBeUndefined()
    expect(steps.some(step => step.name === 'Validate PR detail comments')).toBe(false)
    const diagnostics = steps.find(step => step.name === 'Upload timing and diagnostic records')
    expect(diagnostics.if).toBe('always()')
    expect(diagnostics.with.path.split('\n')).toContain('.tmp/renovate-shims/invocations.jsonl')
  })
  it('rejects publishing before activation and rejects every ref except upstream main', () => {
    const step = yaml('.github/workflows/renovate.yaml').jobs.renovate.steps[0]
    for (const [repository, ref, mode, ready, expected] of [
      [
        'leapmux/leapmux',
        'refs/heads/main',
        'dry-run',
        'false',
        true,
      ],
      [
        'leapmux/leapmux',
        'refs/heads/main',
        'publish',
        'false',
        false,
      ],
      [
        'leapmux/leapmux',
        'refs/heads/main',
        'publish',
        'true',
        true,
      ],
      [
        'fork/leapmux',
        'refs/heads/main',
        'dry-run',
        'true',
        false,
      ],
      [
        'leapmux/leapmux',
        'refs/heads/Main',
        'dry-run',
        'true',
        false,
      ],
      [
        'leapmux/leapmux',
        'refs/tags/main',
        'publish',
        'true',
        false,
      ],
      [
        'leapmux/leapmux',
        'refs/heads/main',
        'invalid',
        'true',
        false,
      ],
    ]) {
      const result = runShell(step.run, fixture(), {
        GITHUB_REPOSITORY: repository,
        GITHUB_REF: ref,
        MODE: mode,
        PUBLISH_READY: ready,
      })
      expect(result.status === 0).toBe(expected)
    }
  })
  it('checks the release age of every pull request and passes a push in seconds', () => {
    const ci = yaml('.github/workflows/ci.yaml')
    expect(Object.keys(ci.on).sort()).toEqual(['pull_request', 'push'])
    const job = ci.jobs['dependency-age']
    expect(job['runs-on']).toBe('ubuntu-24.04')
    // A security branch reads the open Dependabot alerts with the job token.
    expect(job.permissions).toEqual({ 'contents': 'read', 'vulnerability-alerts': 'read' })
    const [scope, ...rest] = job.steps
    expect(scope.id).toBe('scope')
    expect(scope.env.PULL_REQUEST).toBe(expression('github.event_name == \'pull_request\''))
    for (const step of rest)
      expect(step.if, step.name ?? step.uses).toBe('steps.scope.outputs.checked == \'true\'')
    for (const [event, expected] of [
      ['true', 'checked=true'],
      ['false', 'checked=false'],
    ]) {
      const directory = fixture()
      expect(runShell(scope.run, directory, { PULL_REQUEST: event }).status).toBe(0)
      expect(readFileSync(join(directory, 'output'), 'utf8').trim()).toBe(expected)
    }
    const checkout = rest.find(step => String(step.uses).startsWith('actions/checkout@'))
    expect(checkout.with).toEqual({ 'fetch-depth': 2, 'persist-credentials': false })
    // The check needs no compiled cache and no dependency download.
    expect(rest.some(step => step.uses === './.github/actions/setup-build')).toBe(false)
    expect(rest.map(step => String(step.uses ?? '').split('@')[0]).filter(Boolean)).toEqual([
      'actions/checkout',
      './.github/actions/load-versions',
      'actions/setup-go',
      'oven-sh/setup-bun',
      'arduino/setup-task',
    ])
    expect(rest.find(step => String(step.uses).startsWith('actions/setup-go@')).with.cache).toBe(false)
    // Every tool comes from the base commit: the job checks it out before it loads a version.
    const names = rest.map(step => step.name ?? step.uses)
    expect(names.indexOf('Check out the base commit')).toBeLessThan(names.indexOf('Load tool versions'))
    const check = rest.at(-1)
    expect(check.run).toBe('task check-dependency-age -- --base HEAD --head "$PR_MERGE_SHA" --head-ref "$HEAD_REF"')
    expect(check.env).toEqual({
      HEAD_REF: expression('github.head_ref'),
      GITHUB_TOKEN: expression('secrets.GITHUB_TOKEN'),
    })
    expect(yaml('Taskfile.yaml').tasks['check-dependency-age']).toEqual({
      desc: 'Reject a pull request whose lockfiles add a version younger than the minimum release age',
      deps: ['install-renovate-packages'],
      cmds: ['bun scripts/check-dependency-age.mjs {{.CLI_ARGS}}'],
    })
    expect(ci.jobs['required-ci'].needs).toContain('dependency-age')
  })
  it('checks out the base commit and records the merge commit for the age check', () => {
    const step = yaml('.github/workflows/ci.yaml').jobs['dependency-age'].steps.find(entry => entry.name === 'Check out the base commit')
    const directory = fixture()
    const base = gitRepository(directory, {
      files: { file: 'base\n' },
    })
    git(directory, ['checkout', '-q', '-b', 'renovate/x'])
    writeFileSync(join(directory, 'file'), 'head\n')
    git(directory, ['commit', '-q', '-am', 'head'])
    git(directory, ['checkout', '-q', 'main'])
    git(directory, [
      'merge',
      '-q',
      '--no-ff',
      '-m',
      'merge',
      'renovate/x',
    ])
    const merge = git(directory, ['rev-parse', 'HEAD'])
    const result = runShell(step.run, directory)
    expect(result.status, result.stderr).toBe(0)
    expect(git(directory, ['rev-parse', 'HEAD'])).toBe(base)
    expect(outputFile(directory, 'environment').trim()).toBe(`PR_MERGE_SHA=${merge}`)
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
    expect(ci.jobs.linux.steps.find(step => step.run === 'task validate-site-tools').if).toBe('matrix.arch == \'amd64\'')
    expect(ci.jobs.linux.steps.find(step => step.run === 'task renovate-extract').if).toBe('matrix.arch == \'amd64\'')
    // Every JavaScript tool comes from a lockfile that the release-age check reads.
    expect(yaml('Taskfile.yaml').tasks['validate-site-tools']).toMatchObject({
      dir: 'site',
      cmds: ['bun install --frozen-lockfile --ignore-scripts', './node_modules/.bin/wrangler --version'],
    })
    for (const file of ['Taskfile.yaml', ...workflowFiles])
      expect(raw(file), file).not.toMatch(/\bbunx\b|wrangler-action/)
    const required = ci.jobs['required-ci']
    expect(required.name).toBe('Required CI')
    expect(required.if).toBe('always()')
    expect(required.needs).toEqual([
      'linux',
      'macos',
      'windows',
      'docker',
      'dependency-age',
    ])
    for (const result of [
      'success',
      'failure',
      'cancelled',
      'skipped',
    ]) {
      const needs = Object.fromEntries(required.needs.map(job => [
        job,
        {
          result: job === 'linux'
            ? result
            : 'success',
        },
      ]))
      expect(runShell(required.steps[0].run, fixture(), {
        RESULTS: JSON.stringify(needs),
        EVENT_NAME: 'pull_request',
      }).status === 0).toBe(result === 'success')
    }
    const notice = yaml('.github/workflows/notice.yaml')
    expect(notice.on.pull_request.paths).toBeUndefined()
    expect(notice.on.push.paths).toBeUndefined()
    expect(notice.jobs.check.name).toBe('NOTICE')
    expect(notice.jobs.check.steps.find(step => step.name === 'Regenerate NOTICE').if).toBe('steps.inputs.outputs.needed == \'true\'')
  })
})

describe('cache native setup', () => {
  const setupSteps = () => actionSteps('.github/actions/setup-build/action.yaml')
  const aptPackages = 'libwebkit2gtk-4.1-dev libgtk-3-dev libayatana-appindicator3-dev librsvg2-dev libssl-dev libsoup-3.0-dev protobuf-compiler xdg-utils libgstreamer1.0-dev libgstreamer-plugins-base1.0-dev libgstreamer-plugins-bad1.0-dev gstreamer1.0-plugins-base gstreamer1.0-plugins-good gstreamer1.0-plugins-bad gstreamer1.0-plugins-ugly gstreamer1.0-libav gstreamer1.0-tools gstreamer1.0-x gstreamer1.0-alsa gstreamer1.0-gl gstreamer1.0-gtk3 gstreamer1.0-pulseaudio'

  it('restores the compiled caches in the setup and saves them only after the complete validation', () => {
    const steps = setupSteps()
    const goBuild = steps.find(step => step.id === 'go-build')
    const goRestore = steps.find(step => step.id === 'go-restore')
    const rustBuild = steps.find(step => step.id === 'rust-build')
    const rustRestore = steps.find(step => step.id === 'rust-restore')
    expect(goRestore.with).toEqual({
      'kind': 'native-go',
      'key': expression('steps.go-build.outputs.key'),
      'restore-keys': expression('inputs.save-build-caches != \'true\' && steps.go-build.outputs.restore-prefix || \'\''),
      'writer': expression('inputs.save-build-caches'),
    })
    expect(rustRestore.with).toEqual({
      'kind': 'native-rust',
      'key': expression('steps.rust-build.outputs.key'),
      'restore-keys': expression('inputs.save-build-caches != \'true\' && steps.rust-build.outputs.restore-prefix || \'\''),
      'writer': expression('inputs.save-build-caches'),
    })
    expect(steps.indexOf(goBuild)).toBeLessThan(steps.indexOf(goRestore))
    expect(steps.indexOf(rustBuild)).toBeLessThan(steps.indexOf(rustRestore))
    for (const name of ['linux', 'macos', 'windows']) {
      const job = yaml('.github/workflows/ci.yaml').jobs[name]
      const saveSteps = job.steps.filter(step => step.uses === './.github/actions/save-cache-snapshot')
      const goSave = job.steps.find(step => step.id === 'go-cache-save')
      const rustSave = saveSteps.find(step => step.with.kind === 'native-rust')
      const prune = job.steps.find(step => step.uses === './.github/actions/prune-build-cache')
      expect(goSave.with).toEqual({
        kind: 'native-go',
        key: expression('steps.setup.outputs.cache-key'),
      })
      expect(goSave.if).toBe('github.ref == \'refs/heads/main\' && steps.setup.outputs.go-cache-hit != \'true\'')
      expect(rustSave.with).toEqual({
        kind: 'native-rust',
        key: expression('steps.setup.outputs.rust-cache-key'),
      })
      expect(rustSave.if).toBe('github.ref == \'refs/heads/main\' && steps.setup.outputs.rust-cache-hit != \'true\'')
      // The pruner follows the Go save and a capacity skip cannot request it.
      expect(prune.if).toBe('github.ref == \'refs/heads/main\' && steps.go-cache-save.outputs.result == \'ready\'')
      expect(job.steps.indexOf(prune)).toBeGreaterThan(job.steps.indexOf(goSave))
      // The saves run at the end of the job, after every validation step.
      const lastValidation = Math.max(...job.steps.map((step, index) => /^task (?:test|test-native(?:-linux)?|build|check-build-cache|lint)$/.test(step.run ?? '')
        ? index
        : -1))
      expect(job.steps.indexOf(goSave)).toBeGreaterThan(lastValidation)
    }
    // The artifact jobs stay readers: they save no compiled cache and pass no writer input.
    const desktop = yaml('.github/workflows/desktop-artifacts.yaml')
    for (const job of Object.values(desktop.jobs)) {
      for (const step of job.steps ?? []) {
        expect(['native-go', 'native-rust']).not.toContain(step.with?.kind)
        expect(step.with?.['save-build-caches']).toBeUndefined()
      }
    }
    // Every workflow's save-build-caches input belongs to a CI platform job on main.
    const savers = []
    for (const file of workflowFiles) {
      for (const { jobName, step } of jobSteps(yaml(file))) {
        if (step.uses !== './.github/actions/setup-build')
          continue
        if ('save-build-caches' in (step.with ?? {}))
          savers.push(`${file}:${jobName}`)
      }
    }
    expect(savers.sort()).toEqual([
      '.github/workflows/ci.yaml:linux',
      '.github/workflows/ci.yaml:macos',
      '.github/workflows/ci.yaml:windows',
    ])
    for (const file of [
      ...workflowFiles,
      ...actionFiles,
    ])
      expect(raw(file)).not.toContain('rust-save-if')
  })

  it('separates the Rust download snapshot from the compiled snapshot and keeps one debug level', () => {
    const steps = setupSteps()
    const setting = steps.find(step => step.name === 'Set the Cargo debug information level')
    const rustRestore = steps.findIndex(step => step.id === 'rust-restore')
    const cargoDownloads = steps.findIndex(step => step.uses === './.github/actions/dependency-caches' && step.with.cargo === 'true')
    const removeAmbient = steps.findIndex(step => step.name === 'Remove ambient Rust toolchains')
    // The compiled restore needs the debug level, the trimmed compiler
    // fingerprints need one toolchain, and the registry fill runs after the
    // compiled restore so a fallback hit from an older lockfile still gets
    // the current index and cache.
    expect(steps.indexOf(setting)).toBeLessThan(rustRestore)
    expect(removeAmbient).toBeGreaterThan(0)
    expect(removeAmbient).toBeLessThan(rustRestore)
    expect(cargoDownloads).toBeGreaterThan(rustRestore)
    const dir = fixture()
    expect(runShell(setting.run, dir).status).toBe(0)
    expect(outputFile(dir, 'environment')).toBe('CARGO_PROFILE_DEV_DEBUG=line-tables-only\n')
    for (const file of ['ci.yaml', 'desktop-artifacts.yaml'])
      expect(yaml(`.github/workflows/${file}`).env?.CARGO_PROFILE_DEV_DEBUG).toBeUndefined()
    // The compiled Rust key carries no weekly epoch; the trimming rule
    // bounds its growth.
    expect(String(rustBuildKey())).not.toContain('CACHE_WEEK')
    function rustBuildKey() {
      const build = setupSteps().find(step => step.id === 'rust-build')
      return build.run
    }
  })

  it('restores, trims, and saves the APT snapshot around the prerequisite installation', () => {
    const steps = setupSteps()
    const index = name => steps.findIndex(step => step.name === name || step.id === name)
    const prepare = index('Prepare the APT package cache')
    const restore = index('Restore the APT package snapshot')
    const install = index('Install Tauri system dependencies')
    const trim = steps.findIndex(step => step.name === 'Trim and save the APT package snapshot')
    const save = steps.findIndex(step => step.name === 'Save the APT package snapshot')
    expect(restore).toBeGreaterThan(prepare)
    expect(install).toBeGreaterThan(restore)
    expect(trim).toBeGreaterThan(install)
    expect(save).toBeGreaterThan(trim)
    expect(steps[restore].with).toEqual({
      kind: 'apt-packages',
      key: expect.stringMatching(/^apt-2-\$\{\{ runner\.os \}\}-\$\{\{ runner\.arch \}\}-\$\{\{ hashFiles\('\.github\/actions\/setup-build\/action\.yaml'\) \}\}-\$\{\{ env\.CACHE_WEEK \}\}$/),
      writer: 'true',
    })
    expect(steps[save].with).toEqual({ kind: 'apt-packages', key: steps[restore].with.key })
    // The trim verb retains exactly the packages of the successful
    // installation, so its list must match the install step's list.
    const trimRun = String(steps[trim].run)
    const trimmedList = trimRun.match(/--packages "([^"]+)"/)?.[1]
    expect(trimmedList).toBe(aptPackages)
    const installRun = String(steps[install].run).replaceAll('\\\n', ' ')
    const installList = installRun.match(/apt-get install -y --no-install-recommends\s+((?:[\w./-]+\s*)+)/)?.[1]
    expect(installList?.split(/\s+/).filter(Boolean).join(' ')).toBe(aptPackages)
  })

  it('saves the Tauri and spinner snapshots after a complete packaging build', () => {
    // The setup restores both tool caches as a writer without a fallback.
    const steps = setupSteps()
    const tauri = steps.find(step => step.id === 'tauri-restore')
    const spinners = steps.find(step => step.id === 'spinners-restore')
    expect(tauri.with.writer).toBe('true')
    expect(tauri.with['restore-keys']).toBeUndefined()
    expect(spinners.with.writer).toBe('true')
    expect(spinners.with['restore-keys']).toBeUndefined()
    // Every job that completes a packaging build publishes the tool caches;
    // none of them saves compiled caches.
    for (const file of ['.github/workflows/ci.yaml', '.github/workflows/desktop-artifacts.yaml']) {
      const document = yaml(file)
      for (const [jobName, job] of Object.entries(document.jobs)) {
        const stepsWithBuild = (job.steps ?? []).some(step => step.run === 'task build')
        if (!stepsWithBuild)
          continue
        const tauriSave = (job.steps ?? []).find(step => step.name === 'Save the Tauri tool snapshot')
        const spinnerSave = (job.steps ?? []).find(step => step.name === 'Save the spinner source snapshot')
        if (tauriSave !== undefined) {
          expect(tauriSave.uses, file).toBe('./.github/actions/save-cache-snapshot')
          expect(tauriSave.with).toEqual({
            kind: 'tauri-tools',
            key: expression('steps.setup.outputs.tauri-cache-key'),
          })
          expect(tauriSave.if).toContain('tauri-cache-hit != \'true\'')
          // Only the CI platform jobs can run on macOS, so only they need
          // the exclusion in the condition.
          if (file.endsWith('ci.yaml'))
            expect(tauriSave.if).toContain('runner.os != \'macOS\'')
        }
        expect(spinnerSave, `${file}:${jobName}`).toBeDefined()
        expect(spinnerSave.with).toEqual({
          kind: 'spinners',
          key: expression(file.endsWith('ci.yaml')
            ? 'steps.setup.outputs.spinner-cache-key'
            : 'steps.setup.outputs.spinner-cache-key'),
        })
      }
    }
    // The artifact macOS job has no Tauri save step at all; the CI macOS
    // job's save step carries the runner exclusion that the check above
    // asserts.
    const macos = yaml('.github/workflows/desktop-artifacts.yaml').jobs.macos
    expect(macos.steps.some(step => step.name === 'Save the Tauri tool snapshot')).toBe(false)
  })

  it('keeps the patch fingerprint in every Tauri key and the spinner subtree in the clone directory', () => {
    const tauri = setupSteps().find(step => step.id === 'tauri-restore')
    for (const file of ['patch-linuxdeploy.sh', 'linuxdeploy-wrapper.c'])
      expect(tauri.with.key).toContain(file)
    expect(tauri.with.key).toEndWith(`-\${{ env.CACHE_WEEK }}`)
    const taskSuffix = String(yaml('Taskfile.yaml').vars.SPINNERS_CACHE_DIR.sh).match(/\/\.cache\/[\w-]+/)?.[0]
    expect(taskSuffix).toBe('/.cache/awesome-claude-spinners')
    const spinners = setupSteps().find(step => step.id === 'spinners-restore')
    expect(spinners.with.kind).toBe('spinners')
    expect(spinners.with.key).toBe(`spinners-2-\${{ runner.os }}-\${{ env.CACHE_WEEK }}`)
    expect(rootActivePath('spinners', 'spinners', { RUNNER_OS: 'Linux', HOME: '/home/fixture' }, '/work')).toBe('/home/fixture/.cache/awesome-claude-spinners/spinners')
  })

  it('restores and saves the compiled Renovate RE2 engine around the extraction', () => {
    const steps = yaml('.github/workflows/ci.yaml').jobs.linux.steps
    const index = name => steps.findIndex(step => step.name === name)
    const install = index('Install the pinned Renovate packages')
    const restore = index('Restore the compiled Renovate RE2 engine')
    const extract = index('Validate Renovate dependency extraction')
    const save = index('Save the compiled Renovate RE2 engine')
    // bun install replaces a package directory that it installs anew, with
    // a restored engine in it, so the restore follows the install and the
    // save follows the extraction that validates the engine.
    expect(install).toBeGreaterThanOrEqual(0)
    expect(restore).toBeGreaterThan(install)
    expect(extract).toBeGreaterThan(restore)
    expect(save).toBeGreaterThan(extract)
    expect(steps[install].run).toBe('task install-renovate-packages')
    expect(steps[restore].uses).toBe('./.github/actions/build-output-cache')
    expect(steps[save].uses).toBe('./.github/actions/save-cache-snapshot')
    const lockHash = expression('hashFiles(\'.github/renovate/bun.lock\')')
    const taskHash = expression('hashFiles(\'Taskfile.yaml\', \'scripts/task-state.mjs\')')
    const prefix = `renovate-re2-2-\${{ runner.os }}-\${{ runner.arch }}-\${{ env.CACHE_IMAGE }}-node24-bun\${{ env.BUN_VERSION }}-task\${{ env.TASK_VERSION }}-`
    const key = `${prefix}${lockHash}-${taskHash}-\${{ env.CACHE_WEEK }}`
    expect(steps[restore].with.key).toBe(key)
    // A writer on main passes no fallback; a reader falls back across the
    // recipe hashes but not across the dependency hash.
    expect(steps[restore].with.writer).toBe(expression('github.ref == \'refs/heads/main\''))
    expect(steps[restore].with['restore-keys']).toBe(expression('github.ref != \'refs/heads/main\' && format(\'renovate-re2-2-{0}-{1}-{2}-node24-bun{3}-task{4}-{5}-\', runner.os, runner.arch, env.CACHE_IMAGE, env.BUN_VERSION, env.TASK_VERSION, hashFiles(\'.github/renovate/bun.lock\')) || \'\''))
    expect(steps[save].with).toEqual({ kind: 'renovate-re2', key })
    expect(steps[save].if).toBe('github.ref == \'refs/heads/main\' && matrix.arch == \'amd64\' && steps.re2-restore.outputs.cache-hit != \'true\'')
    // The engine archive carries the task outputs whose Task checksum the
    // registry asserts.
    const task = yaml('Taskfile.yaml').tasks['build-renovate-re2']
    expect(task.vars.OUTPUTS.length).toBeGreaterThan(0)
  })

  it('refreshes the Go key each UTC week and keeps the Rust key weekly-stable', () => {
    const dir = fixture()
    const bin = join(dir, 'bin')
    mkdirSync(bin)
    writeFileSync(join(bin, 'go'), '#!/bin/sh\nprintf "%s\\n" "$FIXTURE_GOCACHE"\n')
    chmodSync(join(bin, 'go'), 0o755)
    const cli = `${root}/scripts/cache-snapshot.mjs`
    const run = step => step.run
      .replaceAll(/\$\{\{\s*hashFiles\([^)]*\)\s*\}\}/g, 'a'.repeat(64))
      .replaceAll('node scripts/cache-snapshot.mjs', `node '${cli}'`)
    const goBuild = setupSteps().find(step => step.id === 'go-build')
    const rustBuild = setupSteps().find(step => step.id === 'rust-build')
    const keys = []
    const prefixes = []
    for (const week of ['2026-W53', '2027-W01', '2027-W01']) {
      const weekDir = fixture()
      const result = runShell(run(goBuild), weekDir, {
        FIXTURE_GOCACHE: join(weekDir, 'go-cache'),
        CACHE_WEEK: week,
        RUNNER_OS: 'Linux',
        RUNNER_ARCH: 'ARM64',
        CACHE_IMAGE: 'ubuntu24',
        GOLANG_VERSION: '1.27.1',
        GITHUB_REF: 'refs/heads/main',
        GITHUB_EVENT_NAME: 'push',
        RUNNER_TEMP: weekDir,
      }, bin)
      expect(result.status, result.stderr).toBe(0)
      const output = outputFile(weekDir)
      keys.push(/^key=(.*)$/m.exec(output)?.[1])
      prefixes.push(/^restore-prefix=(.*)$/m.exec(output)?.[1])
      expect(keys.at(-1)).toEndWith(`-${week}`)
    }
    expect(keys[0]).not.toBe(keys[1])
    expect(keys[1]).toBe(keys[2])
    expect(new Set(prefixes).size).toBe(1)
    expect(prefixes[0]).not.toContain('2026-W53')
    expect(prefixes[0]).not.toContain('2027-W01')
    // The composed key belongs to this generation.
    const validated = validateCacheKey('native-go', keys[1], {
      RUNNER_OS: 'Linux',
      RUNNER_ARCH: 'ARM64',
      CACHE_IMAGE: 'ubuntu24',
      GOLANG_VERSION: '1.27.1',
    })
    expect(validated.week).toBe('2027-W01')
    // The Rust key ignores the week entirely.
    const rustRuns = new Set(['2027-W01', '2027-W02'].map((week) => {
      const weekDir = fixture()
      const result = runShell(run(rustBuild), weekDir, {
        CACHE_WEEK: week,
        RUNNER_OS: 'Linux',
        RUNNER_ARCH: 'ARM64',
        CACHE_IMAGE: 'ubuntu24',
        RUST_VERSION: '1.99.0',
        GITHUB_REF: 'refs/heads/main',
        GITHUB_EVENT_NAME: 'push',
        RUNNER_TEMP: weekDir,
      }, bin)
      expect(result.status, result.stderr).toBe(0)
      return /^key=(.*)$/m.exec(outputFile(weekDir))?.[1]
    }))
    expect(rustRuns.size).toBe(1)
    expect([...rustRuns][0]).toContain('rust-build-2-Linux-ARM64-ubuntu24-rust1.99.0-')
  })

  it('classifies the installer caches and leaves them out of every project snapshot', () => {
    // No policy root collects a toolchain or an installer directory.
    for (const kind of cacheKinds()) {
      const text = JSON.stringify(kind)
      expect(text).not.toMatch(/rustup|toolchain|node_modules\/bun|setup-go|setup-node/)
    }
    // The site workflow keeps setup-go's own cache off (see the site tests).
    const setupGo = setupSteps().find(step => step.name === 'Setup Go')
    expect(String(setupGo.with.cache)).toBe('false')
  })
})
