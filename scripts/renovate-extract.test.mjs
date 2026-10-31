import { existsSync, mkdirSync, readdirSync, readFileSync, unlinkSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import { extractRenovateInventory, selectExtractionFiles } from './renovate-extract.mjs'
import { gitRepository, scratchDirectories } from './test-support.mjs'

const root = resolve(import.meta.dirname, '..')
const originalCwd = process.cwd()
const scratch = scratchDirectories()
afterEach(() => {
  process.chdir(originalCwd)
  scratch.cleanup()
})
function write(root, path, content) {
  mkdirSync(dirname(resolve(root, path)), { recursive: true })
  writeFileSync(resolve(root, path), content)
}
function fixture() {
  const directory = scratch.create('extraction-test')
  write(directory, '.gitignore', '.tmp/\n')
  write(directory, 'versions.env', readFileSync(resolve(root, 'versions.env')))
  for (const file of ['backend/go.mod', 'desktop/go/go.mod', 'site/go.mod'])
    write(directory, file, `module example.com/${file.replaceAll('/', '-')}\ngo 1.27.1\nrequire example.com/dependency v1.0.0\n`)
  for (const file of ['frontend/package.json', '.github/renovate/package.json', 'site/package.json']) {
    write(directory, file, '{"dependencies":{"fixture":"1.0.0"}}')
    write(directory, file.replace('package.json', 'bun.lock'), '{"lockfileVersion":1,"workspaces":{"":{"dependencies":{"fixture":"1.0.0"}}},"packages":{"fixture":["fixture@1.0.0","",{},"hash"]}}')
  }
  write(directory, 'desktop/rust/Cargo.toml', '[package]\nname = "example"\nversion = "0.1.0"\n[dependencies]\nfixture = "1"\n')
  write(directory, 'desktop/rust/Cargo.lock', 'version = 4\n[[package]]\nname = "fixture"\nversion = "1.0.0"\n')
  write(directory, 'renovate.json', readFileSync(resolve(root, 'renovate.json')))
  write(directory, '.github/workflows/removed.yaml', 'name: Example\non: push\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v7\n')
  gitRepository(directory)
  return directory
}
function extracted(extra = [], removed = true) {
  return {
    'gomod': ['backend/go.mod', 'desktop/go/go.mod', 'site/go.mod'].map(packageFile => ({ packageFile })),
    'bun': ['frontend/package.json', '.github/renovate/package.json', 'site/package.json'].map(packageFile => ({ packageFile })),
    'cargo': [
      { packageFile: 'desktop/rust/Cargo.toml' },
    ],
    'github-actions': [
      ...extra,
      ...(removed
        ? ['.github/workflows/removed.yaml']
        : []),
    ].map(packageFile => ({ packageFile })),
    'regex': [
      {
        packageFile: 'versions.env',
        deps: [
          ...readFileSync(resolve(root, 'versions.env'), 'utf8').matchAll(/^([A-Z_][A-Z0-9_]*)=/gm),
        ].filter(match => match[1] !== 'VERSION').map(match => ({ depType: match[1] })),
      },
    ],
  }
}
function runner(inventory = extracted()) {
  return async (_command, _args, { output }) => {
    output.write(`${JSON.stringify({ msg: 'Extracted dependencies', packageFiles: inventory })}\n`)
    return { code: 0, timedOut: false, durationMs: 0 }
  }
}

describe('extractRenovateInventory', () => {
  it('matches unexpected manifests and their metadata without copying unrelated source assets', async () => {
    const manifests = [
      'packages/new/package.json',
      'new/rust/Cargo.toml',
      'new/go/go.mod',
      '.github/actions/new/nested/action.yml',
    ]
    const companions = [
      'packages/new/bun.lock',
      'packages/.npmrc',
      'packages/bunfig.toml',
      'new/rust/Cargo.lock',
      'new/go/go.sum',
      'go.work',
      '.cargo/config.toml',
      '.github/workflows/actions.lock',
    ]
    const omitted = [
      'frontend/src/large-asset.bin',
      'backend/internal/source.go',
      'site/content/document.md',
      'renovate.json',
    ]
    const selected = await selectExtractionFiles([
      ...manifests,
      ...companions,
      ...omitted,
    ], JSON.parse(readFileSync(resolve(root, 'renovate.json'), 'utf8')))
    for (const file of [
      ...manifests,
      ...companions,
    ]) expect(selected).toContain(file)
    for (const file of omitted) expect(selected).not.toContain(file)
  })
  it('validates a proposed tree with unstaged workflow deletion and an untracked nested replacement', async () => {
    const directory = fixture()
    unlinkSync(resolve(directory, '.github/workflows/removed.yaml'))
    write(directory, '.github/actions/build/linux/action.yaml', 'name: Example\nruns:\n  using: composite\n  steps:\n    - uses: actions/checkout@v7\n')
    const result = await extractRenovateInventory({
      root: directory,
      run: runner(extracted(['.github/actions/build/linux/action.yaml'], false)),
    })
    expect(result.inventory['github-actions'].some(file => file.packageFile === '.github/actions/build/linux/action.yaml')).toBe(true)
    expect(process.cwd()).toBe(originalCwd)
  })
  it('removes each checkout while preserving per-run logs and the extracted inventory', async () => {
    const directory = fixture()
    for (let index = 0; index < 2; index++) {
      const result = await extractRenovateInventory({
        root: directory,
        run: runner(),
      })
      expect(existsSync(result.fixture)).toBe(false)
      expect(existsSync(result.log)).toBe(true)
      expect(JSON.parse(readFileSync(resolve(directory, '.tmp/renovate-inventory.json'), 'utf8')).log).toBe(result.log)
    }
    for (const entry of readdirSync(resolve(directory, '.tmp')))
      expect(existsSync(resolve(directory, '.tmp', entry, '.git'))).toBe(false)
  })
  it('restores process state and retains diagnostics after execution or inventory failures', async () => {
    for (const [run, expected] of [
      [
        async () => {
          throw new Error('execution failed')
        },
        'execution failed',
      ],
      [
        async () => ({ code: 1 }),
        'Local extraction failed',
      ],
      [
        async () => ({ code: 0 }),
        'did not emit an extracted inventory',
      ],
    ]) {
      const directory = fixture()
      await expect(extractRenovateInventory({ root: directory, run })).rejects.toThrow()
      expect(process.cwd()).toBe(originalCwd)
      const diagnostics = readdirSync(resolve(directory, '.tmp'))
      expect(diagnostics).toHaveLength(1)
      for (const entry of diagnostics) {
        expect(existsSync(resolve(directory, '.tmp', entry, '.git'))).toBe(false)
        const records = readFileSync(resolve(directory, '.tmp', entry, 'extraction.jsonl'), 'utf8').trim().split('\n').map(line => JSON.parse(line))
        expect(records.some(record => record.level === 'error' && record.msg.includes(expected))).toBe(true)
      }
    }
  })
})
