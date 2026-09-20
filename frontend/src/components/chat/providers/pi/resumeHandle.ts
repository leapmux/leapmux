import { validateSessionFilePath, validateSessionId } from '~/lib/validate'

/**
 * Pi's resume handle comes in two shapes, and this picks the rule by shape.
 *
 * The test is a copy of Pi's OWN resolver (`resolveSessionPath` in pi's
 * main.ts): a value that holds a separator, or ends in `.jsonl`, is a session
 * file PATH, and anything else is a session ID that Pi matches inside the
 * working directory's session directory. The two answers must stay identical,
 * because this decides which rule judges a handle and Pi decides which lookup
 * consumes it — a value one reads as a path and the other as an ID is judged
 * by a rule that does not describe what happens to it. The worker's
 * `piResumeHandleIsFilePath` is the same test in Go, and
 * `testdata/pi_resume_handle_conformance.json` pins the two together.
 *
 * Neither rule can serve both shapes. The token rule bans `\` and caps the
 * value at 128 bytes, and a real Pi session path — an escaped copy of the
 * working directory plus a timestamped file name — passes neither, so it
 * refused every legitimate path with "Session ID contains invalid characters".
 * The path rule requires an absolute path, so it refused every legitimate ID,
 * which is relative by construction, with "path must be absolute".
 */
export function piValidateResumeHandle(value: string): string | null {
  if (value === '')
    return null
  if (!/[/\\]/.test(value) && !value.endsWith('.jsonl'))
    return validateSessionId(value)
  return validateSessionFilePath(value)
}
