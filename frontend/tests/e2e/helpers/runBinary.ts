/**
 * The LeapMux executable that one end-to-end run starts every process from.
 *
 * `task build-backend` writes the binary at the repository root, and each
 * other `task` pipeline that builds the backend replaces that file. A pipeline
 * that does not set `LEAPMUX_DEV=1` embeds a frontend that emits none of the
 * timing events that the timing specs wait for. Each pipeline also compiles the
 * source that the tree holds at that moment.
 *
 * A process that started before such a rebuild keeps the old build, so the
 * shared hub does not change. Each process that starts from the root path after
 * the rebuild runs the new build: the private hub of a spec, a worker, or a CLI
 * call. The run then tests two builds at the same time.
 *
 * The launcher copies the build output into the run directory right after the
 * build, and global setup gives only that copy to the specs. A later rebuild of
 * the root binary then cannot reach the run.
 *
 * This module imports no other helper, because the launcher imports it before a
 * run and its state exist.
 */
import { constants, copyFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'

/** The file name of the LeapMux executable, at the repository root and in a run directory. */
export const LEAPMUX_BINARY_NAME = process.platform === 'win32' ? 'leapmux.exe' : 'leapmux'

/** Where the run that owns `runDir` keeps its copy of the LeapMux executable. */
export function runBinaryPath(runDir: string): string {
  return join(runDir, LEAPMUX_BINARY_NAME)
}

/**
 * Copy the build output into the run directory, and return the path of the copy.
 *
 * A copy, not a link. A hard link shares the file that a rebuild rewrites in
 * place, and a symbolic link follows the file that a rebuild renames over the
 * old one. `COPYFILE_FICLONE` makes a copy-on-write clone where the file system
 * supports one, so the copy costs no time and no space on APFS and Btrfs, and it
 * falls back to a byte copy elsewhere. The copy keeps the permissions of the
 * build output, which include the executable bit.
 *
 * `COPYFILE_EXCL`, because the run directory is new. A file at the target means
 * that something else wrote there, so fail rather than replace it.
 */
export function copyRunBinary(buildOutput: string, runDir: string): string {
  const target = runBinaryPath(runDir)
  copyFileSync(buildOutput, target, constants.COPYFILE_EXCL | constants.COPYFILE_FICLONE)
  return target
}
