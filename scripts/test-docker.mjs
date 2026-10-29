// Exercise the shipped entrypoint. Never replace /init in these checks.
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { setTimeout } from 'node:timers/promises'

const [image, ...extra] = process.argv.slice(2)
assert(image && !image.startsWith('-') && extra.length === 0, 'Usage: test-docker.mjs <image>')
const root = resolve(import.meta.dirname, '..')
mkdirSync(join(root, '.tmp'), { recursive: true })
const directory = mkdtempSync(join(root, '.tmp', 'docker-test-'))
const containers = []
const failures = []
const deadlineMs = 30000

function docker(args, timeout = deadlineMs) {
  const result = spawnSync('docker', args, { encoding: 'utf8', timeout, maxBuffer: 8 * 1024 * 1024 })
  if (result.error || result.status !== 0)
    throw new Error(`docker ${args[0]} failed: ${result.error?.message ?? result.stderr}`)
  return (args[0] === 'logs'
    ? result.stdout + result.stderr
    : result.stdout).trim()
}

function create(options = [], command = []) {
  const name = `leapmux-test-${randomUUID()}`
  docker([
    'create',
    '--name',
    name,
    ...options,
    image,
    ...command,
  ])
  containers.push(name)
  return name
}

function exited(name, expected) {
  assert.equal(Number(docker(['wait', name])), expected, docker(['logs', name]))
}

function copy(name, source, destination) {
  docker(['cp', source, `${name}:${destination}`])
}

async function ready(name) {
  const address = docker(['port', name, '4327/tcp'])
  const url = `http://${address}/leapmux.v1.AuthService/GetSystemInfo`
  const deadline = Date.now() + deadlineMs
  let lastError
  while (Date.now() < deadline) {
    if (docker([
      'inspect',
      '--format',
      '{{.State.Running}}',
      name,
    ]) !== 'true') {
      throw new Error(`Hub exited before readiness:\n${docker(['logs', name])}`)
    }
    try {
      const response = await fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: '{}',
        signal: AbortSignal.timeout(2000),
      })
      assert.equal(response.status, 200)
      assert.equal((await response.json()).setupRequired, true)
      return
    }
    catch (error) {
      lastError = error
    }
    // Docker startup is external state. Poll readiness, never assume a startup duration.
    await setTimeout(100)
  }
  throw new Error(`Hub did not become ready: ${lastError?.message}\n${docker(['logs', name])}`)
}

try {
  console.log(`Testing ${image}. Diagnostics: ${directory}`)
  const hub = create([
    '-e',
    'LEAPMUX_MODE=hub',
    '-p',
    '127.0.0.1::4327',
  ])
  docker(['start', hub])
  await ready(hub)
  const key = docker([
    'exec',
    hub,
    'sha256sum',
    '/data/hub/encryption.key',
  ])
  docker(['stop', '--time', '10', hub])
  exited(hub, 0)
  docker(['start', hub])
  await ready(hub)
  assert.equal(docker([
    'exec',
    hub,
    'sha256sum',
    '/data/hub/encryption.key',
  ]), key)
  docker(['stop', '--time', '10', hub])
  exited(hub, 0)
  console.log('PASS: Hub startup, graceful stop, restart, and persistent encryption key')

  // The probe checks the startup script's arguments, environment, and filesystem effects.
  // Its exit status also verifies that s6 propagates the primary process's result.
  const probe = join(directory, 'probe')
  writeFileSync(probe, `#!/bin/sh
set -eu
test "$#" -eq 3
test "$1" = "$LEAPMUX_MODE"
test "$2" = -config
test "$3" = "$LEAPMUX_DATA_DIR/$LEAPMUX_MODE/$LEAPMUX_MODE.yaml"
test "$LEAPMUX_HUB_LOG_LEVEL" = debug
test -f "$3"
test "$(stat -c %a "$3")" = 600
if test "$PROBE_EXISTING" = yes; then
  test "$(cat "$3")" = preserved
else
  test ! -s "$3"
fi
echo probe-ran
if test "$PROBE_EXIT" = signal; then kill -KILL $$; fi
exit "$PROBE_EXIT"
`, { mode: 0o755 })

  for (const [mode, result, existing] of [
    ['hub', '0', false],
    ['worker', '23', false],
    ['dev', '0', true],
    ['solo', 'signal', false],
  ]) {
    const data = '/custom data'
    const name = create([
      '-e',
      `LEAPMUX_MODE=${mode}`,
      '-e',
      `LEAPMUX_DATA_DIR=${data}`,
      '-e',
      'LEAPMUX_BIN=/probe',
      '-e',
      'LEAPMUX_HUB_LOG_LEVEL=debug',
      '-e',
      `PROBE_EXIT=${result}`,
      '-e',
      `PROBE_EXISTING=${existing ? 'yes' : 'no'}`,
    ])
    copy(name, probe, '/probe')
    if (existing) {
      const configDir = join(directory, 'existing', mode)
      mkdirSync(configDir, { recursive: true })
      writeFileSync(join(configDir, `${mode}.yaml`), 'preserved', { mode: 0o600 })
      copy(name, join(directory, 'existing'), data)
    }
    docker(['start', name])
    exited(name, result === 'signal'
      ? 137
      : Number(result))
    assert.match(docker(['logs', name]), /probe-ran/)
    console.log(`PASS: ${mode} arguments, environment, configuration, and exit ${result}`)
  }

  for (const mode of [null, '', 'invalid']) {
    const name = create(mode === null
      ? []
      : ['-e', `LEAPMUX_MODE=${mode}`])
    docker(['start', name])
    exited(name, 1)
    assert.match(docker(['logs', name]), /LEAPMUX_MODE must be one of/)
    console.log(`PASS: rejected mode ${JSON.stringify(mode)}`)
  }

  const unexpected = join(directory, 'unexpected')
  writeFileSync(unexpected, '#!/bin/sh\necho probe-ran\nexit 0\n', { mode: 0o755 })
  // /proc cannot hold an application directory, even when the container runs as root.
  const badDirectory = create([
    '-e',
    'LEAPMUX_MODE=hub',
    '-e',
    'LEAPMUX_DATA_DIR=/proc/leapmux-test',
    '-e',
    'LEAPMUX_BIN=/probe',
  ])
  copy(badDirectory, unexpected, '/probe')
  docker(['start', badDirectory])
  assert.notEqual(Number(docker(['wait', badDirectory])), 0)
  assert.doesNotMatch(docker(['logs', badDirectory]), /probe-ran/)
  console.log('PASS: directory creation failure stops startup')

  const configFailure = create([
    '-e',
    'LEAPMUX_MODE=hub',
    '-e',
    'LEAPMUX_BIN=/probe',
  ])
  const blocked = join(directory, 'blocked', 'hub', 'hub.yaml')
  mkdirSync(blocked, { recursive: true })
  copy(configFailure, join(directory, 'blocked', 'hub'), '/data/hub')
  copy(configFailure, unexpected, '/probe')
  docker(['start', configFailure])
  assert.notEqual(Number(docker(['wait', configFailure])), 0)
  assert.doesNotMatch(docker(['logs', configFailure]), /probe-ran/)
  console.log('PASS: invalid configuration path stops startup')

  const readOnly = create([
    '-e',
    'LEAPMUX_MODE=hub',
    '-e',
    'LEAPMUX_BIN=/probe',
    '--tmpfs',
    '/data/hub:ro',
  ])
  copy(readOnly, unexpected, '/probe')
  docker(['start', readOnly])
  assert.notEqual(Number(docker(['wait', readOnly])), 0)
  assert.doesNotMatch(docker(['logs', readOnly]), /probe-ran/)
  assert.match(docker(['logs', readOnly]), /[Rr]ead-only file system/)
  console.log('PASS: configuration creation failure stops startup')

  const missingBinary = create([
    '-e',
    'LEAPMUX_MODE=hub',
    '-e',
    'LEAPMUX_BIN=/missing-leapmux-binary',
  ])
  docker(['start', missingBinary])
  exited(missingBinary, 127)
  console.log('PASS: missing executable returns exit 127')

  const override = create([], ['/bin/sh', '-c', 'exit 19'])
  docker(['start', override])
  exited(override, 19)
  console.log('PASS: explicit Docker command exits with its own status')
}
catch (error) {
  failures.push(error)
}
finally {
  for (const name of containers) {
    try {
      writeFileSync(join(directory, `${name}.log`), docker(['logs', name]))
    }
    catch (error) {
      failures.push(error)
    }
    try {
      docker(['rm', '--force', '--volumes', name])
    }
    catch (error) {
      failures.push(error)
    }
  }
}
if (failures.length > 0)
  throw new AggregateError(failures, `Image tests failed. Diagnostics: ${directory}`)
