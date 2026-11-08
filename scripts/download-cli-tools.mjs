// Install the pinned protoc, buf and Task CLI binaries for one runner
// platform into the cli-tools snapshot root (scripts/cache-policy.mjs owns
// that directory). CI restores the snapshot of these files on a hit and runs
// this script on a miss; `.github/actions/setup-cli-tools/action.yaml` keys
// the archive from the requested tool set, the pinned versions of exactly
// those tools, and this file.
//
// Each release publishes assets under fixed names, so the download needs no
// release listing and no GitHub API call: the URL is
// https://github.com/<owner>/<repo>/releases/download/v<version>/<asset>.
// The layouts below come from the releases themselves:
// - protoc ships a zip of bin/protoc and include/google/protobuf/*.proto.
// - Task ships a tar.gz (zip on Windows) holding the task binary.
// - buf ships one bare executable per platform.
import { spawnSync } from 'node:child_process'
import { chmodSync, copyFileSync, cpSync, existsSync, lstatSync, mkdirSync, readdirSync, rmSync } from 'node:fs'
import { basename, dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'
import { rootActivePath } from './cache-policy.mjs'
import { downloadReleaseAsset } from './download.mjs'

export const CLI_TOOLS = {
  protoc: {
    versionEnv: 'PROTOC_VERSION',
    repository: 'protocolbuffers/protobuf',
    executable: 'protoc',
    wellKnownIncludes: true,
    asset({ version, os, arch }) {
      if (os === 'Windows') {
        return arch === 'X64'
          ? `protoc-${version}-win64.zip`
          : null
      }
      const platform = { Linux: 'linux', macOS: 'osx' }[os]
      const cpu = { X64: 'x86_64', ARM64: 'aarch_64' }[arch]
      return platform && cpu
        ? `protoc-${version}-${platform}-${cpu}.zip`
        : null
    },
  },
  buf: {
    versionEnv: 'BUF_VERSION',
    repository: 'bufbuild/buf',
    executable: 'buf',
    wellKnownIncludes: false,
    asset({ os, arch }) {
      const platform = { Linux: 'Linux', macOS: 'Darwin', Windows: 'Windows' }[os]
      const cpu = {
        Linux: { X64: 'x86_64', ARM64: 'aarch64' },
        macOS: { X64: 'x86_64', ARM64: 'arm64' },
        Windows: { X64: 'x86_64', ARM64: 'arm64' },
      }[os]?.[arch]
      return platform && cpu
        ? `buf-${platform}-${cpu}${os === 'Windows'
          ? '.exe'
          : ''}`
        : null
    },
  },
  task: {
    versionEnv: 'TASK_VERSION',
    repository: 'go-task/task',
    executable: 'task',
    wellKnownIncludes: false,
    asset({ os, arch }) {
      const platform = { Linux: 'linux', macOS: 'darwin', Windows: 'windows' }[os]
      const cpu = { X64: 'amd64', ARM64: 'arm64' }[arch]
      return platform && cpu
        ? `task_${platform}_${cpu}.${os === 'Windows'
          ? 'zip'
          : 'tar.gz'}`
        : null
    },
  },
}

/** The sorted, de-duplicated tool names of a comma-separated or array request. */
export function canonicalToolNames(request) {
  const names = (Array.isArray(request)
    ? request
    : String(request).split(','))
    .map(name => String(name).trim())
    .filter(Boolean)
  if (names.length === 0)
    throw new Error('The requested tool list is empty.')
  for (const name of names) {
    if (!Object.hasOwn(CLI_TOOLS, name))
      throw new Error(`Unknown tool ${name}. Known tools: ${Object.keys(CLI_TOOLS).join(', ')}.`)
  }
  return [
    ...new Set(names),
  ].sort()
}

function toolVersion(name, env) {
  const value = env[CLI_TOOLS[name].versionEnv]
  if (typeof value !== 'string' || value === '')
    throw new Error(`The ${name} pin ${CLI_TOOLS[name].versionEnv} is missing from the environment. Load versions.env first.`)
  return value
}

/**
 * The canonical identity of one archive: exactly the requested tools and
 * their pins. The archive key hashes this, so a pin of one tool never
 * rotates the archive of a job that installs only another.
 */
export function toolIdentity(request, env = process.env) {
  const names = canonicalToolNames(request)
  return [
    `tools=${names.join(',')}`,
    ...names.map(name => `${name}=${toolVersion(name, env)}`),
  ].join('\n')
}

/**
 * The release asset and download URL of one tool for one runner platform,
 * or a refusal identifying the platform when the release publishes no asset
 * for it (protoc ships no Windows ARM64 build).
 */
export function toolRelease(name, { version, os, arch }) {
  if (!Object.hasOwn(CLI_TOOLS, name))
    throw new Error(`Unknown tool ${name}. Known tools: ${Object.keys(CLI_TOOLS).join(', ')}.`)
  const tool = CLI_TOOLS[name]
  const asset = tool.asset({ version, os, arch })
  if (asset === null)
    throw new Error(`The ${name} release publishes no asset for ${os} ${arch}.`)
  return {
    name,
    version,
    asset,
    url: `https://github.com/${tool.repository}/releases/download/v${version}/${asset}`,
    archiveKind: asset.endsWith('.tar.gz')
      ? 'tar.gz'
      : asset.endsWith('.zip')
        ? 'zip'
        : 'binary',
    binName: tool.executable + (os === 'Windows'
      ? '.exe'
      : ''),
    wellKnownIncludes: tool.wellKnownIncludes,
  }
}

function runOrFail(run, command, args) {
  const result = run(command, args, { encoding: 'utf8' })
  if (result.error)
    throw result.error
  if (result.status !== 0) {
    const output = String(result.stderr ?? result.stdout ?? '').trim()
    throw new Error(`${basename(command)} failed with exit code ${result.status}${output
      ? `: ${output}`
      : '.'}`)
  }
  return result
}

/** Extract one downloaded archive into an empty directory. */
export function extractArchive(archive, destination, { os, run = spawnSync } = {}) {
  mkdirSync(destination, { recursive: true })
  if (archive.endsWith('.tar.gz')) {
    runOrFail(run, 'tar', ['-xzf', archive, '-C', destination])
    return
  }
  if (archive.endsWith('.zip')) {
    if (os === 'Windows') {
      // The Windows zip ships no unzip; PowerShell is the platform extractor.
      runOrFail(run, 'powershell.exe', [
        '-NoProfile',
        '-NonInteractive',
        '-Command',
        `Expand-Archive -LiteralPath '${archive}' -DestinationPath '${destination}' -Force`,
      ])
    }
    else {
      runOrFail(run, 'unzip', ['-q', '-o', archive, '-d', destination])
    }
    return
  }
  throw new Error(`No extraction rule for ${basename(archive)}.`)
}

/** The single file whose basename is `binName` anywhere under an extracted tree. */
export function findExtractedExecutable(root, binName) {
  const matches = []
  const walk = (directory) => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const path = join(directory, entry.name)
      if (entry.isDirectory())
        walk(path)
      else if (entry.isFile() && entry.name === binName)
        matches.push(path)
    }
  }
  walk(root)
  if (matches.length === 1)
    return matches[0]
  const listing = readdirSync(root, { recursive: true }).map(String).sort().join(', ')
  if (matches.length === 0)
    throw new Error(`The extracted archive holds no ${binName}. Found: ${listing}.`)
  throw new Error(`The extracted archive holds ${matches.length} files named ${binName}: ${matches.join(', ')}.`)
}

/**
 * Move one extracted tool into the install layout: its executable into
 * <target>/bin, and for protoc the well-known .proto files into
 * <target>/include, beside the bin directory of the archive.
 */
export function installExtractedTool({ release, extracted, binDir, targetDir }) {
  const found = findExtractedExecutable(extracted, release.binName)
  mkdirSync(binDir, { recursive: true })
  const installed = join(binDir, release.binName)
  copyFileSync(found, installed)
  chmodSync(installed, 0o755)
  if (release.wellKnownIncludes) {
    const includes = resolve(dirname(found), '..', 'include')
    if (!existsSync(includes) || !lstatSync(includes).isDirectory())
      throw new Error(`The extracted ${release.name} archive has no include directory beside its bin directory.`)
    cpSync(includes, join(targetDir, 'include'), { recursive: true })
  }
  return installed
}

/**
 * Install the requested tools into the cli-tools snapshot root. Downloads
 * and extraction land in a staging directory under the workspace .tmp, which
 * the call removes on every outcome.
 */
export async function installCliTools({
  tools,
  env = process.env,
  workspace = process.cwd(),
  run = spawnSync,
  download = downloadReleaseAsset,
} = {}) {
  const names = canonicalToolNames(tools)
  const os = env.RUNNER_OS
  const arch = env.RUNNER_ARCH
  if (!os || !arch)
    throw new Error('The tool download needs the RUNNER_OS and RUNNER_ARCH environment variables.')
  const targetDir = rootActivePath('cli-tools', 'tools', env, workspace)
  const binDir = join(targetDir, 'bin')
  const staging = join(workspace, '.tmp', 'ci-cache', 'download-cli-tools')
  rmSync(staging, { recursive: true, force: true })
  mkdirSync(staging, { recursive: true })
  const installed = []
  try {
    for (const name of names) {
      const release = toolRelease(name, {
        version: toolVersion(name, env),
        os,
        arch,
      })
      console.log(`Installing ${name} ${release.version} from ${release.asset}.`)
      const archive = join(staging, release.asset)
      await download(release.url, archive)
      if (release.archiveKind === 'binary') {
        mkdirSync(binDir, { recursive: true })
        const binPath = join(binDir, release.binName)
        copyFileSync(archive, binPath)
        chmodSync(binPath, 0o755)
        installed.push({ name, binPath })
        continue
      }
      const extracted = join(staging, `extract-${name}`)
      extractArchive(archive, extracted, { os, run })
      installed.push({
        name,
        binPath: installExtractedTool({
          release,
          extracted,
          binDir,
          targetDir,
        }),
      })
    }
    // A wrong-architecture asset runs no version probe, so fail here rather
    // than in a later build step that only sees "protoc: not found".
    for (const { name, binPath } of installed) {
      const result = runOrFail(run, binPath, ['--version'])
      console.log(`${name} reports ${String(result.stdout ?? '').trim()}.`)
    }
  }
  finally {
    rmSync(staging, { recursive: true, force: true })
  }
  return installed.map(entry => entry.binPath)
}

function parseArgs(argv) {
  const args = { tools: 'protoc,buf,task', identity: false }
  for (let index = 0; index < argv.length; index++) {
    const arg = argv[index]
    if (arg === '--identity') {
      args.identity = true
      continue
    }
    if (arg === '--tools') {
      const value = argv[++index]
      if (value === undefined)
        throw new Error('--tools needs a comma-separated tool list.')
      args.tools = value
      continue
    }
    throw new Error(`Unknown argument ${arg}. Use --tools <list> and --identity.`)
  }
  return args
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try {
    const args = parseArgs(process.argv.slice(2))
    if (args.identity) {
      process.stdout.write(`${toolIdentity(args.tools, process.env)}\n`)
    }
    else {
      await installCliTools({
        tools: args.tools,
        env: process.env,
        workspace: process.cwd(),
      })
    }
  }
  catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
