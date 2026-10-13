import { writeFileSync } from 'node:fs'
import process from 'node:process'

/**
 * The permission bits a private file reports on this platform.
 *
 * Windows keeps no owner-only bits: chmod there can only clear the read-only
 * flag, so a writable file reports 0o666 whatever mode the write asked for.
 * POSIX reports exactly the 0o600 the private writes request.
 */
export const privateFileMode = process.platform === 'win32' ? 0o666 : 0o600

/**
 * The permission bits a private directory reports on this platform, for the
 * same reason as {@link privateFileMode}: 0o700 on POSIX, 0o777 on Windows.
 */
export const privateDirectoryMode = process.platform === 'win32' ? 0o777 : 0o700

/**
 * Write `value` as indented JSON with a final newline, readable by its owner alone.
 * A native configuration can hold the mock key, so no other user may read it.
 */
export function writePrivateJSON(path: string, value: unknown): void {
  writeFileSync(path, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 })
}
