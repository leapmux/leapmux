import type { ChildProcess } from 'node:child_process'
import process from 'node:process'

/**
 * How many lines of server output to keep for a failing test.
 *
 * Enough to cover an agent's whole startup (spawn, initialize handshake,
 * settings refresh, first turn) without holding a whole run's chatter. One ring
 * serves every process a fixture spawns, so a hub and a worker share this
 * budget.
 */
const SERVER_LOG_LINES = 4000

/**
 * A window onto the output of the server processes one fixture owns.
 *
 * Sliced per test, not just tailed: the server fixtures are worker-scoped, so
 * one instance serves every test the Playwright worker runs and a plain tail is
 * whatever the LAST test happened to print. Marking at the start of a test and
 * slicing from there is the difference between a readable cause and someone
 * else's chatter.
 */
export interface ServerOutput {
  /** Index of the next line to be emitted. */
  mark: () => number
  /** Everything emitted since `from`, as far back as the buffer still reaches. */
  since: (from: number) => string
  /**
   * Route one process's stdout AND stderr into this buffer.
   *
   * Call it again for each process that REPLACES one already captured (the
   * worker a restart spawns), so the buffer spans the restart and `mark`
   * stays a single timeline across both.
   *
   * `label` prefixes every line the process writes. Give one whenever a buffer
   * carries more than one process, or the reader cannot tell which of them
   * said what.
   */
  capture: (proc: ChildProcess, label?: string) => void
}

/**
 * Keep a bounded tail of a fixture's server output instead of discarding it.
 *
 * This used to be `proc.stdout?.resume()` on both streams -- drained purely to
 * stop backpressure, which meant every worker-side failure (a `git worktree
 * add` that lost an index.lock race, an agent that failed to spawn) reached the
 * report as nothing but a Playwright timeout on some unrelated assertion. The
 * ring buffer costs a few hundred KB and turns those into readable causes; the
 * fixtures attach it only for tests that actually failed.
 */
export function createServerOutput(): ServerOutput {
  const lines: string[] = []
  // How many lines the ring has evicted, so `mark` stays a stable absolute
  // index even as old output falls off the front.
  let evicted = 0
  // Per PROCESS, not one shared field: two processes write concurrently, and a
  // single carry would splice one's half-line onto the other's next chunk. The
  // prefix rides along, because a carry is reported before any line completes
  // it and an unlabelled one leaves the reader guessing which process died.
  const partials = new Map<ChildProcess, { prefix: string, carry: string }>()

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
      // A trailing carry is real output the process has not terminated yet --
      // usually the panic that stopped it mid-write, which is exactly the line
      // worth reading. It goes last, because no line has closed it and so
      // nothing places it among the ones that did.
      const tails = [...partials.values()].filter(s => s.carry).map(s => s.prefix + s.carry)
      return [...slice, ...tails].join('\n')
    },
    capture: (proc: ChildProcess, label?: string) => {
      const state = { prefix: label ? `[${label}] ` : '', carry: '' }
      partials.set(proc, state)
      const consume = (chunk: { toString: () => string }) => {
        const parts = (state.carry + chunk.toString()).split('\n')
        state.carry = parts.pop() ?? ''
        for (const line of parts)
          push(state.prefix + line)
      }
      proc.stdout?.on('data', consume)
      proc.stderr?.on('data', consume)
      // A dead process writes no terminating newline, so land its last line in
      // the ring and drop its carry. Without this, every slice taken after a
      // restart repeats the previous process's dying fragment at the end,
      // forever, and the map grows by one entry per restart.
      proc.once('close', () => {
        if (state.carry)
          push(state.prefix + state.carry)
        partials.delete(proc)
      })
    },
  }
}

/**
 * Print everything a fixture's processes have said, then rethrow `err`.
 *
 * For a failure OUTSIDE a test: a hub that never became ready, a worker that
 * never reached the hub. The per-test attachment cannot carry those -- no test
 * has started, so nothing has marked the buffer or will attach it -- and
 * without this the captured output dies with the fixture and the report holds
 * only "Timed out waiting for...".
 */
export function reportStartupFailure(output: ServerOutput, what: string, err: unknown): never {
  process.stderr.write(`\n[e2e] ${what} failed; output follows\n${output.since(0)}\n`)
  throw err
}
