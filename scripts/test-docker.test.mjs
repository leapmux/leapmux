import { spawnSync } from 'node:child_process'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import { scratchDirectories } from './test-support.mjs'

const scratch = scratchDirectories()
afterEach(scratch.cleanup)

const root = resolve(import.meta.dirname, '..')
const yaml = path => Bun.YAML.parse(readFileSync(join(root, path), 'utf8'))
const actionSteps = path => yaml(path).runs.steps
const expression = body => `\${{ ${body} }}`

describe('docker image tests', () => {
  it('evaluates Task defaults, load selection, and multi-platform refusal', () => {
    const source = yaml('Taskfile.yaml')
    const cases = [
      {
        platform: 'linux/arm64',
        load: 'true',
        ok: true,
        loaded: true,
      },
      { platform: 'linux/amd64,linux/arm64', load: 'true', ok: false },
      {
        platform: 'linux/amd64,linux/arm64',
        load: 'false',
        ok: true,
        loaded: false,
      },
      { platform: 'linux/arm64', ok: true, loaded: false },
      { ok: true, loaded: false },
    ]
    for (const name of ['docker-build-alpine', 'docker-build-ubuntu']) {
      for (const scenario of cases) {
        const dir = scratch.create('docker-task-load')
        const task = {
          ...source.tasks[name],
          cmds: source.tasks[name].cmds.map(command => command.replace('docker buildx build', 'bun record.mjs buildx build')),
        }
        const vars = Object.fromEntries([
          'ALPINE_IMAGE',
          'UBUNTU_IMAGE',
          'BUF_VERSION',
          'TASK_VERSION',
          'S6_OVERLAY_VERSION',
          'BUN_VERSION',
          'NODE_VERSION',
          'GOLANG_VERSION',
          'VERSION',
          'COMMIT_HASH',
          'COMMIT_TIME',
        ].map(key => [key, 'fixture']))
        writeFileSync(join(dir, 'Taskfile.json'), JSON.stringify({
          version: '3',
          vars,
          tasks: { [name]: task },
        }))
        writeFileSync(join(dir, 'record.mjs'), 'import {writeFileSync} from \'node:fs\';writeFileSync(\'recorded\',JSON.stringify(process.argv.slice(2)))\n')
        const assignments = [
          ...(scenario.platform
            ? [`PLATFORM=${scenario.platform}`]
            : []),
          ...(scenario.load
            ? [`LOAD=${scenario.load}`]
            : []),
        ]
        const result = spawnSync('task', [
          '--taskfile',
          join(dir, 'Taskfile.json'),
          name,
          ...assignments,
        ], {
          cwd: dir,
          encoding: 'utf8',
          env: { ...process.env, PLATFORM: '', LOAD: '' },
        })
        expect(result.status === 0, result.stdout + result.stderr).toBe(scenario.ok)
        expect(existsSync(join(dir, 'recorded'))).toBe(scenario.ok)
        if (scenario.ok) {
          const args = JSON.parse(readFileSync(join(dir, 'recorded'), 'utf8'))
          expect(args[args.indexOf('--platform') + 1]).toBe(scenario.platform ?? 'linux/amd64,linux/arm64')
          expect(args.includes('--load')).toBe(scenario.loaded)
        }
      }
    }
  })

  it('requires runtime tests for every published variant and architecture', () => {
    const steps = actionSteps('.github/actions/build-docker-image/action.yaml')
    const buildIndex = steps.findIndex(step => step.id === 'image')
    const checkIndex = steps.findIndex(step => step.name === 'Test the image')
    expect(steps[buildIndex].with.load).toBe(true)
    expect(checkIndex).toBeGreaterThan(buildIndex)
    expect(steps[checkIndex].run).toBe('node scripts/run-test-suite.mjs docker-image task test-docker IMAGE="$IMAGE"')
    const upload = steps.findIndex(step => step.uses === './.github/actions/upload-test-results')
    expect(upload).toBeGreaterThan(checkIndex)
    expect(steps[upload].if).toBe('always()')
    expect(steps[checkIndex].env.IMAGE).toBe(expression('steps.image.outputs.imageid'))
    expect(steps[checkIndex].if).toBeUndefined()
    expect(steps[checkIndex]['continue-on-error']).toBeUndefined()
    const job = yaml('.github/workflows/publish-container.yaml').jobs.build
    const build = job.steps.findIndex(step => step.uses === './.github/actions/build-docker-image')
    const digest = job.steps.findIndex(step => step.uses === './.github/actions/upload-docker-digest')
    expect(digest).toBeGreaterThan(build)
    expect(job.steps[digest].if).toBeUndefined()
    expect(job.strategy.matrix.variant).toEqual(['alpine', 'ubuntu'])
    expect(job.strategy.matrix.arch).toEqual(['amd64', 'arm64'])
  })

  it('runs cleanup once after both variants publish and serializes the two publishers', () => {
    const workflowPath = '.github/workflows/publish-container.yaml'
    const snapshot = yaml('.github/workflows/docker.yaml').jobs.publish
    const release = yaml('.github/workflows/release.yaml').jobs
    for (const caller of [snapshot, release.docker]) {
      expect(caller.uses).toBe(`./${workflowPath}`)
      expect(caller.concurrency).toEqual({ 'group': 'publish-container', 'cancel-in-progress': false, 'queue': 'max' })
      expect(caller.permissions.packages).toBe('write')
    }
    expect(release.docker.with.version).toBe(expression('inputs.version'))
    expect(release['publish-release'].needs).toContain('docker')
    const { build, merge, prune } = yaml(workflowPath).jobs
    expect(build.steps[0].with.ref).toContain('refs/tags/v{0}')
    expect(merge.needs).toBe('build')
    expect(merge.strategy.matrix.include.map(({ variant }) => variant)).toEqual(['alpine', 'ubuntu'])
    expect(prune.needs).toBe('merge')
    expect(prune.if).toBe('github.repository == \'leapmux/leapmux\'')
    expect(prune.strategy).toBeUndefined()
    expect(prune.permissions).toEqual({ contents: 'read', packages: 'write' })
    const cleanup = prune.steps.find(step => step.run === 'node scripts/prune-container-versions.mjs')
    expect(cleanup.env.GH_TOKEN).toBe(expression('github.token'))
    expect(cleanup['continue-on-error']).toBe(true)
  })

  it('publishes dev and release tags only when both architecture digests are valid', () => {
    const { merge } = yaml('.github/workflows/publish-container.yaml').jobs
    const publish = merge.steps.find(step => step.name === 'Publish multi-architecture image')
    const image = 'ghcr.io/leapmux/leapmux'
    const amd64 = `sha256:${'a'.repeat(64)}`
    const arm64 = `sha256:${'b'.repeat(64)}`
    const directory = scratch.create('docker-publish-digests')
    const run = (version, suffix) => spawnSync('bash', [
      '-eo',
      'pipefail',
      '-c',
      `docker() { printf '%s\\n' "$@"; }\n${publish.run}`,
    ], {
      cwd: directory,
      encoding: 'utf8',
      env: {
        ...process.env,
        REGISTRY: 'ghcr.io',
        IMAGE_NAME: 'leapmux/leapmux',
        DIGEST_DIR: '.',
        VERSION: version,
        SUFFIX: suffix,
      },
    })
    writeFileSync(join(directory, 'amd64'), amd64)
    expect(run('', '').status).not.toBe(0)
    writeFileSync(join(directory, 'arm64'), 'invalid')
    expect(run('1.2.3', '').status).not.toBe(0)
    writeFileSync(join(directory, 'arm64'), arm64)
    for (const [version, suffix, tags] of [
      [
        '',
        '',
        ['dev'],
      ],
      [
        '',
        '-ubuntu',
        ['dev-ubuntu'],
      ],
      [
        '1.2.3',
        '',
        ['1.2.3', '1', 'latest'],
      ],
      [
        '1.2.3',
        '-ubuntu',
        ['1.2.3-ubuntu', '1-ubuntu', 'latest-ubuntu'],
      ],
    ]) {
      const result = run(version, suffix)
      expect(result.status, result.stderr).toBe(0)
      expect(result.stdout.trim().split('\n')).toEqual([
        'buildx',
        'imagetools',
        'create',
        ...tags.flatMap(tag => ['-t', `${image}:${tag}`]),
        `${image}@${amd64}`,
        `${image}@${arm64}`,
      ])
    }
  })

  for (const [scenario, args] of [
    ['missing image', []],
    [
      'empty image',
      [''],
    ],
    [
      'option instead of an image',
      ['--help'],
    ],
    [
      'extra argument',
      ['image', 'extra'],
    ],
  ]) {
    it(`rejects ${scenario} before calling Docker`, () => {
      const result = spawnSync(process.execPath, [
        join(root, 'scripts/test-docker.mjs'),
        ...args,
      ], { encoding: 'utf8' })
      expect(result.status).not.toBe(0)
      expect(result.stderr).toContain('Usage: test-docker.mjs <image>')
      expect(result.stdout).not.toContain('Testing ')
    })
  }
})
