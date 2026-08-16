import { Component, useEffect, useState, type ErrorInfo, type FormEvent, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { ThemeProvider, initializeTheme } from "@deskpatrol/design-tokens";
import { Activity, Check, LoaderCircle, Monitor, RefreshCw, Server, ShieldCheck } from "@deskpatrol/icons";
import { Button, Field, StatusBadge, ThemeControl } from "@deskpatrol/ui-admin";
import "@deskpatrol/ui-admin/styles.css";
import "./styles.css";

type Status = { activated: boolean; serverUrl: string; deviceId: string; deviceName: string; architecture: string; clientVersion: string; meshAgentStatus: string; connection: string; lastHeartbeat: string; screenCount: number; lastError: string };
type LogEntry = { timestamp: string; level: "INFO" | "ERROR"; message: string };
const bridge = {
  status: () => window.wails.Call.ByName<Status>("main.RuntimeApp.Status"),
  activate: (serverUrl: string, activationCode: string) => window.wails.Call.ByName<Status>("main.RuntimeApp.Activate", { serverUrl, activationCode }),
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
  if (!status) return <main className="loading"><LoaderCircle className="spin" /><span>正在读取客户端状态</span></main>;
  return <div className="client-shell"><header><div className="client-brand"><span><Monitor /></span><div><strong>DeskPatrol</strong><small>设备客户端</small></div></div><nav><button className={!diagnostics ? "active" : ""} onClick={() => setDiagnostics(false)}>状态</button><button className={diagnostics ? "active" : ""} onClick={() => setDiagnostics(true)}>技术诊断</button></nav><ThemeControl compact /><IconAction label="刷新" onClick={refresh}><RefreshCw /></IconAction></header>{error ? <p className="client-error">{error}</p> : null}{diagnostics ? <Diagnostics status={status} /> : status.activated ? <Home status={status} /> : <Activation onActivated={setStatus} />}</div>;
}

function Activation({ onActivated }: { onActivated: (status: Status) => void }) {
  const [serverUrl, setServerUrl] = useState(""); const [activationCode, setActivationCode] = useState(""); const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  const submit = async (event: FormEvent) => { event.preventDefault(); setBusy(true); setError(""); try { onActivated(await bridge.activate(serverUrl, activationCode)); } catch (next) { setError(text(next)); } finally { setBusy(false); } };
  return <main className="activation"><section><span className="eyebrow"><ShieldCheck />安全激活</span><h1>连接 DeskPatrol 服务</h1><p>输入 Linux 管理端 HTTPS 地址和管理员提供的一机一码。激活时会弹出一次系统权限确认。</p></section><form onSubmit={(event) => void submit(event)}><Field autoComplete="url" label="Linux 服务地址" placeholder="https://monitor.example.com" value={serverUrl} onChange={(event) => setServerUrl(event.target.value)} /><Field autoComplete="one-time-code" label="激活码" placeholder="XXXXXX-XXXXXX-XXXXXX-XXXXXX" value={activationCode} onChange={(event) => setActivationCode(event.target.value.toUpperCase())} />{error ? <p className="client-error">{error}</p> : null}<Button disabled={busy || !serverUrl || !activationCode} tone="primary" type="submit">{busy ? <><LoaderCircle className="spin" />正在激活</> : "激活设备"}</Button></form></main>;
}

function Home({ status }: { status: Status }) { return <main className="client-home"><div className="status-hero"><span><Check /></span><div><h1>设备已激活</h1><p>{status.connection}</p></div><StatusBadge status="ok">运行正常</StatusBadge></div><dl><Row label="设备名称" value={status.deviceName} /><Row label="设备编号" value={status.deviceId} /><Row label="服务地址" value={status.serverUrl} /><Row label="MeshAgent" value={status.meshAgentStatus} /></dl></main>; }
function Diagnostics({ status }: { status: Status }) {
  const [logs, setLogs] = useState<LogEntry[]>([]); const [logError, setLogError] = useState("");
  useEffect(() => { bridge.logs().then(setLogs).catch((error) => setLogError(text(error))); }, []);
  return <main className="diagnostics"><section><h2>设备信息</h2><Row label="设备编号" value={status.deviceId || "未激活"} /><Row label="设备名称" value={status.deviceName || "-"} /><Row label="客户端版本" value={status.clientVersion} /><Row label="系统架构" value={status.architecture} /></section><section><h2>运行状态</h2><Row label="激活状态" value={status.activated ? "已激活" : "未激活"} /><Row label="连接状态" value={status.connection} /><Row label="MeshAgent 服务" value={status.meshAgentStatus} /><Row label="最近心跳" value={status.lastHeartbeat ? new Date(status.lastHeartbeat).toLocaleString() : "尚无"} /><Row label="显示器" value={status.screenCount > 0 ? `${status.screenCount} 个` : "尚未上报"} /><Row label="最近错误" value={status.lastError || "无"} /></section><section className="runtime-logs"><h2>Runtime 日志</h2>{logError ? <p>{logError}</p> : logs.length ? <div>{logs.map((entry) => <code className={entry.level === "ERROR" ? "error" : ""} key={`${entry.timestamp}-${entry.message}`}><time>{new Date(entry.timestamp).toLocaleTimeString()}</time><b>{entry.level}</b><span>{entry.message}</span></code>)}</div> : <p>暂无日志</p>}</section><section className="diagnostic-note"><Activity /><div><strong>远程诊断</strong><p>管理员开启 15 分钟限时会话后，可通过既有出站 WSS 读取状态和执行受审计的诊断操作。</p></div></section></main>;
}
function Row({ label, value }: { label: string; value: string }) { return <div className="row"><dt>{label}</dt><dd>{value}</dd></div>; }
function IconAction({ label, children, onClick }: { label: string; children: ReactNode; onClick: () => void }) { return <button aria-label={label} className="icon-action" title={label} onClick={onClick}>{children}</button>; }
function text(error: unknown) { return String(error instanceof Error ? error.message : error).replace(/^Error:\s*/, ""); }
initializeTheme();
createRoot(document.getElementById("root")!).render(<ThemeProvider><ClientErrorBoundary><App /></ClientErrorBoundary></ThemeProvider>);
