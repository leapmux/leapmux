import { writeFileSync } from 'node:fs'
import { join } from 'node:path'

const runtimeScript = `
import { renameSync, writeFileSync } from 'node:fs';
export function writeMcpReceiptValue(receiptLog, value) {
  if (!receiptLog) return;
  const sibling = receiptLog + '.writing-' + process.pid;
  writeFileSync(sibling, JSON.stringify(value));
  renameSync(sibling, receiptLog);
}
export function createMcpServerReceipt(receiptLog = null) {
  const receipt = { initializeCapabilities: null, toolCatalogs: [], elicitationRequests: [], elicitationReplies: [], toolResults: [] };
  const save = () => writeMcpReceiptValue(receiptLog, receipt);
  save();
  return {
    initialized(capabilities) { receipt.initializeCapabilities = capabilities; save(); },
    listed(id, tools) { receipt.toolCatalogs.push({ id, tools: tools.map(({ name, inputSchema }) => ({ name, inputSchema })) }); save(); },
    requested(id, toolRequestId, params) { receipt.elicitationRequests.push({ id, toolRequestId, params }); save(); },
    replied(reply) { receipt.elicitationReplies.push(reply); save(); },
    completed(id, tool, text, isError = false) { receipt.toolResults.push({ id, tool, text, isError }); save(); },
  };
}
`

/** Write the shared receipt runtime beside each private native MCP server. */
export function writeMcpReceiptRuntime(directory: string): string {
  const path = join(directory, 'mcp-receipt-runtime.mjs')
  writeFileSync(path, runtimeScript)
  return path
}
