"use strict";

const http = require("node:http");
const crypto = require("node:crypto");

module.exports.deskpatrol = function deskpatrol(handler) {
  const plugin = { exports: [] };
  const pending = new Map();
  const heartbeatTimers = new Map();

  plugin.server_startup = function serverStartup() {
    requireConfiguration();
    console.log("DeskPatrol plugin ready for MeshCentral 1.2.5");
  };

  plugin.hook_agentCoreIsStable = function agentCoreIsStable(agent, webServer) {
    const event = buildAgentEvent(agent, "agent_stable");
    sendEventWithRetry(event)
      .then(() => scheduleHeartbeat(agent, webServer, heartbeatTimers))
      .catch((error) => console.error("DeskPatrol agent event failed:", error.message));
  };

  plugin.hook_setupHttpHandlers = function setupHttpHandlers(webServer) {
    webServer.app.post("/deskpatrol/run-command", (request, response) => {
      handleCommand(request, response, webServer, pending).catch((error) => {
        if (!response.headersSent) response.status(500).json({ error: error.message });
      });
    });
  };

  plugin.hook_processAgentData = function processAgentData(command) {
    if (command?.type !== "runcommands" || typeof command.responseid !== "string") return;
    const waiting = pending.get(command.responseid);
    if (!waiting) return;
    pending.delete(command.responseid);
    clearTimeout(waiting.timer);
    try {
      waiting.response.status(200).json(parseCommandResult(command.result));
    } catch (error) {
      waiting.response.status(502).json({ error: error.message });
    }
  };

  return plugin;
};

function buildAgentEvent(agent, type = "agent_stable") {
  if (!agent || typeof agent.dbNodeKey !== "string" || typeof agent.dbMeshKey !== "string") {
    throw new Error("MeshCentral Agent 稳定事件缺少 dbNodeKey 或 dbMeshKey");
  }
  return {
    type,
    nodeId: agent.dbNodeKey,
    meshId: agent.dbMeshKey,
    name: typeof agent.name === "string" ? agent.name : "",
    agentType: Number.isInteger(agent.agentInfo?.agentId) ? agent.agentInfo.agentId : 0,
    agentVersion: Number.isInteger(agent.agentInfo?.agentVersion) ? agent.agentInfo.agentVersion : 0,
  };
}

function scheduleHeartbeat(agent, webServer, timers, delay = 30000) {
  const nodeId = agent.dbNodeKey;
  const previous = timers.get(nodeId);
  if (previous) clearTimeout(previous);
  const tick = async () => {
    const connected = webServer?.wsagents?.[nodeId] === agent && agent.authenticated === 2;
    try {
      await sendEventWithRetry(buildAgentEvent(agent, connected ? "agent_heartbeat" : "agent_offline"));
    } catch (error) {
      console.error("DeskPatrol agent heartbeat failed:", error.message);
    }
    if (!connected) {
      timers.delete(nodeId);
      return;
    }
    timers.set(nodeId, setTimeout(tick, delay));
  };
  timers.set(nodeId, setTimeout(tick, delay));
}

async function sendEvent(event) {
  const { callbackURL, token } = requireConfiguration();
  const body = Buffer.from(JSON.stringify(event));
  const target = new URL(callbackURL);
  await new Promise((resolve, reject) => {
    const request = http.request({
      protocol: target.protocol,
      hostname: target.hostname,
      port: target.port,
      path: target.pathname,
      method: "POST",
      headers: { "Content-Type": "application/json", "Content-Length": body.length, "X-DeskPatrol-Plugin-Token": token },
      timeout: 5000,
    }, (response) => {
      response.resume();
      response.once("end", () => response.statusCode >= 200 && response.statusCode < 300 ? resolve() : reject(new Error(`HTTP ${response.statusCode}`)));
    });
    request.once("timeout", () => request.destroy(new Error("callback timeout")));
    request.once("error", reject);
    request.end(body);
  });
}

async function sendEventWithRetry(event) {
  let lastError;
  for (const delay of [0, 1000, 3000, 8000]) {
    if (delay > 0) await new Promise((resolve) => setTimeout(resolve, delay));
    try { await sendEvent(event); return; } catch (error) { lastError = error; }
  }
  throw lastError;
}

async function handleCommand(request, response, webServer, pending) {
  const configured = requireConfiguration();
  if (!constantTimeEqual(request.get("X-DeskPatrol-Plugin-Token") || "", configured.token)) {
    response.status(401).json({ error: "unauthorized" });
    return;
  }
  const body = await readJSONBody(request, 48 * 1024);
  if (typeof body.nodeId !== "string" || !body.nodeId.startsWith("node/") || typeof body.script !== "string" || body.script.length < 1 || body.script.length > 32 * 1024 || !Number.isInteger(body.timeoutMs) || body.timeoutMs < 1000 || body.timeoutMs > 120000) {
    response.status(400).json({ error: "invalid command request" });
    return;
  }
  const agent = webServer.wsagents[body.nodeId];
  if (!agent || agent.authenticated !== 2 || !agent.agentInfo) {
    response.status(409).json({ error: "agent not connected" });
    return;
  }
  const responseId = `deskpatrol-${crypto.randomBytes(16).toString("hex")}`;
  const timer = setTimeout(() => {
    pending.delete(responseId);
    if (!response.headersSent) response.status(504).json({ error: "remote command timeout" });
  }, body.timeoutMs + 5000);
  pending.set(responseId, { response, timer });
  response.once("close", () => {
    const waiting = pending.get(responseId);
    if (waiting && !response.writableEnded) {
      clearTimeout(waiting.timer);
      pending.delete(responseId);
    }
  });
  try {
    agent.send(JSON.stringify({ action: "runcommands", type: 2, cmds: buildPowerShellWrapper(body.script, body.timeoutMs), runAsUser: 0, reply: true, responseid: responseId }));
  } catch (error) {
    clearTimeout(timer);
    pending.delete(responseId);
    response.status(502).json({ error: error.message });
  }
}

function buildPowerShellWrapper(script, timeoutMs) {
  const encoded = Buffer.from(script, "utf8").toString("base64");
  return [
    "$ErrorActionPreference='Stop'",
    `$source=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('${encoded}'))`,
    "$process=New-Object System.Diagnostics.Process",
    "$process.StartInfo.FileName=$env:SystemRoot+'\\System32\\WindowsPowerShell\\v1.0\\powershell.exe'",
    "$process.StartInfo.Arguments='-NoProfile -NonInteractive -Command -'",
    "$process.StartInfo.UseShellExecute=$false",
    "$process.StartInfo.CreateNoWindow=$true",
    "$process.StartInfo.RedirectStandardInput=$true",
    "$process.StartInfo.RedirectStandardOutput=$true",
    "$process.StartInfo.RedirectStandardError=$true",
    "[void]$process.Start()",
    "$stdoutTask=$process.StandardOutput.ReadToEndAsync()",
    "$stderrTask=$process.StandardError.ReadToEndAsync()",
    "$process.StandardInput.WriteLine($source)",
    "$process.StandardInput.Close()",
    `$timedOut=-not $process.WaitForExit(${timeoutMs})`,
    "if($timedOut){$process.Kill();$process.WaitForExit()}",
    "$stdout=$stdoutTask.GetAwaiter().GetResult()",
    "$stderr=$stderrTask.GetAwaiter().GetResult()",
    "$result=@{stdout=[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($stdout));stderr=[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($stderr));exitCode=$(if($timedOut){-2}else{$process.ExitCode});timedOut=$timedOut}",
    "$result|ConvertTo-Json -Compress",
  ].join("\r\n");
}

function parseCommandResult(raw) {
  if (typeof raw !== "string") throw new Error("remote command response missing");
  const line = raw.trim().split(/\r?\n/).reverse().find((value) => value.startsWith("{"));
  if (!line) throw new Error("remote command response invalid");
  const value = JSON.parse(line);
  const stdout = Buffer.from(value.stdout || "", "base64");
  const stderr = Buffer.from(value.stderr || "", "base64");
  const stdoutLimited = stdout.subarray(0, 256 * 1024);
  const stderrLimited = stderr.subarray(0, 256 * 1024);
  return {
    stdout: stdoutLimited.toString("utf8"), stderr: stderrLimited.toString("utf8"),
    exitCode: Number.isInteger(value.exitCode) ? value.exitCode : -1,
    timedOut: value.timedOut === true,
    outputTruncated: stdout.length > stdoutLimited.length || stderr.length > stderrLimited.length,
  };
}

function readJSONBody(request, limit) {
  if (request.body && typeof request.body === "object") return Promise.resolve(request.body);
  return new Promise((resolve, reject) => {
    const chunks = []; let size = 0;
    request.on("data", (chunk) => {
      size += chunk.length;
      if (size > limit) { reject(new Error("request too large")); request.destroy(); return; }
      chunks.push(chunk);
    });
    request.on("end", () => { try { resolve(JSON.parse(Buffer.concat(chunks).toString("utf8"))); } catch (error) { reject(error); } });
    request.on("error", reject);
  });
}

function constantTimeEqual(left, right) {
  const a = Buffer.from(left); const b = Buffer.from(right);
  return a.length === b.length && crypto.timingSafeEqual(a, b);
}

function requireConfiguration() {
  const callbackURL = process.env.DESKPATROL_CALLBACK_URL || "";
  const token = process.env.DESKPATROL_PLUGIN_TOKEN || "";
  if (!callbackURL.startsWith("http://127.0.0.1:") || token.length < 32) {
    throw new Error("DESKPATROL_CALLBACK_URL 或 DESKPATROL_PLUGIN_TOKEN 未正确配置");
  }
  return { callbackURL, token };
}

module.exports._test = { buildAgentEvent, requireConfiguration, buildPowerShellWrapper, parseCommandResult, constantTimeEqual, scheduleHeartbeat };
