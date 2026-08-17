#!/usr/bin/env node
import { spawn, spawnSync } from "node:child_process";
import fs from "node:fs";
import http from "node:http";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../..");
const inputArgs = process.argv.slice(2);
const args = inputArgs[0] === "--" ? inputArgs.slice(1) : inputArgs;
const env = {
  ...process.env,
  GOPROXY: process.env.GOPROXY || "https://goproxy.cn",
  GOSUMDB: process.env.GOSUMDB || "sum.golang.google.cn",
  NPM_CONFIG_REGISTRY: process.env.NPM_CONFIG_REGISTRY || "https://registry.npmmirror.com",
  COREPACK_NPM_REGISTRY: process.env.COREPACK_NPM_REGISTRY || "https://registry.npmmirror.com",
};

try {
  await dispatch(args);
} catch (error) {
  process.stderr.write(`错误：${error instanceof Error ? error.message : String(error)}\n`);
  process.exitCode = 1;
}

async function dispatch(values) {
  const [command = "help", target = ""] = values;
  if (values.includes("--help") || values.includes("-h") || command === "help") return help();
  if (command === "doctor") return doctor();
  if (command === "env") return printEnv();
  if (command === "status") return status();
  if (command === "setup-status") return setupStatus();
  if (command === "deps") return deps(target || "all");
  if (command === "build") return build(target || "all");
  if (command === "dev") return dev(target || "start", values.slice(2));
  if (command === "test") return test(target || "all");
  if (command === "typecheck") return run("pnpm", ["-r", "--if-present", "typecheck"]);
  if (command === "verify") return verify(target || "all");
  if (command === "migrate") return runServerCommand("migrate");
  if (command === "db" && target === "reset") throw new Error("db reset 仅允许通过隔离测试数据库入口执行，当前未提供目标数据库");
  if (command === "reinit") throw new Error("reinit 会删除本地持久状态，必须在实现隔离开发数据确认后再开放");
  if (command === "release") return release(values.slice(1));
  if (command === "scaffold") return scaffold(values.slice(1));
  throw new Error(`未知命令：${command}`);
}

function help() {
  process.stdout.write(`DeskPatrol Repo CLI\n\n` + [
    "repo help|doctor|env|status|setup-status",
    "repo deps backend|frontend|client|all",
    "repo build backend|admin|showcase|client|linux-release|all",
    "repo dev start|status|stop|restart|logs",
    "repo test affected|backend|frontend|client|release|all",
    "repo typecheck|verify backend|frontend|client|all",
    "repo migrate|db reset|reinit",
    "repo release prepare|verify --version <version>",
    "repo scaffold ui-component <Name>",
  ].join("\n") + "\n");
}

function doctor() {
  const checks = [
    ["Node", process.versions.node, process.versions.node.startsWith("22.")],
    ["pnpm", commandOutput("pnpm", ["--version"]), commandOutput("pnpm", ["--version"]) === "10.32.1"],
    ["Go", commandOutput("go", ["version"]), /go1\.26\./.test(commandOutput("go", ["version"]))],
  ];
  for (const [name, value, ok] of checks) process.stdout.write(`${ok ? "正常" : "异常"}  ${name}: ${value}\n`);
  if (checks.some((item) => !item[2])) throw new Error("工具链与仓库基线不一致");
}

function printEnv() {
  process.stdout.write(`仓库：${repoRoot}\n配置：${path.join(repoRoot, "var/config.json")}\n服务：http://127.0.0.1:18123\n`);
}

async function status() {
  const result = await getJSON("http://127.0.0.1:18123/healthz").catch((error) => ({ error: error.message }));
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
  if (result.error) process.exitCode = 1;
}

async function setupStatus() {
  const result = await getJSON("http://127.0.0.1:18123/api/setup/status");
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
}

function deps(target) {
  assertTarget(target, ["backend", "frontend", "client", "all"]);
  if (["backend", "all"].includes(target)) run("go", ["mod", "download"]);
  if (["frontend", "client", "all"].includes(target)) run("pnpm", ["install", "--frozen-lockfile", "--child-concurrency=2"]);
}

function build(target) {
  assertTarget(target, ["backend", "admin", "showcase", "client", "linux-release", "all"]);
  if (["admin", "all"].includes(target)) run("pnpm", ["--filter", "@deskpatrol/admin", "build"]);
  if (["showcase", "all"].includes(target)) run("pnpm", ["--filter", "@deskpatrol/ui-showcase", "build"]);
  if (["backend", "all"].includes(target)) {
    fs.mkdirSync(path.join(repoRoot, "dist"), { recursive: true });
    run("go", ["build", "-p=2", "-o", "dist/deskpatrol-server", "./cmd/server"]);
  }
  if (["client", "all"].includes(target)) run("go", ["build", "-p=2", "./cmd/client"]);
  if (target === "linux-release") release(["prepare", ...process.argv.slice(4)]);
}

async function dev(action, rest) {
  assertTarget(action, ["start", "status", "stop", "restart", "logs"]);
  if (action === "status") return status();
  if (action === "logs") return tailLog(Number.parseInt(rest[1] || rest[0] || "120", 10));
  if (action === "stop") return stopServer();
  if (action === "restart") stopServer();
  const binary = path.join(repoRoot, "dist/deskpatrol-server");
  if (!fs.existsSync(binary)) throw new Error("未找到 dist/deskpatrol-server，请先执行 repo build backend");
  if (!fs.existsSync(path.join(repoRoot, "frontend/apps/admin/dist/index.html"))) throw new Error("未找到管理端产物，请先执行 repo build admin");
  const child = spawn(binary, ["--config", "var/config.json", "--assets", "frontend/apps/admin/dist"], { cwd: repoRoot, env, stdio: "inherit" });
  const code = await new Promise((resolve, reject) => { child.once("error", reject); child.once("exit", (value, signal) => resolve(value ?? (signal ? 1 : 0))); });
  if (code !== 0) throw new Error(`开发服务退出，code=${code}`);
}

function test(target) {
  assertTarget(target, ["affected", "backend", "frontend", "client", "release", "all"]);
  if (["affected", "backend", "all"].includes(target)) run("go", ["test", "-p=2", "-parallel=2", "./internal/...", "./cmd/server"]);
  if (["affected", "frontend", "all"].includes(target)) run("pnpm", ["-r", "--workspace-concurrency=1", "--if-present", "test"]);
  if (["client", "all"].includes(target)) run("go", ["test", "-p=2", "-parallel=2", "./cmd/client/...", "./cmd/client-helper"]);
  if (["affected", "all"].includes(target)) run("node", ["--test", "integrations/deskpatrol/deskpatrol.test.cjs", "scripts/patch-meshcentral.test.mjs"]);
  if (target === "release") { run("node", ["--test", "integrations/deskpatrol/deskpatrol.test.cjs", "scripts/patch-meshcentral.test.mjs"]); release(["verify", ...process.argv.slice(4)]); }
}

function verify(target) {
  assertTarget(target, ["backend", "frontend", "client", "all"]);
  if (["backend", "all"].includes(target)) test("backend");
  if (["frontend", "all"].includes(target)) { run("pnpm", ["-r", "--if-present", "typecheck"]); test("frontend"); }
  if (["client", "all"].includes(target)) test("client");
}

function release(values) {
  const [action] = values;
  if (action !== "prepare" && action !== "verify") throw new Error("用法: repo release prepare|verify --version <version>");
  const version = option(values, "--version");
  if (!/^\d+\.\d+\.\d+$/.test(version)) throw new Error("Release 版本格式必须为 x.y.z");
  const repository = process.env.GITHUB_REPOSITORY || "";
  if (!/^[^/]+\/[^/]+$/.test(repository)) throw new Error("必须显式设置 GITHUB_REPOSITORY=owner/repository");
  run("node", ["scripts/release.mjs", action, "--version", version], { ...env, GITHUB_REPOSITORY: repository });
}

function scaffold(values) {
  if (values[0] !== "ui-component" || !/^[A-Z][A-Za-z0-9]+$/.test(values[1] || "")) throw new Error("用法: repo scaffold ui-component <PascalCaseName>");
  const name = values[1];
  const filename = name.replace(/([a-z0-9])([A-Z])/g, "$1-$2").toLowerCase();
  const componentDir = path.join(repoRoot, "frontend/packages/ui-admin/src/components");
  const componentPath = path.join(componentDir, `${filename}.tsx`);
  const testPath = path.join(componentDir, `${filename}.test.tsx`);
  const indexPath = path.join(repoRoot, "frontend/packages/ui-admin/src/index.tsx");
  if (fs.existsSync(componentPath) || fs.existsSync(testPath)) throw new Error(`组件 ${name} 已存在，拒绝覆盖`);
  fs.mkdirSync(componentDir, { recursive: true });
  fs.writeFileSync(componentPath, `import type { HTMLAttributes } from "react";\n\nexport function ${name}({ className = "", ...props }: HTMLAttributes<HTMLDivElement>) {\n  return <div className={\`${filename} \${className}\`.trim()} {...props} />;\n}\n`, { mode: 0o644 });
  fs.writeFileSync(testPath, `import { describe, expect, it } from "vitest";\nimport { ${name} } from "./${filename}";\n\ndescribe("${name}", () => {\n  it("导出可调用组件", () => { expect(typeof ${name}).toBe("function"); });\n});\n`, { mode: 0o644 });
  fs.appendFileSync(indexPath, `\nexport { ${name} } from "./components/${filename}";\n`);
  process.stdout.write(`已生成 frontend/packages/ui-admin/src/components/${filename}.tsx\n`);
}

function runServerCommand(command) {
  run("dist/deskpatrol-server", [command, "--config", "var/config.json"]);
}

function run(command, commandArgs, commandEnv = env) {
  const result = spawnSync(command, commandArgs, { cwd: repoRoot, env: commandEnv, stdio: "inherit" });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`${command} 执行失败，exit=${result.status}`);
}

function commandOutput(command, commandArgs) {
  const result = spawnSync(command, commandArgs, { cwd: repoRoot, env, encoding: "utf8" });
  return result.error ? result.error.message : String(result.stdout || result.stderr).trim();
}

function assertTarget(value, allowed) {
  if (!allowed.includes(value)) throw new Error(`目标 ${value} 不受支持，可选：${allowed.join("、")}`);
}

function option(values, key) {
  const index = values.indexOf(key);
  return index >= 0 ? values[index + 1] || "" : "";
}

function stopServer() {
  const pidPath = path.join(repoRoot, "var/run/server.pid");
  if (!fs.existsSync(pidPath)) throw new Error("DeskPatrol 服务未运行");
  const pid = Number.parseInt(fs.readFileSync(pidPath, "utf8"), 10);
  if (!Number.isInteger(pid) || pid <= 1) throw new Error("服务 PID 文件内容不正确");
  process.kill(pid, "SIGTERM");
  process.stdout.write(`已请求停止 DeskPatrol 服务 pid=${pid}\n`);
}

function tailLog(lines) {
  const logPath = path.join(repoRoot, "var/log/server.log");
  if (!fs.existsSync(logPath)) throw new Error("服务日志尚不存在");
  const values = fs.readFileSync(logPath, "utf8").trimEnd().split("\n");
  process.stdout.write(`${values.slice(-Math.max(1, Math.min(lines, 1000))).join("\n")}\n`);
}

function getJSON(url) {
  return new Promise((resolve, reject) => {
    const request = http.get(url, { timeout: 3000 }, (response) => {
      const chunks = [];
      response.on("data", (chunk) => chunks.push(chunk));
      response.on("end", () => {
        try { resolve(JSON.parse(Buffer.concat(chunks).toString("utf8"))); } catch (error) { reject(error); }
      });
    });
    request.on("timeout", () => request.destroy(new Error("服务响应超时")));
    request.on("error", reject);
  });
}
