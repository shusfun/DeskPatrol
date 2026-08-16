import { createHash } from "node:crypto";
import { existsSync, readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { spawnSync } from "node:child_process";

const output = path.resolve(process.argv[2] || "dist/THIRD_PARTY_LICENSES.json");
const frontend = commandJSON("pnpm", ["licenses", "list", "--json", "--prod"]);
const frontendPackages = Object.entries(frontend).flatMap(([license, items]) => items.map((item) => ({
  ecosystem: "npm", name: item.name, versions: item.versions, license,
  homepage: item.homepage || "", author: typeof item.author === "string" ? item.author : "",
}))).sort(compare);

command("go", ["mod", "download", "all"]);
const goTemplate = "{{with .Module}}{{if not .Main}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}{{end}}";
const goTargets = ["./cmd/server", "./cmd/client", "./cmd/client-helper"];
const moduleLines = new Set();
for (const target of [
  { GOOS: "linux", GOARCH: "amd64", CGO_ENABLED: "0" },
  { GOOS: "linux", GOARCH: "arm64", CGO_ENABLED: "0" },
  { GOOS: "windows", GOARCH: "amd64", CGO_ENABLED: "0" },
  { GOOS: "windows", GOARCH: "arm64", CGO_ENABLED: "0" },
]) {
  for (const line of command("go", ["list", "-deps", "-f", goTemplate, ...goTargets], target).split("\n").filter(Boolean)) moduleLines.add(line);
}
const modules = [...moduleLines].map((line) => {
    const [name, version, directory] = line.split("\t");
    if (!name || !version || !directory) throw new Error(`Go 模块清单格式不正确：${line}`);
    const licenseFile = findLicense(directory);
    if (!licenseFile) throw new Error(`Go 模块缺少许可证文件：${name}@${version}`);
    const raw = readFileSync(licenseFile);
    return { ecosystem: "go", name, versions: [version], licenseFile: path.basename(licenseFile), licenseSha256: digest(raw) };
  }).sort(compare);

const manifest = {
  schemaVersion: 1,
  fixedRuntimes: [
    { name: "Node.js", version: "22.22.0", license: "MIT", source: "https://github.com/nodejs/node" },
    { name: "MeshCentral", version: "1.2.5", license: "Apache-2.0", source: "https://github.com/Ylianst/MeshCentral" },
  ],
  packages: [...frontendPackages, ...modules],
};
writeFileSync(output, `${JSON.stringify(manifest, null, 2)}\n`, { mode: 0o644 });
process.stdout.write(`许可证清单已生成：${output}\n`);

function command(executable, args, overrides = {}) {
  const result = spawnSync(executable, args, {
    encoding: "utf8",
    env: {
      ...process.env,
      GOPROXY: "https://goproxy.cn",
      GOSUMDB: "sum.golang.google.cn",
      NPM_CONFIG_REGISTRY: "https://registry.npmmirror.com",
      ...overrides,
    },
  });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`${executable} 执行失败：${String(result.stderr).trim()}`);
  return String(result.stdout).trim();
}
function commandJSON(executable, args) { return JSON.parse(command(executable, args)); }
function findLicense(directory) {
  return readdirSync(directory).map((name) => path.join(directory, name)).find((file) => statSync(file).isFile() && /^(licen[cs]e|copying|notice)(\.|$)/i.test(path.basename(file)) && existsSync(file));
}
function digest(raw) { return createHash("sha256").update(raw).digest("hex"); }
function compare(left, right) { return `${left.ecosystem}:${left.name}`.localeCompare(`${right.ecosystem}:${right.name}`); }
