import { describe, expect, it } from 'vitest'
import { antigravityCommandOutput } from './execute'
import { antigravityStep, antigravityStepFailed } from './toolCommon'

describe('antigravityCommandOutput', () => {
  it('removes native bookkeeping and preserves truncation details and output bytes', () => {
    const output = [
      'Created At: 2026-11-05T00:00:00Z',
      'Completed At: 2026-11-05T00:00:01Z',
      'The tool\'s output was truncated. You can find the full output in a file at file:///p/output.txt.',
      '',
      'Here is a preview:',
      'Command exited with code 0.',
      '',
      'Command output:',
      'first line\r',
      'second line',
      '',
    ].join('\n')
    expect(antigravityCommandOutput(output)).toBe([
      'The tool\'s output was truncated. You can find the full output in a file at file:///p/output.txt.',
      '',
      'Here is a preview:',
      '',
      'first line\r',
      'second line',
      '',
    ].join('\n'))
  })

  it('treats a nonzero exit code as a failed step even when Antigravity says DONE', () => {
    const step = antigravityStep({
      type: 'CORTEX_STEP_TYPE_RUN_COMMAND',
      status: 'CORTEX_STEP_STATUS_DONE',
      runCommand: { exitCode: 7 },
    })!
    expect(antigravityStepFailed(step)).toBe(true)
    expect(antigravityStepFailed(antigravityStep({
      ...step,
      runCommand: { exitCode: 0 },
    })!)).toBe(false)
  })

  it('returns output without the native result marker unchanged', () => {
    expect(antigravityCommandOutput('Created At: actual command text\nplain output\n')).toBe('Created At: actual command text\nplain output\n')
    expect(antigravityCommandOutput('')).toBe('')
  })
})
