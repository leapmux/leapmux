import { writeFileSync } from 'node:fs'

/**
 * Write `value` as indented JSON with a final newline, readable by its owner alone.
 * A native configuration can hold the mock key, so no other user may read it.
 */
export function writePrivateJSON(path: string, value: unknown): void {
  writeFileSync(path, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 })
}
