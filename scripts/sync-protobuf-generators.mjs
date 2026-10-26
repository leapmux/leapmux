#!/usr/bin/env bun
import { readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'

export function synchronizeGenerators(config, goModule, frontend) {
  const runtimes = new Map([
    ['buf.build/protocolbuffers/go', 'google.golang.org/protobuf'],
    ['buf.build/connectrpc/go', 'connectrpc.com/connect'],
  ])
  const versions = new Map()
  for (const [generator, module] of runtimes) {
    const fields = goModule.split('\n').map(line => line.trim().split(/\s+/)).find(fields => fields[0] === module)
    if (!/^v\d+\.\d+\.\d+$/.test(fields?.[1] ?? ''))
      throw new Error(`Missing exact runtime version for ${module}.`)
    versions.set(generator, fields[1])
  }
  const es = frontend.dependencies?.['@bufbuild/protobuf']
  if (!/^\d+\.\d+\.\d+$/.test(es ?? ''))
    throw new Error('@bufbuild/protobuf requires an exact version.')
  versions.set('buf.build/bufbuild/es', `v${es}`)
  for (const [generator, version] of versions) {
    const escaped = generator.replaceAll('.', '\\.').replaceAll('/', '\\/')
    const pattern = new RegExp(`(remote: ${escaped}:)v[^\\s]+`, 'g')
    if ([...config.matchAll(pattern)].length !== 1)
      throw new Error(`Expected one generator entry for ${generator}.`)
    config = config.replace(pattern, `$1${version}`)
  }
  return config
}

if (import.meta.main) {
  try {
    const root = resolve(import.meta.dirname, '..')
    const file = resolve(root, 'buf.gen.yaml')
    const original = readFileSync(file, 'utf8')
    const updated = synchronizeGenerators(original, readFileSync(resolve(root, 'backend/go.mod'), 'utf8'), JSON.parse(readFileSync(resolve(root, 'frontend/package.json'), 'utf8')))
    if (process.argv.slice(2).some(arg => arg !== '--check'))
      throw new Error('Only --check is supported.')
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
