import { spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'
import { afterEach, describe, expect, it } from 'bun:test'
import { needsNotice, NOTICE_INPUTS, selectNoticeInputs } from './notice-inputs.mjs'

const root = resolve(import.meta.dirname, '..')
const fixtures = []
afterEach(() => {
  for (const directory of fixtures.splice(0)) rmSync(directory, { recursive: true, force: true })
})
function fixture() {
  const directory = mkdtempSync(resolve(root, '.tmp/notice-inputs-'))
  fixtures.push(directory)
  return directory
}
function write(directory, path, contents) {
  mkdirSync(dirname(resolve(directory, path)), { recursive: true })
  writeFileSync(resolve(directory, path), contents)
}
function git(directory, ...args) {
  const result = spawnSync('git', args, { cwd: directory, encoding: 'utf8' })
  expect(result.status, result.stderr).toBe(0)
  return result.stdout.trim()
}
function repository(directory) {
  git(directory, 'init', '--initial-branch=main')
  for (const [key, value] of Object.entries({ 'core.fsmonitor': 'false', 'gc.auto': '0', 'maintenance.auto': 'false', 'commit.gpgsign': 'false', 'user.name': 'Fixture', 'user.email': 'fixture@example.invalid' }))
    git(directory, 'config', key, value)
}

describe('needsNotice', () => {
  it('covers every workspace module without treating independent tool graphs as runtime inputs', () => {
    const work = readFileSync(resolve(root, 'go.work'), 'utf8').match(/use\s*\(([^)]*)\)/)[1]
    for (const directory of work.split('\n').map(line => line.trim().replace(/^\.\//, '')).filter(Boolean)) {
      for (const suffix of ['go.mod', 'go.sum', '**/*.go'])
        expect(NOTICE_INPUTS).toContain(`${directory}/${suffix}`)
    }
    expect(needsNotice(['site/go.mod', 'site/go.sum', '.github/renovate/bun.lock'])).toBe(false)
  })
  it('selects every dependency source and shared notice input', () => {
    for (const path of NOTICE_INPUTS.filter(path => !path.includes('*')))
      expect(needsNotice([path])).toBe(true)
    for (const path of ['backend/go.mod', 'desktop/go/go.sum', 'scripts/license-overrides/js/vinxi/LICENSE'])
      expect(needsNotice([path])).toBe(true)
  })
  it('skips empty and unrelated changes', () => {
    expect(needsNotice([])).toBe(false)
    expect(needsNotice(['frontend/src/unrelated.ts', 'site/content/docs/example.md', 'site/example.go', 'README.md'])).toBe(false)
  })
  it('selects source reachability and the tracked inputs used to render the notice', () => {
    for (const file of ['backend/main.go', 'backend/main_windows.go', 'backend/a_test.go', 'desktop/go/internal/a.go', 'frontend/src/styles/themes/default.ts', 'contracts/theme-default.json'])
      expect(needsNotice([file]), file).toBe(true)
  })
})

describe('selectNoticeInputs', () => {
  it('fetches missing validated commits together without history or tags and preserves conservative failures', () => {
    const event = { before: 'a'.repeat(40), after: 'b'.repeat(40) }
    const calls = []
    expect(selectNoticeInputs(event, { run: (args) => {
      calls.push(args)
      return args[0] === 'cat-file' ? { status: 1 } : { status: 0, stdout: 'README.md\0' }
    } })).toBe(false)
    expect(calls[2]).toEqual(['fetch', '--depth=1', '--no-tags', 'origin', event.before, event.after])
    expect(calls[3]).toEqual(['diff', '--no-renames', '--name-only', '-z', event.before, event.after, '--'])
    const warnings = []
    expect(selectNoticeInputs(event, { run: args => args[0] === 'cat-file' ? { status: 0 } : { status: 1 }, warn: message => warnings.push(message) })).toBe(true)
    expect(warnings).toHaveLength(1)
    expect(selectNoticeInputs({ before: 'invalid', after: event.after }, { run: () => {
      throw new Error('Unexpected Git command.')
    } })).toBe(true)
  })
})

describe('notice input selection', () => {
  it('fetches only missing event commits in a shallow checkout and falls back when a commit is unavailable', () => {
    const source = fixture()
    repository(source)
    write(source, 'README.md', 'Before\n')
    git(source, 'add', '.')
    git(source, 'commit', '-m', 'test: Add an unrelated document')
    const base = git(source, 'rev-parse', 'HEAD')
    write(source, 'README.md', 'After\n')
    git(source, 'add', '.')
    git(source, 'commit', '-m', 'test: Change an unrelated document')
    const head = git(source, 'rev-parse', 'HEAD')
    const checkout = fixture()
    repository(checkout)
    git(checkout, 'remote', 'add', 'origin', pathToFileURL(source).href)
    git(checkout, 'fetch', '--depth=1', '--no-tags', 'origin', 'main')
    git(checkout, 'checkout', '--detach', 'FETCH_HEAD')
    expect(spawnSync('git', ['cat-file', '-e', `${base}^{commit}`], { cwd: checkout }).status).not.toBe(0)
    for (const [before, filename, expected] of [[base, 'output', false], ['f'.repeat(40), 'unavailable', true], ['invalid', 'invalid', true]]) {
      const event = resolve(checkout, 'event.json')
      const output = resolve(checkout, filename)
      writeFileSync(event, JSON.stringify({ pull_request: { base: { sha: before }, head: { sha: head } } }))
      const result = spawnSync(process.execPath, [resolve(root, 'scripts/notice-inputs.mjs')], { cwd: checkout, encoding: 'utf8', env: { ...process.env, GITHUB_EVENT_PATH: event, GITHUB_OUTPUT: output } })
      expect(result.status, result.stderr).toBe(0)
      expect(readFileSync(output, 'utf8')).toBe(`needed=${expected}\n`)
    }
    expect(git(checkout, 'rev-parse', '--is-shallow-repository')).toBe('true')
  })
  it('selects both sides of renames and ignores unrelated renames', () => {
    for (const [from, to, expected] of [
      ['scripts/license-overrides/extra/example/LICENSE', 'licenses/example/LICENSE', true],
      ['licenses/example/LICENSE', 'scripts/license-overrides/extra/example/LICENSE', true],
      ['docs/old.md', 'docs/new.md', false],
    ]) {
      const directory = fixture()
      repository(directory)
      write(directory, from, 'The complete fixture license remains unchanged.\n')
      git(directory, 'add', '.')
      git(directory, 'commit', '-m', 'test: Add the fixture input')
      const base = git(directory, 'rev-parse', 'HEAD')
      mkdirSync(dirname(resolve(directory, to)), { recursive: true })
      renameSync(resolve(directory, from), resolve(directory, to))
      git(directory, 'add', '.')
      git(directory, 'commit', '-m', 'test: Move the fixture input')
      const head = git(directory, 'rev-parse', 'HEAD')
      const event = resolve(directory, 'event.json')
      const output = resolve(directory, 'output')
      writeFileSync(event, JSON.stringify({ before: base, after: head }))
      const result = spawnSync(process.execPath, [resolve(root, 'scripts/notice-inputs.mjs')], { cwd: directory, encoding: 'utf8', env: { ...process.env, GITHUB_EVENT_PATH: event, GITHUB_OUTPUT: output } })
      expect(result.status, result.stderr).toBe(0)
      expect(readFileSync(output, 'utf8')).toBe(`needed=${expected}\n`)
    }
  })
  it('invalidates the actual Task source cache after a Go source change', () => {
    const directory = fixture()
    const production = Bun.YAML.parse(readFileSync(resolve(root, 'Taskfile.yaml'), 'utf8')).tasks['generate-notice']
    write(directory, 'scripts/notice-inputs.mjs', readFileSync(resolve(root, 'scripts/notice-inputs.mjs')))
    write(directory, 'scripts/task-state.mjs', readFileSync(resolve(root, 'scripts/task-state.mjs')))
    write(directory, 'backend/main.go', 'package example\n')
    write(directory, 'Taskfile.yaml', Bun.YAML.stringify({ version: '3', method: 'checksum', tasks: { probe: {
      vars: { ...production.vars, OUTPUTS: ['count', 'NOTICE.md', 'NOTICE.html'] },
      sources: production.sources,
      generates: ['count', 'NOTICE.md', 'NOTICE.html'],
      status: production.status,
      cmds: [production.cmds[0], `bun -e 'const f=Bun.file("count"); await Bun.write("count",String((await f.exists()?Number(await f.text()):0)+1)); await Bun.write("NOTICE.md","fixture notice"); await Bun.write("NOTICE.html","fixture notice")'`, production.cmds.at(-1)],
    } } }))
    const run = () => {
      const result = spawnSync('task', ['probe'], { cwd: directory, encoding: 'utf8' })
      expect(result.status, result.stderr).toBe(0)
      return readFileSync(resolve(directory, 'count'), 'utf8')
    }
    expect(run()).toBe('1')
    expect(run()).toBe('1')
    write(directory, 'backend/main.go', 'package example\nimport _ "example.com/already-required"\n')
    expect(run()).toBe('2')
    write(directory, 'frontend/src/unrelated.ts', 'export const value = 1\n')
    expect(run()).toBe('2')
  })
})
