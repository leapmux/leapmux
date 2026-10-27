#!/usr/bin/env bun
import { spawnSync } from 'node:child_process'
import { readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'

export function synchronizeGenerators(config, goMetadata, frontend) {
  const runtimes = new Map([
    ['buf.build/protocolbuffers/go', 'google.golang.org/protobuf'],
    ['buf.build/connectrpc/go', 'connectrpc.com/connect'],
  ])
  const versions = new Map()
  for (const [generator, module] of runtimes) {
    const family = new RegExp(`^${RegExp.escape(module)}(?:/v(\\d+))?$`)
    const requirements = (goMetadata?.Require ?? []).filter(entry => !entry.Indirect && family.test(entry.Path))
    if (requirements.length !== 1)
      throw new Error(`Expected one direct runtime requirement for ${module}.`)
    const selected = requirements[0]
    const version = /^v(\d+)\.\d+\.\d+$/.exec(selected.Version)
    if (!version || !Number.isSafeInteger(Number(version[1])))
      throw new Error(`Missing exact runtime version for ${module}.`)
    const suffix = family.exec(selected.Path)[1]
    if (suffix ? Number(suffix) < 2 || String(Number(suffix)) !== suffix || suffix !== version[1] : Number(version[1]) > 1)
      throw new Error(`The runtime path and version disagree for ${selected.Path}.`)
    versions.set(generator, selected.Version)
  }
  const es = frontend?.dependencies?.['@bufbuild/protobuf']
  if (!/^\d+\.\d+\.\d+$/.test(es ?? ''))
    throw new Error('@bufbuild/protobuf requires an exact version.')
  versions.set('buf.build/bufbuild/es', `v${es}`)
  const parsed = Bun.YAML.parse(config)
  for (const [generator, version] of versions) {
    const entries = (parsed.plugins ?? []).filter(plugin => typeof plugin.remote === 'string' && plugin.remote.startsWith(`${generator}:`))
    if (entries.length !== 1)
      throw new Error(`Expected one generator entry for ${generator}.`)
    // Replace only a literal mapping value, preserving comments and the checked-in YAML format.
    const pattern = new RegExp(`(^[\\t ]*(?:-[\\t ]*)?["']?remote["']?:[\\t ]*["']?)${RegExp.escape(entries[0].remote)}(["']?)(?=[\\t ]*(?:#|$))`, 'gm')
    if ([...config.matchAll(pattern)].length !== 1)
      throw new Error(`Expected one literal generator entry for ${generator}.`)
    config = config.replace(pattern, `$1${generator}:${version}$2`)
  }
  return config
}

if (import.meta.main) {
  try {
    if (process.argv.slice(2).some(arg => arg !== '--check'))
      throw new Error('Only --check is supported.')
    const root = resolve(import.meta.dirname, '..')
    const file = resolve(root, 'buf.gen.yaml')
    const original = readFileSync(file, 'utf8')
    const result = spawnSync('go', ['mod', 'edit', '-json'], { cwd: resolve(root, 'backend'), encoding: 'utf8', env: { ...process.env, GOWORK: 'off', GOTOOLCHAIN: 'local' }, maxBuffer: 4 * 1024 * 1024 })
    if (result.status !== 0)
      throw new Error(`Cannot read backend module metadata: ${result.error?.message ?? result.stderr}`)
    const updated = synchronizeGenerators(original, JSON.parse(result.stdout), JSON.parse(readFileSync(resolve(root, 'frontend/package.json'), 'utf8')))
    if (process.argv.includes('--check') && updated !== original)
      throw new Error('Protobuf generator versions differ from runtime versions. Run task sync-protobuf-generators.')
    if (!process.argv.includes('--check') && updated !== original)
      writeFileSync(file, updated)
  }
  catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
