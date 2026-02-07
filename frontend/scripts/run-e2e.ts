import { spawn } from 'node:child_process'
import { mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import process from 'node:process'

function run(cmd: string, args: string[]): Promise<number> {
  return new Promise((resolve) => {
    const child = spawn(cmd, args, {
      stdio: 'inherit',
      env: process.env,
    })
    child.on('exit', code => resolve(code ?? 1))
  })
}

async function main() {
  // Always clean and build before running e2e tests
  const buildCode = await run('task', ['build-frontend', 'build-backend'])
  if (buildCode !== 0) {
    console.error('`task clean build` failed with exit code', buildCode)
    process.exit(buildCode)
  }

  // Write a nonce file and pass its path via env var so global-setup.ts can verify
  // tests were launched via this script (not by manually setting LEAPMUX_E2E_RUNNER=1).
  const nonceDir = mkdtempSync(join(tmpdir(), 'leapmux-e2e-nonce-'))
  const noncePath = join(nonceDir, 'nonce')
  const nonce = crypto.randomUUID()
  writeFileSync(noncePath, nonce)
  process.env.LEAPMUX_E2E_NONCE_PATH = noncePath
  process.env.LEAPMUX_E2E_NONCE = nonce

  const testCode = await run('bunx', ['playwright', 'test', ...process.argv.slice(2)])
  process.exit(testCode)
}

main()
