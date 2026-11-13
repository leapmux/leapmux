import { spawnSync } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { appendFileSync, copyFileSync, lstatSync, mkdirSync, mkdtempSync, readdirSync, renameSync, rmSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'

const checkout = resolve(import.meta.dirname, '..')

/** Collect only report files. Never follow report symlinks, dependency junctions, or command environment directories. */
export function collectTestResultFiles(root) {
  const files = new Set()
  function visit(path) {
    let stat
    try {
      stat = lstatSync(resolve(root, path))
    }
    catch (error) {
      if (error.code === 'ENOENT')
        return
      throw error
    }
    if (stat.isSymbolicLink())
      return
    if (stat.isFile()) {
      files.add(path)
      return
    }
    if (stat.isDirectory()) {
      for (const entry of readdirSync(resolve(root, path)))
        visit(`${path}/${entry}`)
    }
  }
  function select(path) {
    let current = root
    const parts = path.split('/')
    for (const [index, part] of parts.entries()) {
      current = join(current, part)
      let stat
      try {
        stat = lstatSync(current)
      }
      catch (error) {
        if (error.code === 'ENOENT')
          return
        throw error
      }
      if (stat.isSymbolicLink() || (index < parts.length - 1 && !stat.isDirectory()))
        return
    }
    visit(path)
  }
  for (const directory of [
    '.tmp/test-results',
    'frontend/test-results',
    'frontend/playwright-report',
    'frontend/blob-report',
  ])
    select(directory)
  let scratch = []
  try {
    const stat = lstatSync(join(root, '.tmp'))
    if (stat.isDirectory() && !stat.isSymbolicLink())
      scratch = readdirSync(join(root, '.tmp'), { withFileTypes: true })
  }
  catch (error) {
    if (error.code !== 'ENOENT')
      throw error
  }
  for (const entry of scratch) {
    if (!entry.isDirectory() || entry.isSymbolicLink() || !entry.name.startsWith('e2e-launcher-fixture-'))
      continue
    const fixture = `.tmp/${entry.name}`
    for (const directory of ['retained-artifacts', 'launcher-artifacts', 'records'])
      visit(`${fixture}/${directory}`)
    for (const name of readdirSync(resolve(root, fixture))) {
      if (name.endsWith('.json') || (name.startsWith('launcher-console-') && name.endsWith('.log')))
        visit(`${fixture}/${name}`)
    }
  }
  return [...files].sort()
}

/** Create one gzip archive without replacing a previous archive until compression succeeds. */
export function archiveTestResults({ root = checkout, name = 'test-results.tar.gz', run = spawnSync } = {}) {
  if (typeof name !== 'string' || !/^[a-z0-9][\w.-]{0,200}\.tar\.gz$/i.test(name))
    throw new Error('The test-results archive name must be a filename ending in .tar.gz.')
  root = resolve(root)
  const files = collectTestResultFiles(root)
  if (!files.length)
    return undefined
  const parent = join(root, '.tmp/test-results-archives')
  mkdirSync(parent, { recursive: true })
  const staging = mkdtempSync(join(parent, 'packing-'))
  const contents = join(staging, 'files')
  const draft = join(staging, name)
  const destination = join(parent, name)
  const failures = []
  try {
    // Windows tar interprets file-list bytes through its active code page, which can change UTF-8 filenames.
    // Node copies the selected files through Unicode filesystem APIs. Tar discovers their names through directory traversal.
    for (const file of files) {
      const target = join(contents, file)
      mkdirSync(dirname(target), { recursive: true })
      copyFileSync(join(root, file), target)
    }
    const result = run('tar', [
      '-czf',
      `../${name}`,
      '.',
    ], {
      cwd: contents,
      stdio: ['ignore', 'inherit', 'inherit'],
    })
    if (result.error || result.status !== 0)
      throw new Error(`tar failed to compress test results: ${result.error?.message ?? result.signal ?? result.status}`)
    renameSync(draft, destination)
  }
  catch (error) {
    failures.push(error)
  }
  try {
    rmSync(staging, {
      recursive: true,
      force: true,
      maxRetries: 10,
      retryDelay: 100,
    })
  }
  catch (error) {
    failures.push(error)
  }
  if (failures.length === 1)
    throw failures[0]
  if (failures.length > 1)
    throw new AggregateError(failures, 'Test result compression and its private cleanup failed.')
  return destination
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try {
    const archive = archiveTestResults({
      root: process.cwd(),
      name: process.env.TEST_RESULTS_ARCHIVE_NAME || 'test-results.tar.gz',
    })
    if (process.env.GITHUB_OUTPUT) {
      const delimiter = randomUUID()
      appendFileSync(process.env.GITHUB_OUTPUT, `archive<<${delimiter}\n${archive ?? ''}\n${delimiter}\n`)
    }
    console.log(archive
      ? `Test results archive: ${archive}`
      : 'No test results exist to archive.')
  }
  catch (error) {
    console.error(error)
    process.exitCode = 1
  }
}
