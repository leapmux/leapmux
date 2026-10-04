import { chmodSync, existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'
import process from 'node:process'
import { isObject } from '../../../src/lib/jsonPick'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { waitForFileSignal } from '../helpers/toolOutputControl'

export interface DiracPlanInput {
  hookName: 'Notification'
  taskId: string
  notification: {
    event: 'user_attention'
    source: 'card_interaction'
    message: 'Proposed Plan'
    waitingForUserInput: true
  }
}

export interface DiracPlanReadiness {
  hookPath: string
  scriptPath: string
  signalPath: string
  waitForInput: () => Promise<DiracPlanInput>
}

/** Read the native plan attention record that follows the response-field clear. */
export function parseDiracPlanInput(value: unknown): DiracPlanInput {
  const notification = isObject(value) && isObject(value.notification) ? value.notification : undefined
  if (!isObject(value) || value.hookName !== 'Notification'
    || typeof value.taskId !== 'string' || value.taskId.trim() === ''
    || !notification || notification.event !== 'user_attention'
    || notification.source !== 'card_interaction' || notification.message !== 'Proposed Plan'
    || notification.waitingForUserInput !== true) {
    throw new Error('The native Dirac plan readiness record is invalid.')
  }
  return {
    hookName: 'Notification',
    taskId: value.taskId,
    notification: {
      event: 'user_attention',
      source: 'card_interaction',
      message: 'Proposed Plan',
      waitingForUserInput: true,
    },
  }
}

function assertExistingPrivatePath(path: string, runDir: string): void {
  try {
    lstatSync(path)
  }
  catch (error) {
    if (isObject(error) && error.code === 'ENOENT')
      return
    throw error
  }
  assertPrivateNativePath(path, runDir)
}

/** Observe native card input readiness through a private Notification hook. */
export async function withDiracPlanReadiness(
  options: { home: string, nodePath: string, runDir: string },
  use: (readiness: DiracPlanReadiness) => Promise<void>,
): Promise<void> {
  if (!isAbsolute(options.home) || !isAbsolute(options.nodePath) || !isAbsolute(options.runDir))
    throw new Error('The native plan readiness paths must be absolute.')
  assertPrivateNativePath(options.home, options.runDir)
  const directory = join(options.home, '.dirac')
  const hooksDirectory = join(directory, 'Hooks')
  const hookPath = join(hooksDirectory, process.platform === 'win32' ? 'Notification.ps1' : 'Notification')
  for (const path of [directory, hooksDirectory, hookPath])
    assertExistingPrivatePath(path, options.runDir)
  mkdirSync(hooksDirectory, { recursive: true })
  assertPrivateNativePath(hooksDirectory, options.runDir)
  const previous = existsSync(hookPath) ? { bytes: readFileSync(hookPath), mode: statSync(hookPath).mode & 0o777 } : undefined
  const signalDirectory = mkdtempSync(join(options.runDir, 'dirac-plan-readiness-'))
  const signalPath = join(signalDirectory, 'ready.json')
  const scriptPath = join(signalDirectory, 'notification.cjs')
  const source = `
const fs = require('node:fs')
const input = JSON.parse(fs.readFileSync(0, 'utf8'))
if (!input || typeof input !== 'object' || Array.isArray(input)
  || input.hookName !== 'Notification' || !input.notification
  || typeof input.notification !== 'object' || Array.isArray(input.notification)
  || typeof input.notification.event !== 'string') {
  throw new Error('The native Notification hook input is invalid.')
}
const notification = input.notification
if (notification.event === 'user_attention' && notification.source === 'card_interaction'
  && notification.message === 'Proposed Plan' && notification.waitingForUserInput === true) {
  if (typeof input.taskId !== 'string' || !input.taskId.trim())
    throw new Error('The native Notification hook requires a task ID.')
  const signal = ${JSON.stringify(signalPath)}
  fs.writeFileSync(signal + '.partial', JSON.stringify(input), { mode: 0o600 })
  fs.renameSync(signal + '.partial', signal)
}
process.stdout.write(JSON.stringify({ cancel: false, contextModification: '', errorMessage: '' }) + '\\n')
`
  const launcher = process.platform === 'win32'
    ? `$nativeHookInput = [Console]::In.ReadToEnd()\n$nativeHookInput | & '${options.nodePath.replaceAll('\'', '\'\'')}' '${scriptPath.replaceAll('\'', '\'\'')}'\nexit $LASTEXITCODE\n`
    : `#!/bin/sh\nexec ${quotePosixShellArgument(options.nodePath)} ${quotePosixShellArgument(scriptPath)}\n`
  await withCleanup(async () => {
    writeFileSync(scriptPath, source, { mode: 0o600 })
    writeFileSync(hookPath, launcher, { mode: 0o700 })
    chmodSync(hookPath, 0o700)
    await use({
      hookPath,
      scriptPath,
      signalPath,
      waitForInput: async () => {
        // ACP removes its message listener at end_turn before this native hook executes.
        await waitForFileSignal(signalPath)
        return parseDiracPlanInput(JSON.parse(readFileSync(signalPath, 'utf8')))
      },
    })
  }, async () => {
    const restoreHook = async () => {
      if (previous) {
        writeFileSync(hookPath, previous.bytes)
        chmodSync(hookPath, previous.mode)
      }
      else {
        rmSync(hookPath, { force: true })
      }
    }
    const removeSignalDirectory = async () => {
      rmSync(signalDirectory, { recursive: true, force: true })
    }
    await finishCleanup([restoreHook(), removeSignalDirectory()])
  })
}
