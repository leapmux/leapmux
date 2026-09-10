// Task checks source content. This module checks output content and build options.
// A wildcard existence check alone cannot detect one missing generated file.
import { Buffer } from 'node:buffer'
import { createHash } from 'node:crypto'
import { closeSync, lstatSync, mkdirSync, openSync, readFileSync, readlinkSync, readSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import process from 'node:process'

function digest(value) {
  return createHash('sha256').update(value).digest('hex')
}

function statePath(root, task) {
  return join(root, '.task', 'artifacts', `${digest(task)}.sha256`)
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
  return [recipe, variables, config.env, config.set, config.shopt, config.method]
}

export function outputState(root, patterns, context = '') {
  const files = new Map()
  const exclusions = patterns.filter(p => typeof p !== 'string').map(p => new Bun.Glob(p.exclude))
  for (const pattern of patterns.filter(p => typeof p === 'string')) {
    let matches = 0
    for (const path of new Bun.Glob(pattern).scanSync({ cwd: root, dot: true, onlyFiles: false })) {
      if (exclusions.some(glob => glob.match(path.replaceAll('\\', '/'))))
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
  for (const path of [...files.keys()].sort()) {
    const absolute = join(root, path)
    const stat = files.get(path)
    hash.update(JSON.stringify([path.replaceAll('\\', '/'), stat.mode & 0o777, stat.size]))
    if (stat.isSymbolicLink()) {
      hash.update(JSON.stringify(['symlink', readlinkSync(absolute)]))
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
        hash.update(buffer.subarray(0, count))
      }
    }
    finally {
      closeSync(fd)
    }
  }
  return hash.digest('hex')
}

export function checkState(root, task, patterns, context = '') {
  try {
    return readFileSync(statePath(root, task), 'utf8') === outputState(root, patterns, context)
  }
  catch (error) {
    if (error.code === 'EACCES' || error.code === 'EPERM')
      throw error
    return false
  }
}

export function forgetState(root, task) {
  rmSync(statePath(root, task), { force: true })
}

export function recordState(root, task, patterns, context = '') {
  const content = outputState(root, patterns, context)
  const path = statePath(root, task)
  mkdirSync(dirname(path), { recursive: true })
  const temporary = `${path}.${process.pid}`
  try {
    writeFileSync(temporary, content)
    renameSync(temporary, path)
  }
  finally {
    rmSync(temporary, { force: true })
  }
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
    const recipeContext = JSON.stringify([context, taskRecipe(process.cwd(), task)])
    if (command === 'check')
      process.exitCode = checkState(process.cwd(), task, patterns, recipeContext) ? 0 : 1
    else
      recordState(process.cwd(), task, patterns, recipeContext)
  }
}
