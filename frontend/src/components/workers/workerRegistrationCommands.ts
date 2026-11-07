/**
 * Shell quoting for the worker registration commands.
 *
 * Each shell keeps its own quoting rules; the registration dialog
 * renders one command per shell, and a command whose argument would
 * change meaning under the shell's rules must be refused rather than
 * rendered.
 */

/** The shells the dialog offers. */
export type ShellKind = 'posix' | 'powershell'

/**
 * The character codes that cannot form a safe displayed command in ANY
 * shell: C0 controls, DEL. Written as a predicate rather than a regex
 * so the control characters are named, not patterned.
 */
function isProhibitedControlCharacter(ch: string): boolean {
  const code = ch.codePointAt(0) ?? -1
  return (code >= 0x00 && code <= 0x1F) || code === 0x7F
}

/** True when the argument is safe to embed in a displayed command. */
function isSafeArgument(value: string): boolean {
  if (value === '')
    return false
  for (const ch of value) {
    if (isProhibitedControlCharacter(ch))
      return false
  }
  return true
}

/** Quote one POSIX shell word in single quotes, escaping embedded ones. */
function posixQuote(value: string): string {
  return `'${value.replaceAll('\'', `'\\''`)}'`
}

/** Quote one PowerShell argument, preferring single quotes. */
function powershellQuote(value: string): string {
  // PowerShell recognizes these typographic characters as single quotes too.
  return `'${value.replace(/['\u2018-\u201B]/gu, quote => quote + quote)}'`
}

/** Quote a native argument for ProcessStartInfo's command-line parser. */
function windowsArgument(value: string): string {
  const escaped = value.replace(/(\\*)"/gu, '$1$1\\"').replace(/(\\+)$/u, '$1$1')
  return `"${escaped}"`
}

function powershellRegistration(hubUrl: string, registrationKey: string): string {
  const argumentsText = `worker register --hub ${windowsArgument(hubUrl)} --registration-key ${windowsArgument(registrationKey)}`
  // Direct native invocation in Windows PowerShell changes embedded quotes.
  // ProcessStartInfo bypasses that conversion on both PowerShell runtimes.
  return `& { $ErrorActionPreference = 'Stop'; $info = New-Object System.Diagnostics.ProcessStartInfo; $info.FileName = (Get-Command 'leapmux' -CommandType Application -ErrorAction Stop).Source; $info.UseShellExecute = $false; $info.Arguments = ${powershellQuote(argumentsText)}; $process = [System.Diagnostics.Process]::Start($info); try { $process.WaitForExit(); $code = $process.ExitCode } finally { $process.Dispose() }; $global:LASTEXITCODE = $code; if ($code -ne 0) { throw "Worker registration failed with exit code $code" } }`
}

export interface RegistrationCommands {
  /** `leapmux worker register --hub … --registration-key …` */
  register: string
  /** `leapmux worker start` after registration succeeded */
  start: string
}

export interface CommandInputs {
  hubUrl: string
  registrationKey: string
  shell: ShellKind
}

/** Control characters that cannot form a safe command. */
/** The per-character predicate the dialog's refusal surfaces. */
export const isUnsafeCharacter = isProhibitedControlCharacter

/**
 * Build the two-step registration commands for one shell.
 *
 * Registration and startup stay SEPARATE commands: the user must see
 * that startup follows successful registration, never a single
 * multiline command that would run the worker even after a failed
 * registration.
 */
export function workerRegistrationCommands({ hubUrl, registrationKey, shell }: CommandInputs): RegistrationCommands | null {
  if (!isSafeArgument(hubUrl) || !isSafeArgument(registrationKey))
    return null
  return {
    register: shell === 'powershell'
      ? powershellRegistration(hubUrl, registrationKey)
      : `leapmux worker register --hub ${posixQuote(hubUrl)} --registration-key ${posixQuote(registrationKey)}`,
    start: 'leapmux worker start',
  }
}
