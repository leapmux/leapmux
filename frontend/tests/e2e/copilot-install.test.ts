import { chmodSync, mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { computeCopilotE2ESkipReason, COPILOT_MISSING_REASON, probeCopilotE2ESkipReason } from './copilot-install'
import { writeNodeLauncher } from './helpers/nodeLauncher'

let directory: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'copilot-install-test-'))
})

afterEach(() => rmSync(directory, { recursive: true, force: true }))

describe('computeCopilotE2ESkipReason', () => {
  it('accepts the minimum release and every later one', () => {
    for (const output of [
      'GitHub Copilot CLI 1.0.103.',
      'GitHub Copilot CLI 1.0.103-beta.1',
      'GitHub Copilot CLI 1.0.104.',
      '1.1.0',
      '2.0.0',
      'GitHub Copilot CLI 1.0.104-beta.2',
    ])
      expect(computeCopilotE2ESkipReason(output), output).toBeNull()
  })

  it('refuses an earlier release and states the detected and the required version', () => {
    for (const tc of [
      { output: 'GitHub Copilot CLI 1.0.102.', detected: '1.0.102' },
      { output: '0.9.9', detected: '0.9.9' },
      { output: '0.103.0', detected: '0.103.0' },
    ]) {
      const reason = computeCopilotE2ESkipReason(tc.output)
      expect(reason, tc.output).toContain(tc.detected)
      expect(reason, tc.output).toContain('GitHub Copilot CLI 1.0.103 or later')
    }
  })

  it('refuses output that states no usable release number and states the requirement', () => {
    for (const output of ['', 'copilot version two', '1.0', '99999999999999999999.0.0']) {
      const reason = computeCopilotE2ESkipReason(output)
      expect(reason, output).toContain('GitHub Copilot CLI 1.0.103 or later')
      expect(reason, output).toContain('states no release number')
    }
  })

  it('reports no CLI when the probe produced no output', () => {
    expect(computeCopilotE2ESkipReason(null)).toBe(COPILOT_MISSING_REASON)
  })

  it('takes the first release number when the output holds several', () => {
    expect(computeCopilotE2ESkipReason('GitHub Copilot CLI 1.0.103\nsee 2.0.0 for details')).toBeNull()
    expect(computeCopilotE2ESkipReason('GitHub Copilot CLI 1.0.102\n2.0.0 carries the new flags'))
      .toContain('1.0.102')
  })
})

describe('probeCopilotE2ESkipReason', () => {
  /** A version probe that must not run: the lookup found no file to run. */
  const failIfCalled = () => {
    throw new Error('the version probe must not run when the lookup finds no file')
  }

  // COPILOT_AUTO_UPDATE selects which runtime `--version` reports, so the probe
  // pins it exactly as the worker's launch does. The fake reports 1.0.103 unless
  // the pin arrives, and 1.0.102 when it does: the preflight must read the pinned
  // runtime, which is the one the worker starts.
  it('reads the version of the runtime that the updater pin selects', () => {
    const script = join(directory, 'version.mjs')
    writeFileSync(script, [
      `const pinned = process.env.COPILOT_AUTO_UPDATE === 'false'`,
      `process.stdout.write('GitHub Copilot CLI ' + (pinned ? '1.0.102' : '1.0.103') + '.')`,
      '',
    ].join('\n'))
    writeNodeLauncher(directory, 'copilot', { node: process.execPath, script })

    const reason = probeCopilotE2ESkipReason({ PATH: directory, COPILOT_AUTO_UPDATE: 'true' })
    expect(reason).toContain('1.0.102')
    expect(reason).toContain('GitHub Copilot CLI 1.0.103 or later')
  })

  it('reports the missing CLI without running a version probe', () => {
    expect(probeCopilotE2ESkipReason({ PATH: join(directory, 'empty') }, undefined, failIfCalled))
      .toBe(COPILOT_MISSING_REASON)
  })

  // A mise shim is a link named for its tool to the mise executable, and the
  // isolated HOME of the run stops it (see unusableBinaryReason).
  it.skipIf(process.platform === 'win32')('reports the mise-shim cause without running a version probe', () => {
    const mise = join(directory, 'mise-bin', 'mise')
    mkdirSync(dirname(mise), { recursive: true })
    writeFileSync(mise, '#!/bin/sh\n')
    chmodSync(mise, 0o755)
    const shims = join(directory, 'shims')
    mkdirSync(shims, { recursive: true })
    symlinkSync(mise, join(shims, 'copilot'))

    const reason = probeCopilotE2ESkipReason({ PATH: shims }, undefined, failIfCalled)
    expect(reason).toContain('mise shim')
  })
})
