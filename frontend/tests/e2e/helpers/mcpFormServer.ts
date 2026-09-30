import { writeFileSync } from 'node:fs'
import { join } from 'node:path'

interface McpFormServerOptions {
  responseLog?: string
  expectedEchoArguments?: { query: string, limit: number, tail: string }
}

function serverScript(options: McpFormServerOptions): string {
  return `
import { createInterface } from 'node:readline';
import { writeFileSync } from 'node:fs';
const responseLog = ${JSON.stringify(options.responseLog ?? null)};
const expectedEchoArguments = ${JSON.stringify(options.expectedEchoArguments ?? null)};
let toolRequest;
const send = value => process.stdout.write(JSON.stringify(value) + '\\n');
for await (const line of createInterface({ input: process.stdin })) {
  const request = JSON.parse(line);
  if (request.id === undefined) continue;
  if (request.id === 'probe-form' && !request.method) {
    const reply = request.result;
    if (responseLog) writeFileSync(responseLog, JSON.stringify(request));
    const valid = reply?.action === 'accept' && reply.content?.count === 0 && reply.content?.enabled === false && reply.content?.color === 'b';
    const outcome = valid ? 'FORM_ROUND_TRIP_OK' : reply?.action === 'decline' ? 'FORM_ROUND_TRIP_DECLINED' : 'FORM_ROUND_TRIP_FAILED';
    send({jsonrpc:'2.0',id:toolRequest,result:{content:[{type:'text',text:outcome}]}});
    continue;
  }
  let result;
  switch (request.method) {
    case 'initialize':
      result = {protocolVersion:request.params.protocolVersion,capabilities:{tools:{}},serverInfo:{name:'form_probe',version:'1'}};
      break;
    case 'tools/list':
      result = {tools:[{name:'ask',description:'Request the disposable probe form. Call once with no arguments.',inputSchema:{type:'object',properties:{}}},{name:'echo',description:'Echo approved arguments.',inputSchema:{type:'object',properties:{query:{type:'string'},limit:{type:'integer'},tail:{type:'string'}},required:['query','limit','tail']}}]};
      break;
    case 'tools/call':
      if (request.params.name === 'echo') {
        const args = request.params.arguments;
        const valid = expectedEchoArguments !== null && args !== null && typeof args === 'object' && !Array.isArray(args)
          && Object.keys(args).length === 3 && args.query === expectedEchoArguments.query
          && args.limit === expectedEchoArguments.limit && args.tail === expectedEchoArguments.tail;
        result = {content:[{type:'text',text:valid ? 'PERMISSION_ACCEPTED' : 'PERMISSION_ARGUMENTS_FAILED'}]};
        break;
      }
      toolRequest = request.id;
      send({jsonrpc:'2.0',id:'probe-form',method:'elicitation/create',params:{mode:'form',message:'Choose the probe settings.',requestedSchema:{type:'object',required:['count','enabled','color'],properties:{count:{type:'integer',title:'Count',minimum:0,maximum:3},enabled:{type:'boolean',title:'Enabled'},color:{type:'string',title:'Color',oneOf:[{const:'b',title:'Blue'},{const:'r',title:'Red'}]}}}}});
      continue;
    default:
      send({jsonrpc:'2.0',id:request.id,error:{code:-32601,message:'Method not supported'}});
      continue;
  }
  send({jsonrpc:'2.0',id:request.id,result});
}
`
}

/** Write a disposable MCP server that asks for the probe form. */
export function writeMcpFormServer(directory: string, filename: string, options: McpFormServerOptions = {}): string {
  const path = join(directory, filename)
  writeFileSync(path, serverScript(options))
  return path
}
