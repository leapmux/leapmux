// Run after `task build`, with the same environment and no intervening changes.
// This check executes the native build. It fails if any task runs a command.
import { spawnSync } from 'node:child_process'
import { closeSync, createReadStream, mkdirSync, mkdtempSync, openSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { createInterface } from 'node:readline'
import { pipeline } from 'node:stream/promises'

export async function readBuildLog(path) {
  const tasks = new Set()
  let started = false
  let completed = false
  const lines = createInterface({ input: createReadStream(path), crlfDelay: Infinity })
  for await (const line of lines) {
    if (line === 'task: "build" started')
      started = true
    if (line === 'task: "build" finished' || line === 'task: Task "build" is up to date')
      completed = true
    const match = /^task: \[(.+?)\] /.exec(line)
    if (match)
      tasks.add(match[1])
  }
  // Require execution markers so a changed log format or a listing mode cannot pass silently.
  if (!started || !completed)
    throw new Error('Task did not report a complete build run')
  return [...tasks].sort()
}

async function main() {
  const [executable, ...extra] = process.argv.slice(2)
  if (!executable || extra.length > 0)
    throw new Error('Usage: check-build-cache.mjs <Task executable>')

  const scratch = join(process.cwd(), '.tmp')
  mkdirSync(scratch, { recursive: true })
  const directory = mkdtempSync(join(scratch, 'build-cache-'))
  const paths = ['stdout.log', 'stderr.log'].map(file => join(directory, file))
  const descriptors = []
  let result
  console.log(`Check the unchanged native build. Logs: ${directory}`)
  try {
    for (const path of paths)
      descriptors.push(openSync(path, 'w'))
    // Verbose mode logs even silent commands. Skipped conditions and platform commands go to stdout.
    // Read only stderr for command execution. Task's status checks and dynamic variables remain allowed.
    result = spawnSync(executable, ['--verbose', '--color=false', '--output=interleaved', '--dry=false', 'build'], {
      stdio: ['ignore', ...descriptors],
    })
  }
  finally {
    for (const descriptor of descriptors)
      closeSync(descriptor)
  }

  try {
    if (result.error)
      throw new Error(`Cannot start Task: ${result.error.message}`)
    if (result.status !== 0)
      throw new Error(`Build failed (${result.signal ? `signal ${result.signal}` : `exit code ${result.status}`})`)
    const tasks = await readBuildLog(paths[1])
    if (tasks.length > 0)
      throw new Error(`Build commands ran: ${tasks.join(', ')}`)
  }
  catch (error) {
    // Retain both complete logs and print them on failure for CI diagnostics.
    for (const path of paths) {
      console.error(`Build log: ${path}`)
      await pipeline(createReadStream(path), process.stderr, { end: false })
    }
    throw error
  }
  console.log('Build cache check passed. No build commands ran.')
}

if (import.meta.main) {
  try {
    await main()
  }
  catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
