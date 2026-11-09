// Install the pinned protoc, buf and Task CLI binaries for one runner
// platform into the cli-tools snapshot root (scripts/cache-policy.mjs owns
// that directory). CI restores the snapshot of these files on a hit and runs
// this script on a miss; `.github/actions/setup-cli-tools/action.yaml` keys
// the archive from the requested tool set, the pinned versions of exactly
// those tools, and this file.
//
// Command line:
// - `--tools <list>` installs the listed tools.
// - `--tools <list> --identity` prints the sha256 of the archive identity of
//   the list.
// - `--bin-dir` prints the directory that holds the installed executables.
// - `--sync-digests` rewrites scripts/cli-tools.sha256 for the pins in the
//   environment (Task loads them from versions.env).
//
// Each release publishes assets under fixed names, so the download needs no
// release listing and no GitHub API call: the URL is
// https://github.com/<owner>/<repo>/releases/download/v<version>/<asset>.
// The layouts below come from the releases themselves:
// - protoc ships a zip of bin/protoc and include/google/protobuf/*.proto.
// - Task ships a tar.gz (zip on Windows) holding the task binary.
// - buf ships one bare executable per platform.
//
// The release assets are mutable, and the first job that misses the cache
// publishes what it downloaded to every later job. So each download must
// match the sha256 that scripts/cli-tools.sha256 pins for its asset before
// the installer extracts or installs it. A pin bump regenerates that table
// (`task sync-cli-tool-digests`; the Renovate post-upgrade step runs it), so
// the trust moves from each download to the reviewed pin change.
import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { chmodSync, copyFileSync, cpSync, existsSync, lstatSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { basename, dirname, join, relative, resolve } from 'node:path'
import process from 'node:process'
import { parseArgs } from 'node:util'
import { rootActivePath } from './cache-policy.mjs'
import { downloadFile } from './download.mjs'

/** The table of pinned asset digests, beside this script. */
export const DIGESTS_FILE = join(import.meta.dirname, 'cli-tools.sha256')

// Every runner platform that a release row can serve.
const RUNNER_PLATFORMS = [
  ['Linux', 'X64'],
  ['Linux', 'ARM64'],
  ['macOS', 'X64'],
  ['macOS', 'ARM64'],
  ['Windows', 'X64'],
  ['Windows', 'ARM64'],
]

/** The suffix of an executable file name on the runner platform. */
function exeSuffix(os) {
  return os === 'Windows'
    ? '.exe'
    : ''
}

/** The archive format of a release asset or a downloaded file, from its name. */
function archiveKindOf(name) {
  if (name.endsWith('.tar.gz'))
    return 'tar.gz'
  if (name.endsWith('.zip'))
    return 'zip'
  return 'binary'
}

const CLI_TOOLS = {
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
        ? `buf-${platform}-${cpu}${exeSuffix(os)}`
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

/** The versions.env names of the CLI tool pins. */
export function cliToolVersionEnvs() {
  return Object.values(CLI_TOOLS).map(tool => tool.versionEnv).sort()
}

/** The table entry of one tool, or a refusal that lists the known tools. */
function requireTool(name) {
  if (!Object.hasOwn(CLI_TOOLS, name))
    throw new Error(`Unknown tool ${name}. Known tools: ${Object.keys(CLI_TOOLS).join(', ')}.`)
  return CLI_TOOLS[name]
}

/** The sorted, de-duplicated tool names of a comma-separated request. */
export function canonicalToolNames(request) {
  const names = String(request)
    .split(',')
    .map(name => name.trim())
    .filter(Boolean)
  if (names.length === 0)
    throw new Error('The requested tool list is empty.')
  for (const name of names)
    requireTool(name)
  return [
    ...new Set(names),
  ].sort()
}

function toolVersion(name, env) {
  const { versionEnv } = requireTool(name)
  const value = env[versionEnv]
  if (typeof value !== 'string' || value === '')
    throw new Error(`The ${name} pin ${versionEnv} is missing from the environment. Load versions.env first.`)
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

/** The sha256 of the archive identity, which the archive key carries as one hash segment. */
export function toolIdentityHash(request, env = process.env) {
  return createHash('sha256').update(toolIdentity(request, env)).digest('hex')
}

/**
 * The release asset and download URL of one tool for one runner platform,
 * or a refusal identifying the platform when the release publishes no asset
 * for it (protoc ships no Windows ARM64 build).
 */
export function toolRelease(name, { version, os, arch }) {
  const tool = requireTool(name)
  const asset = tool.asset({ version, os, arch })
  if (asset === null)
    throw new Error(`The ${name} release publishes no asset for ${os} ${arch}.`)
  return {
    name,
    version,
    asset,
    url: `https://github.com/${tool.repository}/releases/download/v${version}/${asset}`,
    archiveKind: archiveKindOf(asset),
    binName: tool.executable + exeSuffix(os),
    wellKnownIncludes: tool.wellKnownIncludes,
  }
}

/**
 * Every release row of the pinned versions: each tool on each runner
 * platform for which its release publishes an asset.
 */
export function pinnedToolReleases(env = process.env) {
  const releases = []
  for (const name of Object.keys(CLI_TOOLS).sort()) {
    const version = toolVersion(name, env)
    for (const [os, arch] of RUNNER_PLATFORMS) {
      if (requireTool(name).asset({ version, os, arch }) !== null)
        releases.push(toolRelease(name, { version, os, arch }))
    }
  }
  return releases
}

/** The key of one release row in the digest table. */
function digestKey({ name, version, asset }) {
  return `${name} ${version} ${asset}`
}

const DIGEST_LINE = /^([0-9a-f]{64}) {2}(\S+) (\S+) (\S+)$/

/**
 * The pinned sha256 of each release row, keyed by `<tool> <version> <asset>`.
 * A line holds `<sha256>  <tool> <version> <asset>`; a line that starts with
 * `#` is a comment.
 */
export function parseToolDigests(text) {
  const digests = new Map()
  for (const [index, line] of text.split('\n').entries()) {
    if (line === '' || line.startsWith('#'))
      continue
    const match = DIGEST_LINE.exec(line)
    if (!match)
      throw new Error(`Line ${index + 1} of the CLI tool digest table is malformed: ${line}`)
    const key = `${match[2]} ${match[3]} ${match[4]}`
    if (digests.has(key))
      throw new Error(`The CLI tool digest table lists ${key} twice.`)
    digests.set(key, match[1])
  }
  return digests
}

/** The digest table text of the given release rows, sorted by key. */
export function formatToolDigests(digests) {
  const lines = [...digests]
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([key, digest]) => `${digest}  ${key}`)
  return [
    '# The sha256 of each pinned CLI tool release asset. scripts/download-cli-tools.mjs',
    '# refuses a download that differs. `task sync-cli-tool-digests` rewrites this file',
    '# for the pins in versions.env.',
    ...lines,
    '',
  ].join('\n')
}

function sha256OfFile(path) {
  return createHash('sha256').update(readFileSync(path)).digest('hex')
}

/**
 * Rewrite the digest table for the pinned versions. A row that the table
 * already holds keeps its digest, so only a changed pin downloads anything;
 * a row of an older version leaves the table. Each new row's asset is
 * downloaded through the retrying downloader and hashed.
 */
export async function syncToolDigests({
  env = process.env,
  file = DIGESTS_FILE,
  download = downloadFile,
  scratchRoot = join(dirname(file), '..', '.tmp'),
} = {}) {
  const existing = existsSync(file)
    ? parseToolDigests(readFileSync(file, 'utf8'))
    : new Map()
  const synced = new Map()
  mkdirSync(scratchRoot, { recursive: true })
  const scratch = mkdtempSync(join(scratchRoot, 'cli-tool-digests-'))
  try {
    for (const release of pinnedToolReleases(env)) {
      const key = digestKey(release)
      if (existing.has(key)) {
        synced.set(key, existing.get(key))
        continue
      }
      console.log(`Hashing ${release.name} ${release.version} ${release.asset}.`)
      const path = join(scratch, release.asset)
      await download(release.url, path)
      synced.set(key, sha256OfFile(path))
      rmSync(path, { force: true })
    }
  }
  finally {
    rmSync(scratch, { recursive: true, force: true })
  }
  writeFileSync(file, formatToolDigests(synced))
  return synced
}

/** Refuse a downloaded asset whose sha256 differs from its pinned digest. */
function verifyDownload(release, path, digests) {
  const expected = digests.get(digestKey(release))
  const actual = sha256OfFile(path)
  if (actual !== expected)
    throw new Error(`The ${release.name} asset ${release.asset} has sha256 ${actual}, but scripts/cli-tools.sha256 pins ${expected}.`)
}

/** The directory that holds the installed executables, which CI puts on the PATH. */
export function cliToolsBinDir(env = process.env, workspace = process.cwd()) {
  return join(rootActivePath('cli-tools', 'tools', env, workspace), 'bin')
}

function runOrFail(run, command, args, options = {}) {
  const result = run(command, args, { encoding: 'utf8', ...options })
  if (result.error)
    throw result.error
  if (result.status !== 0) {
    // spawnSync reports a null status when a signal ended the child. With an
    // encoding, an empty stream is '' rather than null, so `||` picks the
    // first stream that holds text.
    const ending = result.status === null
      ? `was stopped by ${result.signal}`
      : `failed with exit code ${result.status}`
    const output = result.stderr?.trim() || result.stdout?.trim() || ''
    throw new Error(`${basename(command)} ${ending}${output
      ? `: ${output}`
      : '.'}`)
  }
  return result
}

/** Extract one downloaded archive into an empty directory. */
export function extractArchive(archive, destination, { os, run = spawnSync } = {}) {
  mkdirSync(destination, { recursive: true })
  const kind = archiveKindOf(archive)
  if (kind === 'tar.gz') {
    runOrFail(run, 'tar', ['-xzf', archive, '-C', destination])
    return
  }
  if (kind === 'zip') {
    if (os === 'Windows') {
      // Windows itself has no unzip command; only Git for Windows adds one.
      // PowerShell's Expand-Archive is built in. The paths travel in the
      // environment, so a quote in a path cannot end the command's string.
      runOrFail(run, 'powershell.exe', [
        '-NoProfile',
        '-NonInteractive',
        '-Command',
        'Expand-Archive -LiteralPath $env:LEAPMUX_EXTRACT_ARCHIVE -DestinationPath $env:LEAPMUX_EXTRACT_DESTINATION -Force',
      ], {
        env: {
          ...process.env,
          LEAPMUX_EXTRACT_ARCHIVE: archive,
          LEAPMUX_EXTRACT_DESTINATION: destination,
        },
      })
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
  const entries = []
  const matches = []
  const walk = (directory) => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const path = join(directory, entry.name)
      entries.push(relative(root, path))
      if (entry.isDirectory())
        walk(path)
      else if (entry.isFile() && entry.name === binName)
        matches.push(path)
    }
  }
  walk(root)
  if (matches.length === 1)
    return matches[0]
  if (matches.length === 0)
    throw new Error(`The extracted archive holds no ${binName}. Found: ${entries.sort().join(', ')}.`)
  throw new Error(`The extracted archive holds ${matches.length} files named ${binName}: ${matches.join(', ')}.`)
}

/** Copy one executable into the bin directory of the install layout. */
function installExecutable(source, targetDir, binName) {
  const binDir = join(targetDir, 'bin')
  mkdirSync(binDir, { recursive: true })
  const installed = join(binDir, binName)
  copyFileSync(source, installed)
  chmodSync(installed, 0o755)
  return installed
}

/**
 * Copy one extracted tool into the install layout: its executable into
 * <target>/bin, and for protoc the well-known .proto files into
 * <target>/include, beside the bin directory of the archive.
 */
export function installExtractedTool({ release, extracted, targetDir }) {
  const found = findExtractedExecutable(extracted, release.binName)
  const installed = installExecutable(found, targetDir, release.binName)
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
  download = downloadFile,
  digests = parseToolDigests(readFileSync(DIGESTS_FILE, 'utf8')),
} = {}) {
  const names = canonicalToolNames(tools)
  const os = env.RUNNER_OS
  const arch = env.RUNNER_ARCH
  if (!os || !arch)
    throw new Error('The tool download needs the RUNNER_OS and RUNNER_ARCH environment variables.')
  const releases = names.map(name => toolRelease(name, {
    version: toolVersion(name, env),
    os,
    arch,
  }))
  // Refuse a release without a pinned digest before anything downloads.
  for (const release of releases) {
    if (!digests.has(digestKey(release)))
      throw new Error(`scripts/cli-tools.sha256 pins no sha256 for ${release.name} ${release.version} ${release.asset}. Run task sync-cli-tool-digests.`)
  }
  const targetDir = rootActivePath('cli-tools', 'tools', env, workspace)
  const staging = join(workspace, '.tmp', 'ci-cache', 'download-cli-tools')
  rmSync(staging, { recursive: true, force: true })
  mkdirSync(staging, { recursive: true })
  const installed = []
  try {
    for (const release of releases) {
      const { name } = release
      console.log(`Installing ${name} ${release.version} from ${release.asset}.`)
      const archive = join(staging, release.asset)
      await download(release.url, archive)
      verifyDownload(release, archive, digests)
      if (release.archiveKind === 'binary') {
        installed.push({
          name,
          binPath: installExecutable(archive, targetDir, release.binName),
        })
        continue
      }
      const extracted = join(staging, `extract-${name}`)
      extractArchive(archive, extracted, { os, run })
      installed.push({
        name,
        binPath: installExtractedTool({
          release,
          extracted,
          targetDir,
        }),
      })
    }
    // Run each installed binary with --version, so that a wrong-architecture
    // or corrupt asset fails here, and not in a later build step with
    // "Exec format error".
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

const USAGE = 'Use --tools <list> to install, --tools <list> --identity to print the archive identity hash, --bin-dir to print the install directory, or --sync-digests to rewrite the digest table.'

/** The mode and the tool list of one command line. */
export function parseCommandLine(argv) {
  let values
  try {
    ({ values } = parseArgs({
      args: argv,
      options: {
        'tools': { type: 'string' },
        'identity': { type: 'boolean', default: false },
        'bin-dir': { type: 'boolean', default: false },
        'sync-digests': { type: 'boolean', default: false },
      },
      strict: true,
      allowPositionals: false,
    }))
  }
  catch (error) {
    throw new Error(`${error.message} ${USAGE}`, { cause: error })
  }
  for (const mode of ['bin-dir', 'sync-digests']) {
    if (!values[mode])
      continue
    if (Object.keys(values).some(option => option !== mode && values[option] !== undefined && values[option] !== false))
      throw new Error(`--${mode} takes no other option. ${USAGE}`)
    return { mode }
  }
  if (values.tools === undefined)
    throw new Error(`--tools needs a comma-separated tool list. ${USAGE}`)
  return {
    mode: values.identity
      ? 'identity'
      : 'install',
    tools: values.tools,
  }
}

if (import.meta.main) {
  try {
    const { mode, tools } = parseCommandLine(process.argv.slice(2))
    if (mode === 'bin-dir') {
      process.stdout.write(`${cliToolsBinDir(process.env, process.cwd())}\n`)
    }
    else if (mode === 'sync-digests') {
      await syncToolDigests({ env: process.env })
    }
    else if (mode === 'identity') {
      process.stdout.write(`${toolIdentityHash(tools, process.env)}\n`)
    }
    else {
      await installCliTools({
        tools,
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
