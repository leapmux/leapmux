import type { DiracPlanReadiness } from './planReadiness'
import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { parseDiracPlanInput, withDiracPlanReadiness } from './planReadiness'

const SCRATCH_ROOT = resolve(process.cwd(), '../.tmp')
const PLAN_INPUT = {
  diracVersion: '0.5.16',
  hookName: 'Notification',
  taskId: 'native-task',
  notification: {
    event: 'user_attention',
    source: 'card_interaction',
    message: 'Proposed Plan',
    waitingForUserInput: true,
  },
}

let runDir = ''
let home = ''
const extraDirectories: string[] = []

beforeEach(() => {
  mkdirSync(SCRATCH_ROOT, { recursive: true })
  runDir = mkdtempSync(join(SCRATCH_ROOT, 'dirac-plan-readiness-test-'))
  home = join(runDir, 'home')
  mkdirSync(home)
})

afterEach(() => {
  if (runDir)
    rmSync(runDir, { recursive: true, force: true })
  runDir = ''
  home = ''
  for (const directory of extraDirectories.splice(0))
    rmSync(directory, { recursive: true, force: true })
})

function options() {
  return { home, runDir, nodePath: process.execPath }
}

function hookPath(): string {
  return join(home, '.dirac', 'Hooks', process.platform === 'win32' ? 'Notification.ps1' : 'Notification')
}

async function executeHook(readiness: DiracPlanReadiness, input: unknown) {
  const child = spawn(process.execPath, [readiness.scriptPath], { stdio: ['pipe', 'pipe', 'pipe'] })
  const stdout: string[] = []
  const stderr: string[] = []
  child.stdout.on('data', value => stdout.push(String(value)))
  child.stderr.on('data', value => stderr.push(String(value)))
  const completed = new Promise<number | null>((resolve, reject) => {
    child.once('error', reject)
    child.once('close', resolve)
  })
  child.stdin.end(JSON.stringify(input))
  return { exitCode: await completed, stdout: stdout.join(''), stderr: stderr.join('') }
}

describe('parseDiracPlanInput', () => {
  it('keeps the actual native task ID and plan attention fields', () => {
    expect(parseDiracPlanInput(PLAN_INPUT)).toEqual({
      hookName: 'Notification',
      taskId: 'native-task',
      notification: PLAN_INPUT.notification,
    })
  })

  it('rejects absent, empty, numeric, and false readiness fields', () => {
    const invalid: unknown[] = [
      null,
      [],
      {},
      { ...PLAN_INPUT, taskId: undefined },
      { ...PLAN_INPUT, taskId: '' },
      { ...PLAN_INPUT, taskId: '   ' },
      { ...PLAN_INPUT, taskId: 0 },
      { ...PLAN_INPUT, taskId: -1 },
      { ...PLAN_INPUT, hookName: 'Other' },
      { ...PLAN_INPUT, notification: undefined },
      { ...PLAN_INPUT, notification: { ...PLAN_INPUT.notification, event: 'task_complete' } },
      { ...PLAN_INPUT, notification: { ...PLAN_INPUT.notification, source: 'task_completion' } },
      { ...PLAN_INPUT, notification: { ...PLAN_INPUT.notification, message: 'Another card' } },
      { ...PLAN_INPUT, notification: { ...PLAN_INPUT.notification, waitingForUserInput: false } },
      { ...PLAN_INPUT, notification: { ...PLAN_INPUT.notification, waitingForUserInput: 0 } },
    ]
    for (const input of invalid)
      expect(() => parseDiracPlanInput(input)).toThrow('readiness record is invalid')
  })
})

describe('withDiracPlanReadiness', () => {
  it('waits for the actual hook signal and preserves the native hook reply', async () => {
    await withDiracPlanReadiness(options(), async (readiness) => {
      const [result, input] = await Promise.all([
        executeHook(readiness, PLAN_INPUT),
        readiness.waitForInput(),
      ])
      expect(result.exitCode).toBe(0)
      expect(result.stderr).toBe('')
      expect(JSON.parse(result.stdout)).toEqual({ cancel: false, contextModification: '', errorMessage: '' })
      expect(input.taskId).toBe('native-task')
      expect(input.notification.waitingForUserInput).toBe(true)
      expect(JSON.parse(readFileSync(readiness.signalPath, 'utf8'))).toEqual(PLAN_INPUT)
      expect(existsSync(`${readiness.signalPath}.partial`)).toBe(false)
    })
  })

  it('ignores an unrelated notification after its actual script exits', async () => {
    await withDiracPlanReadiness(options(), async (readiness) => {
      const result = await executeHook(readiness, {
        ...PLAN_INPUT,
        notification: { event: 'task_complete', source: 'task_completion', message: 'Task Completed', waitingForUserInput: false },
      })
      expect(result.exitCode).toBe(0)
      expect(JSON.parse(result.stdout)).toEqual({ cancel: false, contextModification: '', errorMessage: '' })
      expect(existsSync(readiness.signalPath)).toBe(false)
    })
  })

  it('rejects malformed native hook input without a readiness signal', async () => {
    await withDiracPlanReadiness(options(), async (readiness) => {
      const result = await executeHook(readiness, {})
      expect(result.exitCode).toBe(1)
      expect(result.stderr).toContain('Notification hook input is invalid')
      expect(result.stdout).toBe('')
      expect(existsSync(readiness.signalPath)).toBe(false)
    })
  })

  it('rejects a native plan attention event with no task ID', async () => {
    await withDiracPlanReadiness(options(), async (readiness) => {
      const result = await executeHook(readiness, { ...PLAN_INPUT, taskId: '' })
      expect(result.exitCode).toBe(1)
      expect(result.stderr).toContain('Notification hook requires a task ID')
      expect(existsSync(readiness.signalPath)).toBe(false)
    })
  })

  it('restores the prior private hook bytes and mode after success', async () => {
    const path = hookPath()
    mkdirSync(dirname(path), { recursive: true })
    writeFileSync(path, 'original private hook', { mode: 0o640 })
    const mode = statSync(path).mode & 0o777
    await withDiracPlanReadiness(options(), async (readiness) => {
      expect(readFileSync(readiness.hookPath, 'utf8')).not.toBe('original private hook')
    })
    expect(readFileSync(path, 'utf8')).toBe('original private hook')
    expect(statSync(path).mode & 0o777).toBe(mode)
  })

  it('restores the prior private hook bytes and mode after callback failure', async () => {
    const path = hookPath()
    mkdirSync(dirname(path), { recursive: true })
    writeFileSync(path, 'original private hook', { mode: 0o640 })
    const mode = statSync(path).mode & 0o777
    await expect(withDiracPlanReadiness(options(), async () => {
      throw new Error('The native browser assertion failed.')
    })).rejects.toThrow('browser assertion failed')
    expect(readFileSync(path, 'utf8')).toBe('original private hook')
    expect(statSync(path).mode & 0o777).toBe(mode)
  })

  it('removes its new hook and owned signal directory after cleanup', async () => {
    let signalDirectory = ''
    await withDiracPlanReadiness(options(), async (readiness) => {
      signalDirectory = dirname(readiness.signalPath)
      expect(existsSync(readiness.scriptPath)).toBe(true)
    })
    expect(existsSync(hookPath())).toBe(false)
    expect(existsSync(signalDirectory)).toBe(false)
  })

  it('rejects relative paths before it installs a hook', async () => {
    await expect(withDiracPlanReadiness({ ...options(), home: 'relative' }, async () => {})).rejects.toThrow('must be absolute')
    expect(existsSync(hookPath())).toBe(false)
  })

  it('rejects a global hook directory that resolves outside the private run', async () => {
    const outside = mkdtempSync(join(SCRATCH_ROOT, 'dirac-plan-outside-'))
    extraDirectories.push(outside)
    const outsideHooks = join(outside, 'Hooks')
    mkdirSync(outsideHooks)
    const outsideHook = join(outsideHooks, process.platform === 'win32' ? 'Notification.ps1' : 'Notification')
    writeFileSync(outsideHook, 'outside hook stays unchanged')
    symlinkSync(outside, join(home, '.dirac'), process.platform === 'win32' ? 'junction' : 'dir')
    await expect(withDiracPlanReadiness(options(), async () => {})).rejects.toThrow('outside the E2E run')
    expect(readFileSync(outsideHook, 'utf8')).toBe('outside hook stays unchanged')
  })
})
