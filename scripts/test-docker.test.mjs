import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { describe, expect, it } from 'bun:test'

const root = resolve(import.meta.dirname, '..')
const yaml = path => Bun.YAML.parse(readFileSync(join(root, path), 'utf8'))
const actionSteps = path => yaml(path).runs.steps
const expression = body => `\${{ ${body} }}`

describe('docker image tests', () => {
  it('requires runtime tests for every published variant and architecture', () => {
    const steps = actionSteps('.github/actions/build-docker-image/action.yaml')
    const buildIndex = steps.findIndex(step => step.id === 'image')
    const checkIndex = steps.findIndex(step => step.name === 'Test the image')
    expect(steps[buildIndex].with.load).toBe(true)
    expect(checkIndex).toBeGreaterThan(buildIndex)
    expect(steps[checkIndex].run).toBe('task test-docker IMAGE="$IMAGE"')
    expect(steps[checkIndex].env.IMAGE).toBe(expression('steps.image.outputs.imageid'))
    expect(steps[checkIndex].if).toBeUndefined()
    expect(steps[checkIndex]['continue-on-error']).toBeUndefined()
    for (const file of ['.github/workflows/docker.yaml', '.github/workflows/release.yaml']) {
      const jobs = Object.values(yaml(file).jobs)
      const job = jobs.find(job => job.steps?.some(step => step.uses === './.github/actions/build-docker-image'))
      const build = job.steps.findIndex(step => step.uses === './.github/actions/build-docker-image')
      const digest = job.steps.findIndex(step => step.uses === './.github/actions/upload-docker-digest')
      expect(digest).toBeGreaterThan(build)
      expect(job.steps[digest].if).toBeUndefined()
      expect(job.strategy.matrix.variant).toEqual(['alpine', 'ubuntu'])
      expect(job.strategy.matrix.arch).toEqual(['amd64', 'arm64'])
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
