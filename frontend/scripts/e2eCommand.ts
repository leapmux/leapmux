import type { SpawnOptions } from 'node:child_process'
import type { CommandProcess, CommandProcessOwnership } from './e2eCommandProcess'
import { Buffer } from 'node:buffer'
import { closeSync, openSync, writeFileSync } from 'node:fs'
import process from 'node:process'
import { StringDecoder } from 'node:string_decoder'
import { spawnCommandProcess } from './e2eCommandProcess'

export interface CommandRuntime {
  logPath?: string
  label?: string
  ownership?: CommandProcessOwnership
  observe?: (command: CommandProcess) => void
}

/** Run one command. Retain its output and wait for stream closure before returning. */
export async function runCommand(cmd: string, args: string[], options: SpawnOptions = {}, runtime: CommandRuntime = {}): Promise<number> {
  const ownership = { ...runtime.ownership }
  const ownTree = ownership.ownTree === true
  const log = runtime.logPath ? openSync(runtime.logPath, 'wx') : undefined
  let command: CommandProcess
  try {
    command = spawnCommandProcess(cmd, args, { ...options, stdio: log === undefined ? 'inherit' : ['inherit', 'pipe', 'pipe'], env: options.env ?? process.env }, ownership)
  }
  catch (error) {
    if (log !== undefined) {
      try {
        closeSync(log)
      }
      catch (closeError) {
        throw new AggregateError([error, closeError], 'The E2E command start and its log close failed.')
      }
    }
    throw error
  }
  const child = command.child
  return new Promise((accept, reject) => {
    let finished = false
    let stopping: Promise<void> | undefined
    const failures: unknown[] = []
    const decoders = [new StringDecoder('utf8'), new StringDecoder('utf8')]
    const pending = ['', '']
    const recordFailure = (error: unknown) => {
      if (!failures.includes(error))
        failures.push(error)
    }
    const writeLine = (index: number, line: string) => {
      const target = index === 0 ? process.stdout : process.stderr
      target.write(`${runtime.label ? `[${runtime.label}] ` : ''}${line}\n`)
    }
    const finish = (code: number) => {
      if (finished)
        return
      finished = true
      for (const [index, decoder] of decoders.entries()) {
        try {
          pending[index] += decoder.end()
          if (pending[index])
            writeLine(index, pending[index]!)
        }
        catch (error) {
          recordFailure(error)
        }
      }
      if (log !== undefined) {
        try {
          closeSync(log)
        }
        catch (error) {
          recordFailure(error)
        }
      }
      void (async () => {
        try {
          await stopping
          if (ownTree)
            await command.stop()
        }
        catch (error) {
          recordFailure(error)
        }
        if (failures.length === 1)
          reject(failures[0])
        else if (failures.length > 1)
          reject(new AggregateError(failures, 'The E2E command and its cleanup failed.'))
        else
          accept(code)
      })()
    }
    const abort = (error: unknown) => {
      recordFailure(error)
      stopping ??= command.stop()
      void stopping.catch((stopError) => {
        recordFailure(stopError)
        finish(1)
      })
    }
    const output = (index: number, chunk: Uint8Array) => {
      if (finished)
        return
      try {
        if (log !== undefined)
          writeFileSync(log, chunk)
        pending[index] += decoders[index]!.write(Buffer.from(chunk))
        const lines = pending[index]!.split('\n')
        pending[index] = lines.pop() ?? ''
        for (const line of lines)
          writeLine(index, line)
      }
      catch (error) {
        abort(error)
      }
    }
    child.stdout?.on('data', chunk => output(0, chunk))
    child.stderr?.on('data', chunk => output(1, chunk))
    child.stdout?.on('error', abort)
    child.stderr?.on('error', abort)
    child.once('error', (error) => {
      abort(error)
      finish(1)
    })
    child.once('close', code => finish(code ?? 1))
    try {
      runtime.observe?.(command)
    }
    catch (error) {
      abort(error)
    }
  })
}
