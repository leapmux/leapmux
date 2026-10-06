import { chmodSync, writeFileSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'
import process from 'node:process'
import { isFileNameComponent } from './runDirectory'
import { quotePosixShellArgument } from './shellArguments'

/**
 * Write the launcher that runs a generated Node script under the executable name `name` in `directory`, and return
 * its path. On POSIX it is a shell launcher that replaces itself with `node script`; on Windows it is a `.cmd` file.
 *
 * The shell launcher needs no shebang in the script, so a long Node path or one with spaces still works. `exec`
 * keeps one process, so the process ID and the signals stay those of Node. The launcher passes its arguments
 * unchanged, so the script reads them from `process.argv.slice(2)`.
 */
export function writeNodeLauncher(directory: string, name: string, options: { node: string, script: string }): string {
  if (!isFileNameComponent(name))
    throw new Error('A Node launcher name must be one file-name component.')
  if (!isAbsolute(directory) || !isAbsolute(options.node) || !isAbsolute(options.script))
    throw new Error('A Node launcher requires an absolute directory, Node executable, and script.')
  const windows = process.platform === 'win32'
  const path = join(directory, windows ? `${name}.cmd` : name)
  const source = windows
    ? `@"${options.node}" "${options.script}" %*\r\n`
    : `#!/bin/sh\nexec ${quotePosixShellArgument(options.node)} ${quotePosixShellArgument(options.script)} "$@"\n`
  writeFileSync(path, source, { mode: 0o700 })
  // The mode of `writeFileSync` applies only to a new file, so set it again for a launcher that existed.
  chmodSync(path, 0o700)
  return path
}
