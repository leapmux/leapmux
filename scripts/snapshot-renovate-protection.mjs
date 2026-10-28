#!/usr/bin/env node
import { execFile } from 'node:child_process'
import process from 'node:process'
import { pathToFileURL } from 'node:url'
import { promisify } from 'node:util'
import { createProtectionSnapshot, readRepositoryProtection } from './check-renovate-protection.mjs'

const execute = promisify(execFile)

/** Use the administrator's existing gh authentication without retrieving its token. */
export async function readProtectionWithCli(path, run = execute) {
  if (!/^\/repos\/leapmux\/leapmux\/(?:rules\/branches\/main\?per_page=100&page=[1-9]\d*|rulesets\/[1-9]\d*)$/.test(path))
    throw new Error('The snapshot CLI can only read leapmux/leapmux main protection.')
  const { stdout } = await run('gh', ['api', '--hostname', 'github.com', '--method', 'GET', '-H', 'X-GitHub-Api-Version: 2022-11-28', '-H', 'Cache-Control: no-cache', path], { encoding: 'utf8', maxBuffer: 4 * 1024 * 1024, timeout: 30000 })
  return JSON.parse(stdout)
}

export async function generateProtectionSnapshot(get = readProtectionWithCli) {
  const { effective, rulesets } = await readRepositoryProtection(get)
  return createProtectionSnapshot(effective, rulesets)
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    if (process.argv.length !== 2)
      throw new Error('The snapshot command accepts no arguments.')
    console.log(JSON.stringify(await generateProtectionSnapshot(), null, 2))
  }
  catch (error) {
    console.error(`Administrator protection snapshot failed: ${error.message}`)
    process.exitCode = 1
  }
}
