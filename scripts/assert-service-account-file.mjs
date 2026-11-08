import { stat } from 'node:fs/promises'
import path from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'

export function isAbsoluteServiceAccountFile(value, platform = process.platform) {
  if (typeof value !== 'string' || value === '')
    return false
  if (platform === 'win32') {
    return path.win32.isAbsolute(value)
      && (/^[a-z]:[\\/]/i.test(value) || /^[\\/]{2}[^\\/]+[\\/][^\\/]+/.test(value))
  }
  return path.posix.isAbsolute(value)
}

export async function validateServiceAccountFile(value) {
  if (!isAbsoluteServiceAccountFile(value))
    throw new Error('SERVICE_ACCOUNT_FILE must be an absolute path to the private native-account fixture')
  const info = await stat(value)
  if (!info.isFile())
    throw new Error('SERVICE_ACCOUNT_FILE must point to a regular file')
  return value
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    await validateServiceAccountFile(process.argv[2])
  }
  catch (error) {
    console.error(error instanceof Error
      ? error.message
      : String(error))
    process.exitCode = 1
  }
}
