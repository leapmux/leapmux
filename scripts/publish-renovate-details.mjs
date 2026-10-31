#!/usr/bin/env node
import { Buffer } from 'node:buffer'
import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'
import { renovateModule } from './renovate-engine.mjs'
import { COMMENT_MARKER, partitionLegacyBody, renderDetailComments, validateDetailRecord } from './renovate-pr-details.mjs'

const repositoryPath = '/repos/leapmux/leapmux'
const botLogin = 'leapmux-renovate[bot]'

export async function githubRequest(method, path, body) {
  const readable = /^\/repos\/leapmux\/leapmux\/(?:pulls(?:\?[^\r\n#]*)?|pulls\/[1-9]\d*|issues\/[1-9]\d*\/comments(?:\?[^\r\n#]*)?|issues\/comments\/[1-9]\d*)$/.test(path)
  const writable = method === 'POST'
    ? /^\/repos\/leapmux\/leapmux\/issues\/[1-9]\d*\/comments$/.test(path)
    : /^\/repos\/leapmux\/leapmux\/(?:pulls|issues\/comments)\/[1-9]\d*$/.test(path)
  if (!['GET', 'POST', 'PATCH'].includes(method) || (method === 'GET'
    ? !readable || body !== undefined
    : !writable || typeof body?.body !== 'string' || Object.keys(body).length !== 1)) {
    throw new Error('PR details can only use the leapmux/leapmux GitHub API.')
  }
  const args = [
    'api',
    '--hostname',
    'github.com',
    '--method',
    method,
    '-H',
    'X-GitHub-Api-Version: 2022-11-28',
    '-H',
    'Cache-Control: no-cache',
    path,
  ]
  if (body !== undefined)
    args.push('--input', '-')
  const result = spawnSync('gh', args, {
    encoding: 'utf8',
    input: body === undefined
      ? undefined
      : JSON.stringify(body),
    timeout: 30000,
    maxBuffer: 16 * 1024 * 1024,
  })
  if (result.error || result.status !== 0)
    throw new Error(`GitHub ${method} ${path} failed: ${result.error?.message ?? result.stderr}`)
  return result.stdout.trim()
    ? JSON.parse(result.stdout)
    : null
}

async function listAll(api, path) {
  const values = []
  for (let page = 1; page <= 100; page++) {
    const batch = await api('GET', `${path}${path.includes('?')
      ? '&'
      : '?'}per_page=100&page=${page}`)
    if (!Array.isArray(batch))
      throw new TypeError('GitHub returned an invalid PR detail collection.')
    values.push(...batch)
    if (batch.length < 100)
      return values
  }
  throw new Error('PR detail pagination exceeds the supported collection size.')
}

function requireBotPullRequest(pr, branchName) {
  if (!Number.isSafeInteger(pr?.number) || pr.number <= 0 || pr.user?.login !== botLogin || pr.user?.type !== 'Bot'
    || pr.base?.ref !== 'main' || pr.base?.repo?.full_name !== 'leapmux/leapmux'
    || pr.head?.repo?.full_name !== 'leapmux/leapmux' || pr.head.ref !== branchName) {
    throw new Error('PR details require the expected Renovate PR targeting leapmux/leapmux main.')
  }
}

export async function updateDetailComments(pr, bodies, { api = githubRequest, actorLogin = botLogin } = {}) {
  const comments = await listAll(api, `${repositoryPath}/issues/${pr}/comments`)
  // Update only the parts that the bot wrote. A human comment never changes.
  const owned = comments.filter(comment => comment.user?.login === actorLogin && typeof comment.body === 'string' && comment.body.startsWith(COMMENT_MARKER))
  const parts = new Map()
  for (const comment of owned) {
    const part = /^<!-- leapmux-renovate-details:v1 part=([1-9]\d*) -->/.exec(comment.body)?.[1]
    if (!part || !Number.isSafeInteger(comment.id) || comment.id <= 0 || Number(part) > 1000 || parts.has(Number(part)))
      throw new Error('Managed PR detail comments contain an invalid or duplicate part.')
    parts.set(Number(part), comment)
  }
  const urls = []
  const count = Math.max(bodies.length, 0, ...parts.keys())
  for (let index = 0; index < count; index++) {
    const marker = `${COMMENT_MARKER}${index + 1} -->`
    const existing = parts.get(index + 1)
    const body = bodies[index] ?? `${marker}\nThe current proposal no longer needs this detail part.`
    if (!existing && index >= bodies.length)
      continue
    let result = existing
    if (existing?.body !== body) {
      result = existing
        ? await api('PATCH', `${repositoryPath}/issues/comments/${existing.id}`, { body })
        : await api('POST', `${repositoryPath}/issues/${pr}/comments`, { body })
    }
    if (!Number.isSafeInteger(result?.id) || result.id <= 0 || result.user?.login !== actorLogin || result.body !== body)
      throw new Error('GitHub did not confirm the expected PR detail comment and author.')
    const stored = await api('GET', `${repositoryPath}/issues/comments/${result.id}`)
    if (stored.body !== body || stored.user?.login !== actorLogin)
      throw new Error('The stored PR detail comment differs from the requested content.')
    urls.push(stored.html_url)
  }
  return urls
}

function restoreNumericEntities(text) {
  // Renovate treats the # in a decimal HTML entity as an issue reference.
  return text.replace(/&#&#8203;(?=\d+;)/g, '&#')
}

export async function publishRecord(record, pr, template, { api = githubRequest, actorLogin = botLogin, sanitize = text => text } = {}) {
  validateDetailRecord(record)
  requireBotPullRequest(pr, record.branchName)
  const partition = partitionLegacyBody(pr.body, template)
  // The native changelog renderer already suppresses mentions. A second pass corrupts its HTML entities.
  const clean = {
    ...record,
    warnings: restoreNumericEntities(sanitize(record.warnings)),
    notes: restoreNumericEntities(sanitize(record.notes)),
    changelogs: restoreNumericEntities(record.changelogs),
  }
  // Confirm all comments before removing any details from an existing description.
  const comments = await updateDetailComments(pr.number, renderDetailComments(clean), { api, actorLogin })
  if (pr.body.trim() !== partition.body.trim()) {
    await api('PATCH', `${repositoryPath}/pulls/${pr.number}`, { body: partition.body })
    const stored = await api('GET', `${repositoryPath}/pulls/${pr.number}`)
    if (stored.body.trim() !== partition.body.trim())
      throw new Error('The stored PR description differs from the three core sections.')
  }
  return { pr: pr.number, comments }
}

export async function publishCapturedDetails(records, template, options = {}) {
  if (!Array.isArray(records) || records.length > 10000)
    throw new Error('PR detail publication requires at most 10000 captured proposals.')
  const api = options.api ?? githubRequest
  const latest = new Map(records.map(record => [validateDetailRecord(record).branchName, record]))
  const results = []
  for (const [branch, record] of latest) {
    const query = new URLSearchParams({ state: 'open', head: `leapmux:${branch}`, base: 'main' })
    const prs = await listAll(api, `${repositoryPath}/pulls?${query}`)
    if (prs.length === 0) {
      results.push({ branch, skipped: 'No open PR remains for the captured proposal.' })
      continue
    }
    if (prs.length !== 1)
      throw new Error('More than one open PR matches a captured Renovate branch.')
    results.push(await publishRecord(record, prs[0], template, { ...options, api }))
  }
  return results
}

export async function migratePullRequest(pr, template, options = {}) {
  requireBotPullRequest(pr, pr?.head?.ref)
  const { details } = partitionLegacyBody(pr.body, template)
  if (!details.trim())
    return { pr: pr.number, skipped: 'The description already contains only the core sections.' }
  return publishRecord({
    version: 1,
    repository: 'leapmux/leapmux',
    branchName: pr.head.ref,
    warnings: '',
    notes: details,
    changelogs: '',
  }, pr, template, options)
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const root = resolve(import.meta.dirname, '..')
    const template = JSON.parse(readFileSync(resolve(root, 'renovate.json'), 'utf8')).prBodyTemplate
    const args = process.argv.slice(2)
    const dryRun = args.length === 1 && args[0] === '--dry-run'
    const migrate = args.length === 4 && args[0] === '--migrate-pr' && /^[1-9]\d*$/.test(args[1]) && args[2] === '--actor-login' && /^[\w[\]-]+$/.test(args[3])
    if (args.length && !dryRun && !migrate)
      throw new Error('Use --dry-run or --migrate-pr NUMBER --actor-login LOGIN.')
    const { sanitizeMarkdown } = await renovateModule('util/markdown.js')
    if (migrate) {
      const pr = await githubRequest('GET', `${repositoryPath}/pulls/${args[1]}`)
      console.log(JSON.stringify(await migratePullRequest(pr, template, { actorLogin: args[3], sanitize: sanitizeMarkdown })))
    }
    else {
      let text = ''
      try {
        text = readFileSync(resolve(root, '.tmp/renovate-pr-details.jsonl'), 'utf8')
      }
      catch (error) {
        if (error.code !== 'ENOENT')
          throw error
      }
      const records = text.split('\n').filter(Boolean).map(line => validateDetailRecord(JSON.parse(line)))
      if (dryRun) {
        console.log(JSON.stringify(records.map(record => ({
          branch: record.branchName,
          commentBytes: renderDetailComments(record).map(body => Buffer.byteLength(body)),
        }))))
      }
      else {
        if (process.env.GITHUB_ACTIONS !== 'true' || process.env.GITHUB_REPOSITORY !== 'leapmux/leapmux' || process.env.GITHUB_REF !== 'refs/heads/main')
          throw new Error('Automatic PR detail publication requires upstream main in GitHub Actions.')
        console.log(JSON.stringify(await publishCapturedDetails(records, template, { sanitize: sanitizeMarkdown })))
      }
    }
  }
  catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
