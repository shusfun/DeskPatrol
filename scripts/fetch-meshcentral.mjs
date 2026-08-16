import { createHash } from "node:crypto";
import { createWriteStream, existsSync, mkdirSync, readFileSync, renameSync, rmSync } from "node:fs";
import { dirname, resolve } from "node:path";
import process from "node:process";
import { Readable } from "node:stream";
import { pipeline } from "node:stream/promises";

const version = "1.2.5";
const source = process.env.MESHCENTRAL_SOURCE_URL || "https://codeload.github.com/Ylianst/MeshCentral/tar.gz/refs/tags/1.2.5";
const expected = process.env.MESHCENTRAL_SHA256 || "";
const output = resolve(process.argv[2] || `third_party/cache/meshcentral-${version}.tar.gz`);

if (!source.startsWith("https://")) throw new Error("MESHCENTRAL_SOURCE_URL 必须使用 HTTPS");
if (!/^[a-f0-9]{64}$/.test(expected)) throw new Error("必须显式设置 MESHCENTRAL_SHA256 为 64 位小写 SHA-256");
mkdirSync(dirname(output), { recursive: true });
if (existsSync(output) && digest(readFileSync(output)) === expected) {
  process.stdout.write(`MeshCentral ${version} 缓存已命中\n`);
  process.exit(0);
}
const response = await fetch(source, { redirect: "follow", signal: AbortSignal.timeout(300_000) });
if (!response.ok || !response.body) throw new Error(`MeshCentral 下载失败：HTTP ${response.status}`);
const temporary = `${output}.tmp`;
try {
  await pipeline(Readable.fromWeb(response.body), createWriteStream(temporary, { mode: 0o600 }));
  const actual = digest(readFileSync(temporary));
  if (actual !== expected) throw new Error(`MeshCentral SHA-256 不匹配，expected=${expected} actual=${actual}`);
  renameSync(temporary, output);
} catch (error) {
  rmSync(temporary, { force: true });
  throw error;
}
process.stdout.write(`MeshCentral ${version} 已保存到 ${output}\n`);

function digest(value) { return createHash("sha256").update(value).digest("hex"); }
