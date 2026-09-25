import { validateSessionFileOrIdHandle } from '~/lib/validate'

/**
 * Oh My Pi's resume handle comes in two shapes, and `omp --resume` picks the lookup
 * by shape, with the test Pi's resolver uses: a value that holds a separator, or ends
 * in `.jsonl`, is a session file PATH, and anything else is a session ID.
 *
 * The worker stores the FILE, because `--resume <path>` also resumes a session whose
 * file omp has not written yet, while `--resume <id>` exits for one. A reader may
 * still paste an ID, which omp accepts.
 */
export function ohMyPiValidateResumeHandle(value: string): string | null {
  return validateSessionFileOrIdHandle(value)
}
