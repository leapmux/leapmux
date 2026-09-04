import type { PathFlavor } from '~/lib/paths'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import { flavorFromOs, tildify } from '~/lib/paths'

/**
 * The path flavor to shorten a worker's paths with, or undefined when the
 * worker has not reported its OS.
 *
 * Undefined rather than a default, because `flavorFromOs(undefined)` answers
 * `'posix'` -- which would force posix onto a Windows worker with no system
 * info yet, and stop `C:\Users\u\repo` from compressing at all. Undefined lets
 * `tildify` sniff the flavor from the path instead.
 */
export function workerPathFlavor(info: WorkerInfo | null | undefined): PathFlavor | undefined {
  return info?.os ? flavorFromOs(info.os) : undefined
}

/**
 * Tilde-compress a worker-side absolute path for display.
 *
 * ONE spelling of the rule for every surface that shows a worker's path: the
 * sidebar tree's branch-collision suffix, its row tooltip, and the section
 * header menu's repository rows. Two copies let a row read `~/repos/foo` while
 * its tooltip reads `/Users/x/repos/foo`.
 *
 * Returns the path unchanged while the worker's system info is absent -- a
 * correct long path beats a wrong short one.
 */
export function tildifyForWorker(path: string, info: WorkerInfo | null | undefined): string {
  return tildify(path, info?.homeDir, workerPathFlavor(info))
}
