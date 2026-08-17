import { Component, useEffect, useState, type ErrorInfo, type FormEvent, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { ThemeProvider, initializeTheme } from "@deskpatrol/design-tokens";
import { Activity, Check, LoaderCircle, Monitor, RefreshCw, Server, ShieldCheck } from "@deskpatrol/icons";
import { Button, Field, StatusBadge, ThemeControl } from "@deskpatrol/ui-admin";
import "@deskpatrol/ui-admin/styles.css";
import "./styles.css";

const runtimeModulePath = "/wails/runtime.js";
let runtimeLoadError = "";
try {
  await import(/* @vite-ignore */ runtimeModulePath);
} catch (error) {
  runtimeLoadError = text(error);
}

type Status = { activated: boolean; serverUrl: string; deviceId: string; deviceName: string; architecture: string; clientVersion: string; meshAgentStatus: string; agentSetupStatus: "pending" | "preparing" | "installing" | "ready" | "failed"; agentSetupError: string; agentNextRetryAt: string; connection: string; lastHeartbeat: string; screenCount: number; lastError: string };
type LogEntry = { timestamp: string; level: "INFO" | "ERROR"; message: string };
const bridge = {
  status: () => window.wails.Call.ByName<Status>("main.RuntimeApp.Status"),
  activate: (connectionKey: string) => window.wails.Call.ByName<Status>("main.RuntimeApp.Activate", { connectionKey }),
  retryAgentSetup: () => window.wails.Call.ByName<void>("main.RuntimeApp.RetryAgentSetup"),
  logs: () => window.wails.Call.ByName<LogEntry[]>("main.RuntimeApp.Logs", { limit: 50, level: "", contains: "" }),
  reportFrontendError: (value: unknown) => window.wails.Call.ByName<void>("main.RuntimeApp.ReportFrontendError", JSON.stringify(value)),
};

class ClientErrorBoundary extends Component<{ children: ReactNode }, { error: string }> {
  state = { error: "" };
  static getDerivedStateFromError(error: Error) { return { error: error.message }; }
  componentDidCatch(error: Error, info: ErrorInfo) {
    void bridge.reportFrontendError({ eventId: crypto.randomUUID(), occurredAt: new Date().toISOString(), category: "react_error_boundary", message: error.message, stack: `${error.stack || ""}\n${info.componentStack || ""}`, clientVersion: "0.1.0" }).catch((reportError) => console.error("上报 React 异常失败", reportError));
  }
  render() { return this.state.error ? <main className="loading"><Activity /><span>客户端界面异常：{this.state.error}</span></main> : this.props.children; }
}

function App() {
  const [status, setStatus] = useState<Status | null>(null); const [diagnostics, setDiagnostics] = useState(false); const [error, setError] = useState("");
  const refresh = () => bridge.status().then(setStatus).catch((next) => setError(text(next)));
  useEffect(() => {
    void refresh();
    const report = (category: string, input: unknown) => void bridge.reportFrontendError({ eventId: crypto.randomUUID(), occurredAt: new Date().toISOString(), category, message: input instanceof Error ? input.message : String(input), stack: input instanceof Error ? input.stack || "" : "", clientVersion: "0.1.0" }).catch((reportError) => console.error("上报前端异常失败", reportError));
    const onError = (event: ErrorEvent) => report("window_error", event.error || event.message);
    const onRejection = (event: PromiseRejectionEvent) => report("unhandled_rejection", event.reason);
    window.addEventListener("error", onError); window.addEventListener("unhandledrejection", onRejection);
    return () => { window.removeEventListener("error", onError); window.removeEventListener("unhandledrejection", onRejection); };
  }, []);
  useEffect(() => {
    if (!status?.activated || status.agentSetupStatus === "ready") return;
    let cancelled = false;
    let timer = 0;
    const poll = async () => { await refresh(); if (!cancelled) timer = window.setTimeout(() => void poll(), 2000); };
    timer = window.setTimeout(() => void poll(), 2000);
    return () => { cancelled = true; window.clearTimeout(timer); };
  }, [status?.activated, status?.agentSetupStatus]);
  const retryAgentSetup = async () => { setError(""); try { await bridge.retryAgentSetup(); await refresh(); } catch (next) { setError(text(next)); } };
  if (!status) return <main className="loading"><LoaderCircle className="spin" /><span>正在读取客户端状态</span></main>;
  return <div className="client-shell"><header><div className="client-brand"><span><Monitor /></span><div><strong>DeskPatrol</strong><small>设备客户端</small></div></div><nav><button className={!diagnostics ? "active" : ""} onClick={() => setDiagnostics(false)}>状态</button><button className={diagnostics ? "active" : ""} onClick={() => setDiagnostics(true)}>技术诊断</button></nav><ThemeControl compact /><IconAction label="刷新" onClick={refresh}><RefreshCw /></IconAction></header>{error ? <p className="client-error">{error}</p> : null}{diagnostics ? <Diagnostics onRetry={() => void retryAgentSetup()} status={status} /> : status.activated ? <Home onRetry={() => void retryAgentSetup()} status={status} /> : <Activation onActivated={setStatus} />}</div>;
}

function Activation({ onActivated }: { onActivated: (status: Status) => void }) {
  const [connectionKey, setConnectionKey] = useState(""); const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  const submit = async (event: FormEvent) => { event.preventDefault(); setBusy(true); setError(""); try { onActivated(await bridge.activate(connectionKey)); } catch (next) { setError(text(next)); } finally { setBusy(false); } };
  return <main className="activation"><section><span className="eyebrow"><ShieldCheck />安全激活</span><h1>连接 DeskPatrol 服务</h1><p>粘贴管理员提供的连接密钥。连接成功后设备会立即进入已激活状态，MeshAgent 将在后续步骤中准备并安装。</p></section><form onSubmit={(event) => void submit(event)}><Field autoComplete="off" label="连接密钥" placeholder="dp-link. ..." value={connectionKey} onChange={(event) => setConnectionKey(event.target.value)} />{error ? <p className="client-error">{error}</p> : null}<Button disabled={busy || !connectionKey.trim()} tone="primary" type="submit">{busy ? <><LoaderCircle className="spin" />正在激活</> : "激活设备"}</Button></form></main>;
}

function Home({ status, onRetry }: { status: Status; onRetry: () => void }) {
  const setup = agentSetupPresentation(status);
  return <main className="client-home"><div className={`status-hero status-hero--${setup.tone}`}><span>{status.agentSetupStatus === "ready" ? <Check /> : status.agentSetupStatus === "failed" ? <Activity /> : <LoaderCircle className="spin" />}</span><div><h1>设备已激活</h1><p>{status.connection}</p></div><StatusBadge status={setup.badge}>{setup.label}</StatusBadge></div>{status.agentSetupStatus === "failed" ? <section className="agent-setup-error"><div><strong>MeshAgent 尚未完成</strong><p>{status.agentSetupError || "Agent 安装失败，Runtime 将自动重试。"}</p>{status.agentNextRetryAt ? <small>下次重试：{new Date(status.agentNextRetryAt).toLocaleString()}</small> : null}</div><Button icon={<RefreshCw />} onClick={onRetry}>立即重试</Button></section> : null}<dl><Row label="设备名称" value={status.deviceName} /><Row label="设备编号" value={status.deviceId} /><Row label="服务地址" value={status.serverUrl} /><Row label="Agent 准备" value={setup.label} /><Row label="MeshAgent" value={status.meshAgentStatus} /></dl></main>;
}
function Diagnostics({ status, onRetry }: { status: Status; onRetry: () => void }) {
  const [logs, setLogs] = useState<LogEntry[]>([]); const [logError, setLogError] = useState("");
  useEffect(() => { bridge.logs().then(setLogs).catch((error) => setLogError(text(error))); }, []);
  const setup = agentSetupPresentation(status);
  return <main className="diagnostics"><section><h2>设备信息</h2><Row label="设备编号" value={status.deviceId || "未激活"} /><Row label="设备名称" value={status.deviceName || "-"} /><Row label="客户端版本" value={status.clientVersion} /><Row label="系统架构" value={status.architecture} /></section><section><h2>运行状态</h2><Row label="激活状态" value={status.activated ? "已激活" : "未激活"} /><Row label="Agent 准备" value={setup.label} /><Row label="连接状态" value={status.connection} /><Row label="MeshAgent 服务" value={status.meshAgentStatus} /><Row label="最近心跳" value={status.lastHeartbeat ? new Date(status.lastHeartbeat).toLocaleString() : "尚无"} /><Row label="显示器" value={status.screenCount > 0 ? `${status.screenCount} 个` : "尚未上报"} /><Row label="最近错误" value={status.lastError || "无"} />{status.activated && status.agentSetupStatus !== "ready" ? <div className="diagnostic-action"><Button icon={<RefreshCw />} onClick={onRetry}>立即重试 Agent</Button></div> : null}</section><section className="runtime-logs"><h2>Runtime 日志</h2>{logError ? <p>{logError}</p> : logs.length ? <div>{logs.map((entry) => <code className={entry.level === "ERROR" ? "error" : ""} key={`${entry.timestamp}-${entry.message}`}><time>{new Date(entry.timestamp).toLocaleTimeString()}</time><b>{entry.level}</b><span>{entry.message}</span></code>)}</div> : <p>暂无日志</p>}</section><section className="diagnostic-note"><Activity /><div><strong>远程诊断</strong><p>管理员开启 15 分钟限时会话后，可通过既有出站 WSS 读取状态和执行受审计的诊断操作。</p></div></section></main>;
}
function agentSetupPresentation(status: Status): { label: string; badge: "ok" | "warn" | "error" | "muted"; tone: "ok" | "warn" | "error" } { switch (status.agentSetupStatus) { case "ready": return { label: "运行正常", badge: "ok", tone: "ok" }; case "failed": return { label: "等待重试", badge: "error", tone: "error" }; case "installing": return { label: "正在安装", badge: "warn", tone: "warn" }; case "preparing": return { label: "正在准备", badge: "warn", tone: "warn" }; default: return { label: "等待准备", badge: "muted", tone: "warn" }; } }
function Row({ label, value }: { label: string; value: string }) { return <div className="row"><dt>{label}</dt><dd>{value}</dd></div>; }
function IconAction({ label, children, onClick }: { label: string; children: ReactNode; onClick: () => void }) { return <button aria-label={label} className="icon-action" title={label} onClick={onClick}>{children}</button>; }
function text(error: unknown) { return String(error instanceof Error ? error.message : error).replace(/^Error:\s*/, ""); }

function RuntimeUnavailable({ error }: { error: string }) {
  return <main className="loading runtime-unavailable"><Activity /><div><strong>客户端运行时无法加载</strong><p>{error || "Wails Runtime 未响应"}</p><small>请重新安装 DeskPatrol，或联系管理员提供这台设备的 Runtime 日志。</small></div></main>;
}

initializeTheme();
createRoot(document.getElementById("root")!).render(runtimeLoadError ? <RuntimeUnavailable error={runtimeLoadError} /> : <ThemeProvider><ClientErrorBoundary><App /></ClientErrorBoundary></ThemeProvider>);
