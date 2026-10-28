import { spawnSync } from 'node:child_process'
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'

const root = resolve(import.meta.dirname, '..')
const fixtures = []

afterEach(() => {
  for (const directory of fixtures.splice(0))
    rmSync(directory, { recursive: true, force: true })
})

describe('frontend dependency installation', () => {
  it('leaves the disk image native build to Task without disabling other install scripts', () => {
    mkdirSync(join(root, '.tmp'), { recursive: true })
    const directory = mkdtempSync(join(root, '.tmp', 'frontend-install-'))
    fixtures.push(directory)
    const frontend = join(directory, 'frontend')
    mkdirSync(frontend)
    mkdirSync(join(directory, 'scripts'))
    cpSync(join(root, 'scripts/task-state.mjs'), join(directory, 'scripts/task-state.mjs'))
    const tasks = Bun.YAML.parse(readFileSync(join(root, 'Taskfile.yaml'), 'utf8')).tasks
    writeFileSync(join(directory, 'Taskfile.yaml'), Bun.YAML.stringify({
      version: '3',
      tasks: { 'install-frontend-deps': tasks['install-frontend-deps'] },
    }))

    // Local archives keep the test independent of registries, compilers, and header downloads.
    // The native package's install hook records the build that only prepare-dmg-tools may run.
    for (const name of ['macos-alias', 'install-probe']) {
      const packageDir = join(directory, name)
      mkdirSync(packageDir)
      writeFileSync(join(packageDir, 'package.json'), JSON.stringify({
        name,
        version: '1.0.0',
        scripts: { install: 'bun install-hook.mjs' },
      }))
      writeFileSync(join(packageDir, 'binding.gyp'), '{}')
      writeFileSync(join(packageDir, 'install-hook.mjs'), `import { writeFileSync } from 'node:fs'; writeFileSync('install-ran', 'yes')`)
      const packed = spawnSync(process.execPath, ['pm', 'pack', '--ignore-scripts', '--filename', join(directory, `${name}.tgz`)], {
        cwd: packageDir,
        encoding: 'utf8',
      })
      expect(packed.status, packed.stdout + packed.stderr).toBe(0)
    }

    const manifest = JSON.parse(readFileSync(join(root, 'frontend/package.json'), 'utf8'))
    writeFileSync(join(frontend, 'package.json'), JSON.stringify({
      private: true,
      scripts: { preinstall: 'bun root-hook.mjs' },
      dependencies: { 'macos-alias': 'file:../macos-alias.tgz', 'install-probe': 'file:../install-probe.tgz' },
      trustedDependencies: ['macos-alias', 'install-probe'],
      ignoreScripts: manifest.ignoreScripts,
    }))
    writeFileSync(join(frontend, 'root-hook.mjs'), `import { writeFileSync } from 'node:fs'; writeFileSync('root-install-ran', 'yes')`)
    // Host npm and Bun policies must not change which fixture hooks this test exercises.
    const env = {
      ...process.env,
      HOME: directory,
      USERPROFILE: directory,
      XDG_CONFIG_HOME: join(directory, 'config'),
      NPM_CONFIG_USERCONFIG: join(directory, '.npmrc'),
      BUN_INSTALL_CACHE_DIR: join(directory, 'package-cache'),
    }
    delete env.NPM_CONFIG_IGNORE_SCRIPTS
    delete env.npm_config_ignore_scripts
    writeFileSync(join(directory, '.npmrc'), '')
    const lock = spawnSync(process.execPath, ['install', '--lockfile-only'], { cwd: frontend, env, encoding: 'utf8' })
    expect(lock.status, lock.stdout + lock.stderr).toBe(0)

    for (const args of [['install-frontend-deps'], ['--force', 'install-frontend-deps']]) {
      const installed = spawnSync('task', args, { cwd: directory, env, encoding: 'utf8' })
      expect(installed.status, installed.stdout + installed.stderr).toBe(0)
      const native = join(frontend, 'node_modules/macos-alias')
      expect(existsSync(join(native, 'binding.gyp'))).toBe(true)
      expect(existsSync(join(native, 'install-ran')), 'the frontend install must not compile the disk image module').toBe(false)
      const rootMarker = join(frontend, 'root-install-ran')
      const dependencyMarker = join(frontend, 'node_modules/install-probe/install-ran')
      expect(existsSync(rootMarker), installed.stdout + installed.stderr).toBe(true)
      expect(readFileSync(rootMarker, 'utf8')).toBe('yes')
      expect(readFileSync(dependencyMarker, 'utf8')).toBe('yes')
      rmSync(rootMarker)
      rmSync(dependencyMarker)
    }
  })
})
