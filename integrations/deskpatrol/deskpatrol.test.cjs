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
  const serviceStart = installer.indexOf("systemctl enable --now");
  assert.notEqual(directorySetup, -1);
  assert.ok(directorySetup < serviceStart);
});

test("Linux Release 在 MeshCentral 父级预装动态运行依赖", () => {
  const buildScript = fs.readFileSync(path.resolve(__dirname, "../../scripts/build-linux-release.sh"), "utf8");
  assert.match(buildScript, /--prefix "\$stage\/meshcentral" install/);
  for (const dependency of ["ua-client-hints-js@0.1.2", "image-size@2.0.2", "pg@8.16.3", "otplib@13.4.1"]) {
    assert.match(buildScript, new RegExp(dependency.replaceAll(".", "\\.")));
  }
  assert.match(buildScript, /requireFromMeshCentral\.resolve/);
});
