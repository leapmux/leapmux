import { execFileSync } from 'node:child_process'

/** Resolve the `task` binary, which may be installed as `go-task`. */
export function resolveTaskBin(): string {
  for (const name of ['task', 'go-task']) {
    try {
      execFileSync('which', [name], { stdio: 'pipe' })
      return name
    }
    catch {}
  }
  throw new Error('Neither "task" nor "go-task" found in $PATH')
}
