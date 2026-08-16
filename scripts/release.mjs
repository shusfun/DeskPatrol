import { createHash } from "node:crypto";
import { cpSync, existsSync, mkdirSync, readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import path from "node:path";
import process from "node:process";

const [action] = process.argv.slice(2);
const version = option("--version");
const repository = process.env.GITHUB_REPOSITORY || "";
if (!/^[^/]+\/[^/]+$/.test(repository)) throw new Error("GITHUB_REPOSITORY 必须使用 owner/repository 格式");
if (!/^\d+\.\d+\.\d+$/.test(version)) throw new Error("版本必须使用 x.y.z 格式");
const root = process.cwd();
const releaseDir = path.join(root, "dist", "releases", version);

if (action === "prepare") prepare();
else if (action === "verify") verify();
else throw new Error("只支持 prepare 或 verify");

function prepare() {
  const required = [
    path.join(root, "dist", "linux", "amd64", `deskpatrol-linux-amd64-${version}.tar.gz`),
    path.join(root, "dist", "linux", "arm64", `deskpatrol-linux-arm64-${version}.tar.gz`),
    path.join(root, "dist", "windows", "amd64", `DeskPatrol-${version}-windows-amd64.exe`),
    path.join(root, "dist", "windows", "arm64", `DeskPatrol-${version}-windows-arm64.exe`),
  ];
  for (const file of required) if (!existsSync(file)) throw new Error(`Release 必需文件不存在：${path.relative(root, file)}`);
  mkdirSync(releaseDir, { recursive: true });
  for (const file of required) cpSync(file, path.join(releaseDir, path.basename(file)));
  cpSync(path.join(root, "scripts", "install.sh"), path.join(releaseDir, "install.sh"));
  const licenseResult = spawnSync(process.execPath, [path.join(root, "scripts", "generate-license-manifest.mjs"), path.join(releaseDir, "THIRD_PARTY_LICENSES.json")], { cwd: root, stdio: "inherit" });
  if (licenseResult.error) throw licenseResult.error;
  if (licenseResult.status !== 0) throw new Error(`许可证清单生成失败，exit=${licenseResult.status}`);
  const metadata = new Set(["manifest.json", "SHA256SUMS"]);
  const files = walk(releaseDir).filter((file) => !metadata.has(path.basename(file)));
  const manifest = { schemaVersion: 1, product: "DeskPatrol", version, repository, meshCentralVersion: "1.2.5", generatedAt: new Date().toISOString(), artifacts: files.map((file) => describeArtifact(file)) };
  writeFileSync(path.join(releaseDir, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`, { mode: 0o644 });
  writeFileSync(path.join(releaseDir, "SHA256SUMS"), `${manifest.artifacts.map((artifact) => `${artifact.sha256}  ${artifact.filename}`).join("\n")}\n`, { mode: 0o644 });
  process.stdout.write(`Release manifest 已生成：${path.relative(root, releaseDir)}\n`);
}

function verify() {
  const manifestPath = path.join(releaseDir, "manifest.json");
  if (!existsSync(manifestPath)) throw new Error("Release manifest 不存在");
  const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
  if (manifest.version !== version || manifest.repository !== repository || manifest.meshCentralVersion !== "1.2.5") throw new Error("Release manifest 元数据不匹配");
  for (const artifact of manifest.artifacts) {
    const file = path.resolve(releaseDir, artifact.filename);
    if (!file.startsWith(`${releaseDir}${path.sep}`) || !existsSync(file)) throw new Error(`Release 文件不存在：${artifact.filename}`);
    if (statSync(file).size !== artifact.size || sha256(file) !== artifact.sha256) throw new Error(`Release 文件校验失败：${artifact.filename}`);
  }
  process.stdout.write(`Release ${version} 校验通过，共 ${manifest.artifacts.length} 个文件\n`);
}

function walk(directory) { return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => { const value = path.join(directory, entry.name); return entry.isDirectory() ? walk(value) : [value]; }); }
function sha256(file) { return createHash("sha256").update(readFileSync(file)).digest("hex"); }
function describeArtifact(file) {
  const filename = path.basename(file);
  const windows = filename.match(/windows-(amd64|arm64)\.exe$/i);
  const linux = filename.match(/linux-(amd64|arm64)-\d+\.\d+\.\d+\.tar\.gz$/i);
  return { filename, platform: windows ? "windows" : linux ? "linux" : "metadata", architecture: windows?.[1] || linux?.[1] || "all", size: statSync(file).size, sha256: sha256(file) };
}
function option(key) { const index = process.argv.indexOf(key); return index >= 0 ? process.argv[index + 1] || "" : ""; }
