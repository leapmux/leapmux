import type { ChildProcess } from 'node:child_process'
import process from 'node:process'
import { createProcessOutputLineDecoder } from './processOutputLines'

/** Keep enough complete lines to diagnose startup without retaining the whole run. */
const SERVER_LOG_LINES = 4000

/** Read the output from each server process that one fixture owns. */
export interface ServerOutput {
  /** Index of the next line to be emitted. */
  mark: () => number
  /** Everything emitted since `from`, as far back as the buffer still reaches. */
  since: (from: number) => string
  /**
   * Capture one process's stdout and stderr in this buffer.
   *
   * Capture each new process after a restart. The absolute mark remains valid across both processes.
   *
   * Give a label when the fixture owns several processes. The label identifies the process that wrote each line.
   */
  capture: (proc: ChildProcess, label?: string) => void
}

/** Keep a limited log history and the current partial lines for failure diagnostics. */
export function createServerOutput(): ServerOutput {
  const lines: string[] = []
  // Count removed lines so each mark remains an absolute index.
  let evicted = 0
  // Each process stream needs its own decoder. Neither processes nor streams can share partial bytes.
  const partials = new Map<ChildProcess, {
    prefix: string
    stdout: ReturnType<typeof createProcessOutputLineDecoder>
    stderr: ReturnType<typeof createProcessOutputLineDecoder>
  }>()

  const push = (line: string) => {
    lines.push(line)
    if (lines.length > SERVER_LOG_LINES) {
      lines.shift()
      evicted++
    }
  }

  return {
    mark: () => evicted + lines.length,
    since: (from: number) => {
      const slice = lines.slice(Math.max(0, from - evicted))
      // Keep unfinished output after complete lines. A panic can end before it writes a newline.
      const tails = [...partials.values()].flatMap(state => [state.stdout.partial(), state.stderr.partial()]
        .filter(line => line !== '')
        .map(line => state.prefix + line))
      return [...slice, ...tails].join('\n')
    },
    capture: (proc: ChildProcess, label?: string) => {
      const prefix = label ? `[${label}] ` : ''
      const state = {
        prefix,
        stdout: createProcessOutputLineDecoder(line => push(prefix + line)),
        stderr: createProcessOutputLineDecoder(line => push(prefix + line)),
      }
      partials.set(proc, state)
      proc.stdout?.on('data', state.stdout.write)
      proc.stderr?.on('data', state.stderr.write)
      proc.stdout?.once('end', state.stdout.end)
      proc.stderr?.once('end', state.stderr.end)
      // Flush each final line once. Remove both stream states after the process closes.
      proc.once('close', () => {
        state.stdout.end()
        state.stderr.end()
        partials.delete(proc)
      })
    },
  }
}

/** Print captured output and rethrow a setup failure that precedes test attachments. */
export function reportStartupFailure(output: ServerOutput, what: string, err: unknown): never {
  process.stderr.write(`\n[e2e] ${what} failed; output follows\n${output.since(0)}\n`)
  throw err
}
