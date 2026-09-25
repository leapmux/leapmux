import { validateSessionFileOrIdHandle } from '~/lib/validate'

/**
 * Pi's resume handle comes in two shapes, and Pi's resolver picks the lookup by
 * shape.
 *
 * `resolveSessionPath` in pi's main.ts reads a value that holds a separator, or
 * ends in `.jsonl`, as a session file PATH, and anything else as a session ID
 * that Pi matches inside the working directory's session directory.
 * `validateSessionFileOrIdHandle` is that rule, with the path and token halves it
 * composes; it states why neither half can serve both shapes.
 */
export function piValidateResumeHandle(value: string): string | null {
  return validateSessionFileOrIdHandle(value)
}
