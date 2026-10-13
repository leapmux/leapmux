import { execFileSync } from 'node:child_process'
import process from 'node:process'

/** Resolve the `task` binary, which may be installed as `go-task`. */
export function resolveTaskBin(): string {
  // On Windows, name the executable with its suffix: CreateProcess appends
  // .exe only some of the time under every runtime, and a spawn of the bare
  // name can also reach cmd's own `task` resolution order. The suffixed
  // probe answers the same binary the shell would run.
  const names = process.platform === 'win32'
    ? ['task.exe', 'go-task.exe', 'task', 'go-task']
    : ['task', 'go-task']
  for (const name of names) {
    try {
      execFileSync(name, ['--version'], { stdio: 'ignore' })
      return name
    }
    catch {}
  }
  throw new Error('Neither "task" nor "go-task" runs from PATH')
}
