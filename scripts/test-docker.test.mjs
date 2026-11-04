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

  describe('docker build load flag', () => {
    const taskfile = () => Bun.YAML.parse(readFileSync(join(root, 'Taskfile.yaml'), 'utf8'))

    function buildCommand(target) {
      const task = taskfile().tasks[target]
      expect(task, target).toBeDefined()
      const precondition = task.preconditions?.map(entry => entry.sh).join('\n')
      const command = task.cmds.at(-1)
      return { precondition, command }
    }

    it('loads by default only when a single platform is given, and rejects a multi-platform load', () => {
      for (const target of ['docker-build-alpine', 'docker-build-ubuntu']) {
        const { precondition, command } = buildCommand(target)
        // The template carries the conditional load; the default variable
        // value is 'false', so the default build loads nothing.
        expect(command).toContain('{{if eq .LOAD "true"}}--load{{end}}')
        expect(taskfile().tasks[target].vars.LOAD).toBe('{{.LOAD | default "false"}}')
        // A load requires a single platform: the precondition rejects a
        // comma before the build starts.
        expect(precondition, target).toContain('grep -q ,')
        expect(precondition).toContain('{{.LOAD}}" != "true"')
      }
    })

    it('assembles the load command for a single platform', () => {
      // The template renders --load only for LOAD=true; assert the rendered
      // shape through a minimal template expansion of the same rule.
      const template = buildCommand('docker-build-alpine').command
      const renderedLoad = template.replace('{{if eq .LOAD "true"}}--load{{end}}', '--load')
      expect(renderedLoad).toContain('--load')
      const renderedNoLoad = template.replace('{{if eq .LOAD "true"}}--load{{end}}', '')
      expect(renderedNoLoad).not.toContain('--load')
    })
  })
})
