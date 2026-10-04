import { chmodSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'
import process from 'node:process'
import { isObject } from '../../../src/lib/jsonPick'

export interface DiracStdioMcpServer {
  name: string
  command: string
  args: string[]
  env: { name: string, value: string }[]
}

/** Change only the configured server list of a native session creation request. */
export function rewriteDiracMcpRequest(line: string, servers: readonly DiracStdioMcpServer[]) {
  let frame: unknown
  try {
    frame = JSON.parse(line)
  }
  catch {
    return { line }
  }
  if (typeof frame !== 'object' || frame === null || Array.isArray(frame)
    || !('method' in frame) || frame.method !== 'session/new'
    || !('id' in frame) || (typeof frame.id !== 'number' && typeof frame.id !== 'string')
    || (typeof frame.id === 'number' && !Number.isFinite(frame.id))
    || !('params' in frame) || typeof frame.params !== 'object' || frame.params === null || Array.isArray(frame.params)) {
    return { line }
  }
  const request = {
    ...frame,
    params: { ...frame.params, mcpServers: servers.map(server => ({ ...server, args: [...server.args], env: server.env.map(entry => ({ ...entry })) })) },
  }
  return { line: JSON.stringify(request), request }
}

function validateServers(servers: readonly DiracStdioMcpServer[]): void {
  const names = new Set<string>()
  for (const server of servers) {
    if (!isObject(server) || typeof server.name !== 'string' || !server.name.trim() || names.has(server.name)
      || typeof server.command !== 'string' || !isAbsolute(server.command)
      || !Array.isArray(server.args) || !server.args.every(argument => typeof argument === 'string')
      || !Array.isArray(server.env) || !server.env.every(entry => isObject(entry) && typeof entry.name === 'string' && entry.name !== '' && typeof entry.value === 'string')) {
      throw new Error('The private Dirac MCP server requires a unique name and an absolute stdio command.')
    }
    names.add(server.name)
  }
}

export interface DiracMcpWrapper {
  directory: string
  scriptPath: string
  receiptLog: string
}

/** Forward the actual native protocol while supplying its standard MCP configuration. */
export function writeDiracMcpWrapper(options: {
  directory: string
  nodeExecutable: string
  executable: string
  args?: readonly string[]
  servers: readonly DiracStdioMcpServer[]
}): DiracMcpWrapper {
  if (!isAbsolute(options.directory) || !isAbsolute(options.nodeExecutable) || !isAbsolute(options.executable))
    throw new Error('The private Dirac MCP wrapper requires absolute native paths.')
  validateServers(options.servers)
  mkdirSync(options.directory, { recursive: true })
  const receiptLog = join(options.directory, 'native-mcp-session.jsonl')
  const scriptPath = join(options.directory, 'dirac-mcp.cjs')
  const configuration = { executable: options.executable, args: [...options.args ?? []], servers: options.servers, receiptLog }
  const source = `#!${options.nodeExecutable}
const {spawn}=require('node:child_process');
const {appendFileSync}=require('node:fs');
const {createInterface}=require('node:readline');
const config=${JSON.stringify(configuration)};
const rewrite=${rewriteDiracMcpRequest.toString()};
const argv=process.argv.slice(2);
const acp=argv.includes('--acp');
const child=spawn(config.executable,[...config.args,...argv],{stdio:acp?['pipe','pipe','pipe']:'inherit',env:process.env});
let input;let output;let spawnFailed=false;
const requests=new Set();
const save=(direction,frame)=>appendFileSync(config.receiptLog,JSON.stringify({direction,pid:process.pid,nativePid:child.pid,frame})+'\\n',{mode:0o600});
if(acp){
  child.stdout.pipe(process.stdout);
  child.stderr.pipe(process.stderr);
  output=createInterface({input:child.stdout});
  output.on('line',line=>{
    let frame;try{frame=JSON.parse(line)}catch{return}
    if(frame&&typeof frame==='object'&&!Array.isArray(frame)&&!('method' in frame)&&requests.has(JSON.stringify(frame.id)))save('reply',frame);
  });
  input=createInterface({input:process.stdin});
  input.on('line',line=>{
    const changed=rewrite(line,config.servers);
    if(changed.request){requests.add(JSON.stringify(changed.request.id));save('request',changed.request)}
    child.stdin.write(changed.line+'\\n');
  });
  input.once('close',()=>child.stdin.end());
  child.stdin.on('error',error=>{if(child.exitCode===null&&child.signalCode===null)process.stderr.write('The native Dirac input failed: '+error.message+'\\n')});
}
child.once('error',error=>{
  spawnFailed=child.pid===undefined;
  process.stderr.write('The native Dirac MCP wrapper failed: '+error.message+'\\n');
  process.exitCode=127;input?.close();output?.close();process.stdin.pause();
});
child.once('close',(code,signal)=>{
  input?.close();output?.close();process.stdin.pause();
  if(spawnFailed){process.exitCode=127;return}
  if(signal){process.removeAllListeners(signal);process.kill(process.pid,signal)}else process.exitCode=code??1;
});
for(const signal of ['SIGTERM','SIGINT','SIGHUP'])process.on(signal,()=>child.kill(signal));
`
  writeFileSync(scriptPath, source, { mode: 0o700 })
  chmodSync(scriptPath, 0o700)
  if (process.platform === 'win32') {
    writeFileSync(join(options.directory, 'dirac.cmd'), `@"${options.nodeExecutable}" "${scriptPath}" %*\r\n`)
  }
  else {
    const executable = join(options.directory, 'dirac')
    writeFileSync(executable, source, { mode: 0o700 })
    chmodSync(executable, 0o700)
  }
  return { directory: options.directory, scriptPath, receiptLog }
}

export interface DiracMcpSessionObservation {
  request: Record<string, unknown>
  reply: Record<string, unknown>
  sessionId: string
}

/** Require a paired actual native session reply for the configured request. */
export function readDiracMcpSessionObservation(path: string): DiracMcpSessionObservation {
  const records: unknown[] = readFileSync(path, 'utf8').split('\n').filter(Boolean).map(line => JSON.parse(line))
  const requests = records.filter(record => isObject(record) && record.direction === 'request')
  if (requests.length !== 1 || !isObject(requests[0]) || !isObject(requests[0].frame))
    throw new Error('The private Dirac MCP proof requires one actual session creation request.')
  const request = requests[0].frame
  if (request.jsonrpc !== '2.0' || request.method !== 'session/new'
    || (typeof request.id !== 'string' && (typeof request.id !== 'number' || !Number.isFinite(request.id)))
    || !isObject(request.params) || !Array.isArray(request.params.mcpServers)) {
    throw new Error('The native Dirac MCP receipt contains an invalid session creation request.')
  }
  const replies = records.filter(record => isObject(record) && record.direction === 'reply' && isObject(record.frame) && record.frame.id === request.id)
  const reply = isObject(replies[0]) && isObject(replies[0].frame) ? replies[0].frame : undefined
  if (replies.length !== 1 || !reply || reply.error || !isObject(reply.result) || typeof reply.result.sessionId !== 'string' || reply.result.sessionId === '')
    throw new Error('The configured native Dirac request has no unique successful session reply.')
  return { request, reply, sessionId: reply.result.sessionId }
}
