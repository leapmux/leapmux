// Task checks source content. This module checks output content and build options.
// A wildcard existence check alone cannot detect one missing generated file.
import { Buffer } from 'node:buffer'
import { createHash } from 'node:crypto'
import { appendFileSync, closeSync, existsSync, lstatSync, mkdirSync, openSync, readFileSync, readlinkSync, readSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import process from 'node:process'

function digest(value) {
  return createHash('sha256').update(value).digest('hex')
}

function statePath(root, task) {
  return join(root, '.task', 'artifacts', `${digest(task)}.sha256`)
}

function manifestPath(root, task) {
  return `${statePath(root, task)}.json`
}

// Include only this task and the global variables that its definition uses.
// An edit to an unrelated task must not rebuild every artifact.
export function taskRecipe(root, task) {
  const config = Bun.YAML.parse(readFileSync(join(root, 'Taskfile.yaml'), 'utf8'))
  const definition = config.tasks[task]
  if (!definition)
    throw new Error(`Unknown task: ${task}`)
  const { desc, summary, ...recipe } = definition
  const variables = {}
  const pending = [recipe]
  while (pending.length > 0) {
    for (const match of JSON.stringify(pending.pop()).matchAll(/\.([A-Z][A-Z_0-9]*)/g)) {
      const key = match[1]
      if (!(key in variables) && key in (config.vars ?? {})) {
        variables[key] = config.vars[key]
        pending.push(config.vars[key])
      }
    }
  }
  return [
    recipe,
    variables,
    config.env,
    config.set,
    config.shopt,
    config.method,
  ]
}

function outputStateDetails(root, patterns, context = '') {
  const files = new Map()
  const exclusions = patterns.filter(p => typeof p !== 'string').map(({ exclude }) => {
    const segments = exclude.split('/')
    const wildcard = segments.findIndex(segment => /[*?[{]/.test(segment))
    const fixedSegments = wildcard === -1
      ? segments.slice(0, -1)
      : segments.slice(0, wildcard)
    const rootPath = fixedSegments.join('/') || '.'
    const patternSegments = wildcard === -1
      ? segments.slice(-1)
      : segments.slice(wildcard)
    const pattern = patternSegments.join('/')
    if (!existsSync(join(root, rootPath)))
      return new Set()
    const prefix = rootPath === '.'
      ? ''
      : `${rootPath}/`
    const excluded = new Set()
    const glob = new Bun.Glob(pattern)
    const cwd = join(root, rootPath)
    const paths = glob.scanSync({
      cwd,
      dot: true,
    })
    for (const path of paths) {
      const normalized = path.replaceAll('\\', '/')
      excluded.add(`${prefix}${normalized}`)
    }
    return excluded
  })
  for (const pattern of patterns.filter(p => typeof p === 'string')) {
    let matches = 0
    for (const path of new Bun.Glob(pattern).scanSync({ cwd: root, dot: true, onlyFiles: false })) {
      const normalized = path.replaceAll('\\', '/')
      if (exclusions.some(excluded => excluded.has(normalized)))
        continue
      const stat = lstatSync(join(root, path))
      if (stat.isDirectory())
        continue
      files.set(path, stat)
      matches++
    }
    if (matches === 0)
      throw new Error(`No output matches ${pattern}`)
  }
  const hash = createHash('sha256')
  hash.update(JSON.stringify([context, patterns]))
  const buffer = Buffer.allocUnsafe(256 * 1024)
  const manifest = []
  for (const path of [
    ...files.keys(),
  ].sort()) {
    const absolute = join(root, path)
    const stat = files.get(path)
    const metadata = JSON.stringify([
      path.replaceAll('\\', '/'),
      stat.mode & 0o777,
      stat.size,
    ])
    hash.update(metadata)
    const fileHash = createHash('sha256').update(metadata)
    if (stat.isSymbolicLink()) {
      const target = readlinkSync(absolute)
      const linkData = JSON.stringify(['symlink', target])
      hash.update(linkData)
      fileHash.update(linkData)
      manifest.push([path, fileHash.digest('hex')])
      continue
    }
    if (!stat.isFile())
      throw new Error(`Output is not a regular file: ${path}`)
    const fd = openSync(absolute, 'r')
    try {
      while (true) {
        const count = readSync(fd, buffer)
        if (count === 0)
          break
        const content = buffer.subarray(0, count)
        hash.update(content)
        fileHash.update(content)
      }
    }
    finally {
      closeSync(fd)
    }
    manifest.push([path, fileHash.digest('hex')])
  }
  return {
    fingerprint: hash.digest('hex'),
    manifest,
  }
}

export function outputState(root, patterns, context = '') {
  return outputStateDetails(root, patterns, context).fingerprint
}

export function checkState(root, task, patterns, context = '') {
  try {
    const stored = readFileSync(statePath(root, task), 'utf8')
    const current = outputStateDetails(root, patterns, context)
    if (stored === current.fingerprint)
      return true
    const details = {
      task,
      reason: 'output fingerprint changed',
      stored,
      current: current.fingerprint,
      outputs: patterns,
    }
    if (process.env.LEAPMUX_TASK_STATE_DEBUG === '1') {
      try {
        const previousManifest = new Map(JSON.parse(readFileSync(manifestPath(root, task), 'utf8')))
        const currentManifest = new Map(current.manifest)
        const added = []
        const removed = []
        const modified = []
        for (const [path, fingerprint] of currentManifest) {
          if (!previousManifest.has(path))
            added.push(path)
          else if (previousManifest.get(path) !== fingerprint)
            modified.push(path)
        }
        for (const path of previousManifest.keys()) {
          if (!currentManifest.has(path))
            removed.push(path)
        }
        details.fileChanges = {
          added: added.slice(0, 40),
          removed: removed.slice(0, 40),
          modified: modified.slice(0, 40),
          totalAdded: added.length,
          totalRemoved: removed.length,
          totalModified: modified.length,
        }
      }
      catch {
        details.fileChanges = 'Previous file manifest unavailable'
      }
    }
    logCacheMiss(details)
    return false
  }
  catch (error) {
    if (error.code === 'EACCES' || error.code === 'EPERM')
      throw error
    logCacheMiss({
      task,
      reason: error.message,
      code: error.code,
      outputs: patterns,
    })
    return false
  }
}

export function forgetState(root, task) {
  rmSync(statePath(root, task), { force: true })
  rmSync(manifestPath(root, task), { force: true })
}

export function recordState(root, task, patterns, context = '') {
  const details = outputStateDetails(root, patterns, context)
  const path = statePath(root, task)
  const manifest = manifestPath(root, task)
  mkdirSync(dirname(path), { recursive: true })
  const temporary = `${path}.${process.pid}`
  const temporaryManifest = `${manifest}.${process.pid}`
  try {
    writeFileSync(temporary, details.fingerprint)
    renameSync(temporary, path)
    writeFileSync(temporaryManifest, JSON.stringify(details.manifest))
    renameSync(temporaryManifest, manifest)
  }
  finally {
    rmSync(temporary, { force: true })
    rmSync(temporaryManifest, { force: true })
  }
}

function logCacheMiss(details) {
  if (process.env.LEAPMUX_TASK_STATE_DEBUG !== '1')
    return
  const message = `[task-state] cache miss: ${JSON.stringify(details)}\n`
  const logPath = process.env.LEAPMUX_TASK_STATE_DEBUG_LOG
  if (logPath)
    appendFileSync(logPath, message)
  else
    console.error(message.trimEnd())
}

if (import.meta.main) {
  const [command, task, encodedPatterns = '[]', context = ''] = process.argv.slice(2)
  if (!task || !['check', 'record', 'forget'].includes(command))
    throw new Error('Usage: task-state.mjs <check|record|forget> <task> [outputs JSON] [context]')
  if (command === 'forget') {
    forgetState(process.cwd(), task)
  }
  else {
    const patterns = JSON.parse(encodedPatterns)
    const recipeContext = JSON.stringify([
      context,
      taskRecipe(process.cwd(), task),
    ])
    if (command === 'check') {
      process.exitCode = checkState(process.cwd(), task, patterns, recipeContext)
        ? 0
        : 1
    }
    else {
      recordState(process.cwd(), task, patterns, recipeContext)
    }
  }
}
