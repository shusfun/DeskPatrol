import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import process from "node:process";
import { pathToFileURL } from "node:url";

export const meshCentralVersion = "1.2.5";
export const originalMultiplexSha256 = "b6d1c0c6b4f790556fa48413424cfae10e7486055966626810bc8d8ef5ad1d53";

const originalBlock = `            case 12: // SET DISPLAY, forward to agent
                if (viewer.viewOnly == false) { obj.sendToAgent(data); }
                break;`;

const patchedBlock = `            case 12: // SET DISPLAY, allow a validated physical display in view-only sessions
                if ((data.length != 6) || (obj.lastDisplayInfoData == null) || (obj.lastDisplayInfoData.length < 8)) break;
                var display = data.readUInt16BE(4);
                if (display == 65535) break;
                var displayCount = obj.lastDisplayInfoData.readUInt16BE(4);
                if (obj.lastDisplayInfoData.length < (8 + (displayCount * 2))) break;
                var displayFound = false;
                for (var displayIndex = 0; displayIndex < displayCount; displayIndex++) {
                    if (obj.lastDisplayInfoData.readUInt16BE(6 + (displayIndex * 2)) == display) { displayFound = true; break; }
                }
                if (displayFound) { obj.sendToAgent(data); }
                break;`;

export function patchMultiplexSource(source, actualSha256 = digest(source)) {
  if (actualSha256 !== originalMultiplexSha256) {
    throw new Error(`MeshCentral ${meshCentralVersion} meshdesktopmultiplex.js SHA-256 不匹配，expected=${originalMultiplexSha256} actual=${actualSha256}`);
  }
  const occurrences = source.split(originalBlock).length - 1;
  if (occurrences !== 1) {
    throw new Error(`MeshCentral ${meshCentralVersion} SetDisplay 补丁上下文数量不正确：${occurrences}`);
  }
  return source.replace(originalBlock, patchedBlock);
}

export function patchMeshCentral(root) {
  const packageJSON = JSON.parse(readFileSync(resolve(root, "package.json"), "utf8"));
  if (packageJSON.version !== meshCentralVersion) {
    throw new Error(`MeshCentral 版本不匹配，要求 ${meshCentralVersion}，实际 ${packageJSON.version || "未知"}`);
  }
  const target = resolve(root, "meshdesktopmultiplex.js");
  const source = readFileSync(target, "utf8");
  const patched = patchMultiplexSource(source);
  writeFileSync(target, patched, "utf8");
  process.stdout.write(`MeshCentral ${meshCentralVersion} 只读单屏补丁已应用\n`);
}

function digest(value) {
  return createHash("sha256").update(value).digest("hex");
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const root = process.argv[2];
  if (!root) throw new Error("必须提供 MeshCentral 根目录");
  patchMeshCentral(root);
}
