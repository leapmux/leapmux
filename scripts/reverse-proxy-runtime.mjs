import { Buffer } from 'node:buffer'
import { spawn } from 'node:child_process'
import { randomUUID } from 'node:crypto'

/** Return a command's exit status and output, or reject when execution is canceled. */
export function runCommand(command, args, { input, timeoutMs = 120_000, signal: abortSignal } = {}) {
  return new Promise((resolve, reject) => {
    abortSignal?.throwIfAborted()
    const child = spawn(command, args, {
      stdio: ['pipe', 'pipe', 'pipe'],
    })
    const stdout = []
    const stderr = []
    let processError
    let inputError
    const timer = setTimeout(() => child.kill('SIGKILL'), timeoutMs)
    const abort = () => child.kill('SIGKILL')
    abortSignal?.addEventListener('abort', abort, { once: true })
    if (abortSignal?.aborted)
      abort()
    child.stdout.on('data', chunk => stdout.push(chunk))
    child.stderr.on('data', chunk => stderr.push(chunk))
    child.stdin.on('error', (error) => {
      inputError = error
    })
    child.on('error', (error) => {
      processError = error
    })
    child.on('close', (status, exitSignal) => {
      clearTimeout(timer)
      abortSignal?.removeEventListener('abort', abort)
      // Wait for close before rejecting so the caller can clean up resources
      // after the command and all its stdio streams have stopped.
      if (abortSignal?.aborted) {
        reject(abortSignal.reason)
      }
      else if (processError || inputError) {
        reject(processError ?? inputError)
      }
      else {
        resolve({
          status: status ?? (exitSignal ? 128 : 1),
          stdout: Buffer.concat(stdout).toString('utf8'),
          stderr: Buffer.concat(stderr).toString('utf8'),
        })
      }
    })
    child.stdin.end(input ?? '')
  })
}

function dockerFailure(result, args) {
  return new Error(`docker ${args.join(' ')} failed (${result.status}):\n${result.stdout}${result.stderr}`)
}

function checkedDockerResult(result, args) {
  if (result.status !== 0)
    throw dockerFailure(result, args)
  return result.stdout.trim()
}

const MISSING_DOCKER_RESOURCE_MESSAGES = new Map([
  ['container', 'No such container: {name}'],
  ['volume', 'get {name}: no such volume'],
  ['network', 'network {name} not found'],
])

/** Recognize a daemon diagnostic that identifies the expected resource as absent. */
export function isMissingDockerResource(stderr, resourceKind, resourceName) {
  if (typeof stderr !== 'string' || typeof resourceName !== 'string' || resourceName.length === 0)
    return false
  const template = MISSING_DOCKER_RESOURCE_MESSAGES.get(resourceKind)
  if (!template)
    return false
  const detail = template.replace('{name}', () => RegExp.escape(resourceName))
  return new RegExp(`^(?:Error response from daemon:\\s*|Error:\\s*)?${detail}$`).test(stderr.trim())
}

/** Owns verification commands and foreground helper containers for one run. */
export class DockerSession {
  #signal
  #execute
  #helpers = new Set()

  constructor(signal, execute = runCommand) {
    this.#signal = signal
    this.#execute = execute
  }

  get signal() {
    return this.#signal
  }

  async run(args, options) {
    this.#signal?.throwIfAborted()
    return this.#execute('docker', args, { ...options, signal: this.#signal })
  }

  cleanupRun(args) {
    return this.#execute('docker', args)
  }

  async docker(args, options) {
    return checkedDockerResult(await this.run(args, options), args)
  }

  async runContainer(args, options) {
    this.#signal?.throwIfAborted()
    const name = `leapmux-rp-helper-${randomUUID()}`
    this.#helpers.add(name)
    const command = ['run', '--name', name, ...args]
    let outcome
    try {
      outcome = { result: await this.run(command, options) }
    }
    catch (error) {
      outcome = { error }
    }
    try {
      await this.#removeHelper(name)
    }
    catch (cleanupError) {
      if ('error' in outcome)
        throw new AggregateError([outcome.error, cleanupError], 'The helper command failed and its container could not be removed.')
      if (outcome.result.status !== 0) {
        throw new AggregateError([
          dockerFailure(outcome.result, command),
          cleanupError,
        ], 'The helper command failed and its container could not be removed.')
      }
      throw cleanupError
    }
    if ('error' in outcome)
      throw outcome.error
    return outcome.result
  }

  async dockerContainer(args, options) {
    return checkedDockerResult(await this.runContainer(args, options), ['run', ...args])
  }

  async #removeHelper(name) {
    // Killing a Docker client does not stop its container. Removal uses a
    // command independent of the verification signal, including after abort.
    const result = await this.cleanupRun(['rm', '--force', name])
    if (result.status !== 0 && !isMissingDockerResource(result.stderr, 'container', name))
      throw dockerFailure(result, ['rm', '--force', name])
    this.#helpers.delete(name)
  }

  async cleanup() {
    const results = await Promise.allSettled([...this.#helpers].map(name => this.#removeHelper(name)))
    return results.filter(result => result.status === 'rejected').map(result => result.reason)
  }
}
