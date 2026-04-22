// Runs vitest with the webstorage feature flag disabled on Node.js v25+
// to avoid "Warning: `--localstorage-file` was provided without a valid
// path" when running tests with jsdom. The flag is rejected on older
// runtimes, so the guard is required.

import { spawn } from 'node:child_process'
import { argv, env, exit, kill, pid, versions } from 'node:process'

const [nodeMajor] = versions.node.split('.').map(n => Number.parseInt(n, 10))
if (nodeMajor >= 25) {
  const extra = '--no-experimental-webstorage'
  env.NODE_OPTIONS = env.NODE_OPTIONS ? `${env.NODE_OPTIONS} ${extra}` : extra
}

// Pass a single command string with shell: true so:
//   - Node doesn't emit DEP0190 (triggered by shell: true + args array).
//   - Windows cmd.exe applies PATHEXT, finding `vitest.cmd` in
//     node_modules/.bin (which bun/npm adds to PATH when running
//     package.json scripts).
// JSON.stringify gives us safe shell quoting for our simple string args.
const quoted = argv.slice(2).map(a => JSON.stringify(a)).join(' ')
const child = spawn(quoted ? `vitest ${quoted}` : 'vitest', {
  stdio: 'inherit',
  shell: true,
})

child.on('exit', (code, signal) => {
  if (signal) {
    kill(pid, signal)
    return
  }
  exit(code ?? 1)
})
