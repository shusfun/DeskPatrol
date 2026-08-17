const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const plugin = require("./deskpatrol.js");

test("固定 MeshCentral Agent 字段形成稳定事件", () => {
  const event = plugin._test.buildAgentEvent({ dbNodeKey: "node//abc", dbMeshKey: "mesh//group", name: "PC-01", agentInfo: { agentId: 3, agentVersion: 2026 } });
  assert.deepEqual(event, { type: "agent_stable", nodeId: "node//abc", meshId: "mesh//group", name: "PC-01", agentType: 3, agentVersion: 2026 });
});

test("缺少固定字段时直接失败", () => {
  assert.throws(() => plugin._test.buildAgentEvent({}), /dbNodeKey/);
});

test("心跳事件沿用固定 Agent 标识", () => {
  const event = plugin._test.buildAgentEvent({ dbNodeKey: "node//abc", dbMeshKey: "mesh//group", name: "PC-01", agentInfo: {} }, "agent_heartbeat");
  assert.equal(event.type, "agent_heartbeat");
  assert.equal(event.nodeId, "node//abc");
});

test("Agent 对象替换后仍向当前连接发送心跳", async () => {
  const nodeId = "node//replacement";
  const previous = { dbNodeKey: nodeId, dbMeshKey: "mesh//group", name: "PC-01", authenticated: 2, agentInfo: { agentVersion: 1 } };
  const current = { dbNodeKey: nodeId, dbMeshKey: "mesh//group", name: "PC-01", authenticated: 2, agentInfo: { agentVersion: 2 } };
  const webServer = { wsagents: { [nodeId]: previous } };
  const timers = new Map();
  let resolveEvent;
  const emitted = new Promise((resolve) => { resolveEvent = resolve; });
  plugin._test.scheduleHeartbeatWithSender(previous, webServer, timers, 5, async (event) => { resolveEvent(event); });
  webServer.wsagents[nodeId] = current;
  const event = await emitted;
  const state = timers.get(nodeId);
  clearTimeout(state.timer);
  timers.delete(nodeId);
  assert.equal(event.type, "agent_heartbeat");
  assert.equal(event.agentVersion, 2);
});

test("过期心跳回调不会清除新连接的定时器", async () => {
  const nodeId = "node//stale";
  const previous = { dbNodeKey: nodeId, dbMeshKey: "mesh//group", name: "PC-01", authenticated: 2, agentInfo: {} };
  const current = { dbNodeKey: nodeId, dbMeshKey: "mesh//group", name: "PC-01", authenticated: 2, agentInfo: {} };
  const webServer = { wsagents: { [nodeId]: previous } };
  const timers = new Map();
  let release;
  let entered;
  const started = new Promise((resolve) => { entered = resolve; });
  const blocked = new Promise((resolve) => { release = resolve; });
  plugin._test.scheduleHeartbeatWithSender(previous, webServer, timers, 5, async (event) => {
    assert.equal(event.type, "agent_offline");
    entered();
    await blocked;
  });
  delete webServer.wsagents[nodeId];
  await started;
  webServer.wsagents[nodeId] = current;
  plugin._test.scheduleHeartbeatWithSender(current, webServer, timers, 1000, async () => {});
  const currentState = timers.get(nodeId);
  release();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(timers.get(nodeId), currentState);
  clearTimeout(currentState.timer);
  timers.delete(nodeId);
});

test("PowerShell 包装器只通过 stdin 解码脚本并解析分离输出", () => {
  const wrapper = plugin._test.buildPowerShellWrapper("Write-Output 'ok'", 30000);
  assert.doesNotMatch(wrapper, /Write-Output 'ok'/);
  assert.match(wrapper, /StandardInput\.WriteLine/);
  const value = plugin._test.parseCommandResult(JSON.stringify({ stdout: Buffer.from("out").toString("base64"), stderr: Buffer.from("err").toString("base64"), exitCode: 7, timedOut: false }));
  assert.deepEqual(value, { stdout: "out", stderr: "err", exitCode: 7, timedOut: false, outputTruncated: false });
});

test("插件令牌使用常量时间比较", () => {
  assert.equal(plugin._test.constantTimeEqual("same", "same"), true);
  assert.equal(plugin._test.constantTimeEqual("same", "different"), false);
});

test("Linux 安装脚本在启动 MeshCentral 前创建全部可写目录", () => {
  const installer = fs.readFileSync(path.resolve(__dirname, "../../scripts/install.sh"), "utf8");
  const directorySetup = installer.indexOf("/var/lib/deskpatrol/meshcentral-files");
  const serviceStart = installer.indexOf("systemctl start deskpatrol.service deskpatrol-meshcentral.path");
  assert.notEqual(directorySetup, -1);
  assert.ok(directorySetup < serviceStart);
});

test("Linux 升级先迁移再切换版本并重启服务", () => {
  const installer = fs.readFileSync(path.resolve(__dirname, "../../scripts/install.sh"), "utf8");
  const migration = installer.indexOf('"$release_dir/bin/deskpatrol-server" migrate');
  const switchVersion = installer.indexOf('ln -sfn "$release_dir" "$install_root/current"', migration);
  const restartServices = installer.indexOf("systemctl restart deskpatrol.service deskpatrol-meshcentral.service");
  assert.notEqual(migration, -1);
  assert.ok(migration < switchVersion);
  assert.ok(switchVersion < restartServices);
});

test("MeshCentral systemd 服务在启动前引导内部管理员", () => {
  const unit = fs.readFileSync(path.resolve(__dirname, "../../deploy/systemd/deskpatrol-meshcentral.service"), "utf8");
  const bootstrap = fs.readFileSync(path.resolve(__dirname, "../../deploy/systemd/bootstrap-meshcentral.sh"), "utf8");
  assert.match(unit, /^ExecStartPre=.*bootstrap-meshcentral\.sh$/m);
  assert.match(bootstrap, /--createaccount admin/);
  assert.match(bootstrap, /--adminaccount admin/);
  assert.match(bootstrap, /password=.*randomBytes\(32\)/);
  assert.match(bootstrap, /--pass "\$password"/);
});

test("MeshCentral AgentDownload 通过仅回环可见的 TLS 入口", () => {
  const nginx = fs.readFileSync(path.resolve(__dirname, "../../deploy/nginx/deskpatrol.conf.example"), "utf8");
  assert.match(nginx, /listen 127\.0\.0\.1:18130 ssl;/);
  assert.match(nginx, /location = \/control\.ashx[\s\S]*proxy_pass http:\/\/127\.0\.0\.1:18129;/);
  assert.match(nginx, /location = \/meshagents[\s\S]*proxy_pass http:\/\/127\.0\.0\.1:18129;/);
});

test("Linux Release 在 MeshCentral 父级预装动态运行依赖", () => {
  const buildScript = fs.readFileSync(path.resolve(__dirname, "../../scripts/build-linux-release.sh"), "utf8");
  assert.match(buildScript, /--prefix "\$stage\/meshcentral" install/);
  for (const dependency of ["ua-client-hints-js@0.1.2", "image-size@2.0.2", "pg@8.16.3", "otplib@13.4.1"]) {
    assert.match(buildScript, new RegExp(dependency.replaceAll(".", "\\.")));
  }
  assert.match(buildScript, /requireFromMeshCentral\.resolve/);
});
