import { mkdirSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'
import process from 'node:process'

/** Keep optional structured reports beside the owning suite's console and result. */
export function testReportPath(label, env = process.env) {
  if (typeof label !== 'string' || !/^[a-z0-9][a-z0-9-]*$/i.test(label))
    throw new Error('The test report label must start with a letter or digit and contain only letters, digits, or hyphens.')
  const directory = env.LEAPMUX_TEST_RESULTS_DIR
  if (!directory)
    return undefined
  if (!isAbsolute(directory) || directory.includes('\0'))
    throw new Error('The test results directory must be an absolute path without NUL.')
  mkdirSync(directory, { recursive: true })
  return join(directory, `${label}.xml`)
}
