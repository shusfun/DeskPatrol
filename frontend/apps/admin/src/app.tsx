import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import { json, request, ApiError } from "@deskpatrol/api";
import { getCurrentAdministrator, login, logout } from "@deskpatrol/auth";
import { formatBytes, formatDate } from "@deskpatrol/core";
import { Activity, AppWindow, ChevronLeft, Clipboard, Copy, Download, Expand, Grid2X2, KeyRound, LayoutDashboard, List, LoaderCircle, LockKeyhole, LogOut, Monitor, Plus, RefreshCw, Search, Server, Settings, ShieldCheck, Terminal } from "@deskpatrol/icons";
import type { ActivationCode, ActivationCodeCreated, Administrator, DebugSession, Device, DownloadArtifact, ReleaseJob, SetupRequest, SetupResult, SetupStatus, WallLayout } from "@deskpatrol/types";
import { Button, Dialog, EmptyState, Field, IconButton, SelectField, StatusBadge, ThemeControl } from "@deskpatrol/ui-admin";
import { createToolbarAutoHideController, isImmersionExitKey, moveDeviceOrder } from "./wall-immersion";

type Page = "wall" | "devices" | "codes" | "downloads" | "diagnostics" | "audit" | "settings";
type AuditLog = { id: number; operation: string; scriptSha256: string; durationMs: number; exitCode: number | null; outputTruncated: boolean; createdAt: string; sessionId: string; deviceId: string; deviceName: string; administrator: string };
type MeshSharingWindow = Window & typeof globalThis & {
  connectDesktop?: (event: null, connectionType: number) => void;
  desktopsettings?: { framerate?: number };
};

export function App() {
  const [setupStatus, setSetupStatus] = useState<SetupStatus | null>(null);
  const [administrator, setAdministrator] = useState<Administrator | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const bootstrap = async () => {
    setLoading(true); setError("");
    try {
      const status = await request<SetupStatus>("/api/setup/status");
      setSetupStatus(status);
      if (!status.needsSetup) {
        try { setAdministrator(await getCurrentAdministrator()); }
        catch (next) { if (!(next instanceof ApiError && next.status === 401)) throw next; }
      }
    } catch (next) { setError(message(next)); }
    finally { setLoading(false); }
  };

  useEffect(() => { void bootstrap(); }, []);
  if (loading) return <LoadingScreen />;
  if (error) return <FatalError error={error} onRetry={() => void bootstrap()} />;
  if (setupStatus?.needsSetup) return <SetupWizard status={setupStatus} onComplete={() => { setSetupStatus({ needsSetup: false, step: "complete" }); }} />;
  if (!administrator) return <LoginPage onLogin={setAdministrator} />;
  return <AdminShell administrator={administrator} onLogout={() => { void logout().finally(() => setAdministrator(null)); }} />;
}

function LoadingScreen() {
  return <main className="center-screen"><div className="brand-mark"><Monitor /></div><LoaderCircle className="spin" /><p>正在读取服务状态</p></main>;
}

function FatalError({ error, onRetry }: { error: string; onRetry: () => void }) {
  return <main className="center-screen"><Server className="fatal-icon" /><h1>DeskPatrol 无法启动</h1><p>{error}</p><Button icon={<RefreshCw size={16} />} onClick={onRetry}>重新连接</Button></main>;
}

function SetupWizard({ status, onComplete }: { status: SetupStatus; onComplete: () => void }) {
  const defaults = status.defaults!;
  const [form, setForm] = useState<SetupRequest>({ ...defaults, database: { ...defaults.database }, admin: { ...defaults.admin, password: "" } });
  const [step, setStep] = useState(0);
  const [databaseAdvanced, setDatabaseAdvanced] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<SetupResult | null>(null);
  const steps = ["站点", "数据库", "管理员", "完成"];
  const update = <K extends keyof SetupRequest>(key: K, value: SetupRequest[K]) => setForm((current) => ({ ...current, [key]: value }));
  const next = () => { setError(""); setStep((value) => Math.min(value + 1, 2)); };
  const testDatabase = async () => {
    setBusy(true); setError("");
    try { await request("/api/setup/test-db", { method: "POST", body: json({ database: form.database }) }); next(); }
    catch (nextError) { setError(message(nextError)); }
    finally { setBusy(false); }
  };
  const install = async () => {
    setBusy(true); setError("");
    try { const value = await request<SetupResult>("/api/setup/install", { method: "POST", body: json(form) }); setResult(value); setStep(3); }
    catch (nextError) { setError(message(nextError)); }
    finally { setBusy(false); }
  };
  return <main className="setup-shell">
    <aside className="setup-sidebar"><div className="setup-brand"><span><Monitor /></span><div><strong>DeskPatrol</strong><small>服务初始化</small></div></div><ol>{steps.map((label, index) => <li className={index === step ? "active" : index < step ? "done" : ""} key={label}><i>{index < step ? "✓" : index + 1}</i><span>{label}</span></li>)}</ol><p>所有配置将写入 Linux 本机，安装完成后 Setup 接口会自动锁定。</p></aside>
    <section className="setup-content"><div className="setup-form">
      {step === 0 ? <><PageTitle title="配置服务入口" description="填写管理员访问地址、Agent 连接端口和本地持久化位置。" /><div className="form-grid"><Field label="管理端 HTTPS 地址" value={form.publicUrl} onChange={(event) => update("publicUrl", event.target.value)} placeholder="https://monitor.example.com" /><Field label="Agent WSS 公网端口" type="number" value={form.agentPublicPort} onChange={(event) => update("agentPublicPort", Number(event.target.value))} /><Field label="持久化目录" value={form.storageDir} onChange={(event) => update("storageDir", event.target.value)} /><Field label="GitHub 仓库" value={form.githubRepository} onChange={(event) => update("githubRepository", event.target.value)} placeholder="owner/repository" /></div><SetupActions><Button tone="primary" onClick={next}>继续</Button></SetupActions></> : null}
      {step === 1 ? <><PageTitle title="连接 PostgreSQL" description="默认连接本机 deskpatrol 数据库。" /><div className="database-simple"><Field autoComplete="current-password" label="数据库密码" hint="本机 PostgreSQL 使用免密认证时可以留空。" type="password" value={form.database.password} onChange={(event) => update("database", { ...form.database, password: event.target.value })} /><div className="database-target"><Server /><span><strong>{form.database.user}@{form.database.host}:{form.database.port}</strong><small>{form.database.name} · SSL {form.database.sslMode === "disable" ? "关闭" : form.database.sslMode}</small></span></div><label className="advanced-toggle"><input checked={databaseAdvanced} type="checkbox" onChange={(event) => setDatabaseAdvanced(event.target.checked)} /><span>高级配置</span></label></div>{databaseAdvanced ? <div className="form-grid database-advanced"><Field label="主机" value={form.database.host} onChange={(event) => update("database", { ...form.database, host: event.target.value })} /><Field label="端口" type="number" value={form.database.port} onChange={(event) => update("database", { ...form.database, port: Number(event.target.value) })} /><Field label="数据库" value={form.database.name} onChange={(event) => update("database", { ...form.database, name: event.target.value })} /><Field label="用户" value={form.database.user} onChange={(event) => update("database", { ...form.database, user: event.target.value })} /><SelectField label="SSL 模式" value={form.database.sslMode} onChange={(event) => update("database", { ...form.database, sslMode: event.target.value as SetupRequest["database"]["sslMode"] })}><option value="disable">disable</option><option value="require">require</option><option value="verify-full">verify-full</option></SelectField></div> : null}<SetupActions><Button onClick={() => setStep(0)}>返回</Button><Button disabled={busy} icon={busy ? <LoaderCircle className="spin" size={16} /> : <Server size={16} />} tone="primary" onClick={() => void testDatabase()}>{busy ? "正在连接" : "测试并继续"}</Button></SetupActions></> : null}
      {step === 2 ? <><PageTitle title="创建超级管理员" description="设置用于进入管理端的账号和密码。密码至少 6 位。" /><div className="form-grid one-column"><Field autoComplete="username" label="管理员账号" value={form.admin.loginName} onChange={(event) => update("admin", { ...form.admin, loginName: event.target.value })} /><Field autoComplete="new-password" label="管理员密码" minLength={6} type="password" value={form.admin.password} onChange={(event) => update("admin", { ...form.admin, password: event.target.value })} hint="长度 6-128。" /></div><SetupActions><Button onClick={() => setStep(1)}>返回</Button><Button disabled={busy} icon={busy ? <LoaderCircle className="spin" size={16} /> : <ShieldCheck size={16} />} tone="primary" onClick={() => void install()}>{busy ? "正在初始化" : "完成安装"}</Button></SetupActions></> : null}
      {step === 3 && result ? <><PageTitle title="服务初始化完成" description="管理员账号已经创建，可以进入管理端。" /><section className="setup-result"><ShieldCheck /><div><strong>DeskPatrol 已就绪</strong><small>Runtime 诊断访问口令将在创建诊断连接时单独生成。</small></div></section><SetupActions><Button tone="primary" onClick={onComplete}>进入登录</Button></SetupActions></> : null}
      {error ? <p className="form-error" role="alert">{error}</p> : null}
    </div></section>
  </main>;
}

function SetupActions({ children }: { children: ReactNode }) { return <div className="setup-actions">{children}</div>; }
function PageTitle({ title, description }: { title: string; description: string }) { return <header className="page-title"><h1>{title}</h1><p>{description}</p></header>; }

function LoginPage({ onLogin }: { onLogin: (admin: Administrator) => void }) {
  const [loginName, setLoginName] = useState("admin"); const [password, setPassword] = useState(""); const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  const submit = async (event: FormEvent) => { event.preventDefault(); setBusy(true); setError(""); try { onLogin(await login(loginName, password)); } catch (next) { setError(message(next)); } finally { setBusy(false); } };
  return <main className="login-shell"><form className="login-panel" onSubmit={(event) => void submit(event)}><div className="login-brand"><span><Monitor /></span><div><strong>DeskPatrol</strong><small>设备监控桌面</small></div></div><h1>管理员登录</h1><p>输入超级管理员账号和密码。</p><Field autoComplete="username" label="账号" value={loginName} onChange={(event) => setLoginName(event.target.value)} /><Field autoComplete="current-password" label="密码" type="password" value={password} onChange={(event) => setPassword(event.target.value)} />{error ? <p className="form-error">{error}</p> : null}<Button disabled={busy || !loginName.trim() || !password} tone="primary" type="submit">{busy ? "正在验证" : "登录"}</Button></form><section className="login-context"><div className="login-grid-preview">{Array.from({ length: 9 }, (_, index) => <i key={index}><Monitor /></i>)}</div><p>仅显示已授权设备的当前桌面，不保存桌面帧。</p></section></main>;
}

function AdminShell({ administrator, onLogout }: { administrator: Administrator; onLogout: () => void }) {
  const [page, setPage] = useState<Page>("wall");
  const navigation: Array<[Page, string, ReactNode]> = [["wall", "监控墙", <LayoutDashboard />], ["devices", "设备", <Monitor />], ["codes", "激活码", <KeyRound />], ["downloads", "下载中心", <Download />], ["diagnostics", "Runtime 诊断", <Activity />], ["audit", "审计日志", <List />], ["settings", "系统设置", <Settings />]];
  return <div className="admin-shell"><aside className="sidebar"><div className="sidebar-brand"><span><Monitor /></span><strong>DeskPatrol</strong></div><nav>{navigation.map(([key, label, icon]) => <button className={page === key ? "active" : ""} key={key} onClick={() => setPage(key)}>{icon}<span>{label}</span></button>)}</nav><div className="sidebar-account"><div><strong>{administrator.loginName}</strong><small>超级管理员</small></div><IconButton label="退出登录" onClick={onLogout}><LogOut /></IconButton></div></aside><main className="workspace">{page === "wall" ? <WallPage /> : null}{page === "devices" ? <DevicesPage /> : null}{page === "codes" ? <CodesPage /> : null}{page === "downloads" ? <DownloadsPage /> : null}{page === "diagnostics" ? <DiagnosticsPage /> : null}{page === "audit" ? <AuditPage /> : null}{page === "settings" ? <SettingsPage /> : null}</main></div>;
}

function WorkspaceHeader({ title, description, actions }: { title: string; description: string; actions?: ReactNode }) { return <header className="workspace-header"><div><h1>{title}</h1><p>{description}</p></div><div className="header-actions">{actions}</div></header>; }

function useDevices() {
  const [devices, setDevices] = useState<Device[]>([]); const [loading, setLoading] = useState(true); const [error, setError] = useState("");
  const refresh = async () => { setLoading(true); setError(""); try { setDevices(await request<Device[]>("/api/v1/devices")); } catch (next) { setError(message(next)); } finally { setLoading(false); } };
  useEffect(() => { void refresh(); }, []);
  return { devices, loading, error, refresh };
}

const tileCounts = [1, 4, 9, 16] as const;

function useImmersiveToolbar(immersive: boolean, exit: () => void) {
  const [visible, setVisible] = useState(false);
  const controller = useRef<ReturnType<typeof createToolbarAutoHideController> | null>(null);
  if (!controller.current) controller.current = createToolbarAutoHideController(setVisible);
  const show = useCallback(() => controller.current!.show(), []);
  const scheduleHide = useCallback(() => controller.current!.scheduleHide(), []);

  useEffect(() => {
    if (!immersive) { controller.current!.cancelHide(); setVisible(false); return; }
    show();
    scheduleHide();
    const onKeyDown = (event: KeyboardEvent) => { if (isImmersionExitKey(event.key)) exit(); };
    window.addEventListener("keydown", onKeyDown);
    return () => { controller.current!.cancelHide(); window.removeEventListener("keydown", onKeyDown); };
  }, [exit, immersive, scheduleHide, show]);

  return { visible, show, scheduleHide };
}

function LayoutSelector({ layout, busy, onChange }: { layout: WallLayout; busy: boolean; onChange: (value: 1 | 4 | 9 | 16) => void }) {
  return <div className="segmented" aria-label="宫格数量">{tileCounts.map((value) => <button aria-pressed={layout.tileCount === value} className={layout.tileCount === value ? "active" : ""} disabled={busy} key={value} onClick={() => onChange(value)}>{value}</button>)}</div>;
}

function WallPage() {
  const { devices, loading, error, refresh } = useDevices(); const [layout, setLayout] = useState<WallLayout>({ tileCount: 9, deviceOrder: [] }); const [layoutBusy, setLayoutBusy] = useState(true); const [layoutError, setLayoutError] = useState(""); const [dragged, setDragged] = useState(""); const [tickets, setTickets] = useState<Record<string, { url: string; expiresAt: string; viewOnly: true }>>({}); const [ticketErrors, setTicketErrors] = useState<Record<string, string>>({}); const [selected, setSelected] = useState(""); const [immersive, setImmersive] = useState(false);
  const exitImmersive = useCallback(() => setImmersive(false), []);
  const toolbar = useImmersiveToolbar(immersive, exitImmersive);
  useEffect(() => { request<WallLayout>("/api/v1/wall-layout").then(setLayout).catch((next) => setLayoutError(message(next))).finally(() => setLayoutBusy(false)); }, []);
  const ordered = useMemo(() => [...devices].sort((a, b) => { const ai = layout.deviceOrder.indexOf(a.id); const bi = layout.deviceOrder.indexOf(b.id); return (ai < 0 ? 999 : ai) - (bi < 0 ? 999 : bi); }), [devices, layout.deviceOrder]);
  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      const online = ordered.filter((device) => device.status === "online" && (ordered.indexOf(device) < layout.tileCount || device.id === selected));
      const values = await Promise.all(online.map(async (device) => {
        try { return { deviceId: device.id, ticket: await request<{ url: string; expiresAt: string; viewOnly: true }>(`/api/v1/devices/${device.id}/desktop-ticket`, { method: "POST" }) }; }
        catch (next) { return { deviceId: device.id, error: message(next) }; }
      }));
      if (!cancelled) {
        setTickets(Object.fromEntries(values.flatMap((value) => value.ticket ? [[value.deviceId, value.ticket]] : [])));
        setTicketErrors(Object.fromEntries(values.flatMap((value) => value.error ? [[value.deviceId, value.error]] : [])));
      }
    };
    void load(); const timer = window.setInterval(() => void load(), 4 * 60 * 1000);
    return () => { cancelled = true; window.clearInterval(timer); };
  }, [ordered, layout.tileCount, selected]);
  const saveTileCount = async (value: 1 | 4 | 9 | 16) => { const next = { ...layout, tileCount: value }; setLayout(next); setLayoutError(""); try { await request("/api/v1/wall-layout", { method: "PUT", body: json(next) }); } catch (nextError) { setLayoutError(message(nextError)); } };
  const drop = async (target: string) => { if (!dragged || dragged === target) return; const order = moveDeviceOrder(ordered.map((item) => item.id), dragged, target); const next = { ...layout, deviceOrder: order }; setLayout(next); setDragged(""); setLayoutError(""); try { await request("/api/v1/wall-layout", { method: "PUT", body: json(next) }); } catch (nextError) { setLayoutError(message(nextError)); } };
  const selectedDevice = ordered.find((device) => device.id === selected);
  const tiles = loading ? <ContentLoading /> : ordered.length === 0 ? <EmptyState icon={<Grid2X2 />} title="还没有已激活设备" description="创建设备激活码并完成 Windows 客户端激活后，设备会出现在监控墙。" /> : <section className={`monitor-grid monitor-grid--${layout.tileCount}`}>{ordered.slice(0, layout.tileCount).map((device) => <article aria-label={`查看 ${device.name}`} className="monitor-tile" draggable key={device.id} onClick={() => setSelected(device.id)} onDragStart={() => setDragged(device.id)} onDragOver={(event) => event.preventDefault()} onDrop={() => void drop(device.id)} onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); setSelected(device.id); } }} tabIndex={0}><DesktopFrame device={device} error={ticketErrors[device.id]} ticket={tickets[device.id]} /><footer><div><strong>{device.name}</strong><small>{device.screenCount} 个显示器 · {device.architecture}</small></div><StatusBadge status={device.status === "online" ? "ok" : device.status === "locked" ? "warn" : "muted"}>{statusText(device.status)}</StatusBadge></footer></article>)}</section>;
  if (immersive) return <section className={`wall-immersive${selectedDevice ? " wall-immersive--detail" : ""}`}>
    <button aria-label="显示监控墙操作栏" className="wall-toolbar-sensor" onClick={toolbar.show} onPointerEnter={toolbar.show} type="button" />
    <header className={`wall-immersive-toolbar${toolbar.visible ? " visible" : ""}`} onPointerEnter={toolbar.show} onPointerLeave={toolbar.scheduleHide}>
      <div>{selectedDevice ? <Button icon={<ChevronLeft size={16} />} onClick={() => setSelected("")}>返回监控墙</Button> : <strong>监控墙</strong>}</div>
      <div className="wall-immersive-actions">{selectedDevice ? null : <LayoutSelector busy={layoutBusy} layout={layout} onChange={(value) => void saveTileCount(value)} />}<ThemeControl compact /><IconButton label="刷新设备" onClick={() => void refresh()}><RefreshCw /></IconButton><Button onClick={exitImmersive}>退出沉浸</Button></div>
    </header>
    {selectedDevice ? <DesktopDetail device={selectedDevice} error={ticketErrors[selectedDevice.id]} immersive ticket={tickets[selectedDevice.id]} onBack={() => setSelected("")} /> : <div className="wall-immersive-content">{error || layoutError ? <InlineError value={error || layoutError} /> : null}{tiles}</div>}
  </section>;
  if (selectedDevice) return <DesktopDetail device={selectedDevice} error={ticketErrors[selectedDevice.id]} ticket={tickets[selectedDevice.id]} onBack={() => setSelected("")} />;
  return <><WorkspaceHeader title="监控墙" description="设备主屏低帧预览，不保存桌面内容。" actions={<><LayoutSelector busy={layoutBusy} layout={layout} onChange={(value) => void saveTileCount(value)} /><IconButton label="刷新设备" onClick={() => void refresh()}><RefreshCw /></IconButton><Button icon={<Expand size={16} />} onClick={() => setImmersive(true)}>沉浸显示</Button></>} />{error || layoutError ? <InlineError value={error || layoutError} /> : null}{tiles}</>;
}

function DesktopFrame({ device, ticket, error }: { device: Device; ticket?: { url: string }; error?: string }) {
  const [viewerError, setViewerError] = useState("");
  const loaded = (frame: HTMLIFrameElement) => {
    try { configureMeshViewer(frame, "wall"); setViewerError(""); }
    catch (error) { setViewerError(message(error)); }
  };
  return <div className="desktop-frame">{ticket ? <iframe aria-label={`${device.name} 桌面预览`} onLoad={(event) => loaded(event.currentTarget)} sandbox="allow-scripts allow-same-origin" src={ticket.url} /> : <><Monitor /><span>{error || (device.status === "locked" ? "设备已锁屏" : device.status === "online" ? "正在连接桌面" : "设备离线")}</span></>}{viewerError ? <span role="alert">{viewerError}</span> : null}</div>;
}

function DesktopDetail({ device, ticket, error, onBack, immersive = false }: { device: Device; ticket?: { url: string }; error?: string; onBack: () => void; immersive?: boolean }) {
  const [viewerError, setViewerError] = useState("");
  const fullscreen = () => { const element = document.querySelector<HTMLElement>(".single-desktop"); if (element?.requestFullscreen) void element.requestFullscreen(); };
  const loaded = (frame: HTMLIFrameElement) => {
    try { configureMeshViewer(frame, "detail"); setViewerError(""); }
    catch (error) { setViewerError(message(error)); }
  };
  return <>{immersive ? null : <WorkspaceHeader title={device.name} description={`${device.screenCount} 个显示器 · ${device.architecture}`} actions={<><Button icon={<ChevronLeft size={16} />} onClick={onBack}>返回监控墙</Button><IconButton label="全屏" onClick={fullscreen}><Expand /></IconButton></>} />}<section className={`single-desktop${immersive ? " single-desktop--immersive" : ""}`}>{ticket ? <iframe aria-label={`${device.name} 桌面`} allowFullScreen onLoad={(event) => loaded(event.currentTarget)} sandbox="allow-scripts allow-same-origin" src={ticket.url} /> : <div><Monitor /><StatusBadge status={error ? "error" : device.status === "online" ? "warn" : "muted"}>{error || (device.status === "online" ? "正在创建桌面会话" : statusText(device.status))}</StatusBadge></div>}{viewerError ? <p className="desktop-viewer-error" role="alert">{viewerError}</p> : null}</section></>;
}

function configureMeshViewer(frame: HTMLIFrameElement, mode: "wall" | "detail") {
  const viewer = frame.contentWindow as MeshSharingWindow | null;
  if (!viewer?.desktopsettings || typeof viewer.connectDesktop !== "function") throw new Error("MeshCentral 桌面查看器初始化失败");
  viewer.desktopsettings.framerate = mode === "wall" ? 1000 : 333;
  const header = viewer.document.getElementById("deskarea1");
  const viewport = viewer.document.getElementById("deskarea3x");
  const footer = viewer.document.getElementById("deskarea4");
  const screenshot = viewer.document.getElementById("DeskSaveImageButton");
  if (!header || !viewport || !footer || !screenshot) throw new Error("MeshCentral 桌面查看器版本不兼容");
  header.style.display = "none";
  screenshot.style.display = "none";
  viewport.style.maxHeight = mode === "wall" ? "100vh" : "calc(100vh - 28px)";
  viewport.style.height = mode === "wall" ? "100vh" : "calc(100vh - 28px)";
  if (mode === "wall") footer.style.display = "none";
  viewer.connectDesktop(null, 1);
}

function DevicesPage() {
  const { devices, loading, error, refresh } = useDevices();
  return <><WorkspaceHeader title="设备" description="查看 MeshAgent 注册、在线状态和显示器信息。" actions={<><div className="search"><Search /><input aria-label="搜索设备" placeholder="搜索设备" /></div><IconButton label="刷新" onClick={() => void refresh()}><RefreshCw /></IconButton></>} />{error ? <InlineError value={error} /> : null}{loading ? <ContentLoading /> : devices.length === 0 ? <EmptyState icon={<Monitor />} title="暂无设备" description="客户端完成激活并首次连接 MeshCentral 后才会创建正式设备记录。" /> : <div className="table-wrap"><table><thead><tr><th>设备</th><th>状态</th><th>架构</th><th>显示器</th><th>最近在线</th></tr></thead><tbody>{devices.map((device) => <tr key={device.id}><td><strong>{device.name}</strong><small>{device.id}</small></td><td><StatusBadge status={device.status === "online" ? "ok" : "muted"}>{statusText(device.status)}</StatusBadge></td><td>{device.architecture}</td><td>{device.screenCount}</td><td>{formatDate(device.lastSeenAt)}</td></tr>)}</tbody></table></div>}</>;
}

function CodesPage() {
  const [items, setItems] = useState<ActivationCode[]>([]); const [open, setOpen] = useState(false); const [label, setLabel] = useState(""); const [days, setDays] = useState(7); const [created, setCreated] = useState<ActivationCodeCreated | null>(null); const [error, setError] = useState("");
  const refresh = () => request<ActivationCode[]>("/api/v1/activation-codes").then(setItems).catch((next) => setError(message(next)));
  useEffect(() => { void refresh(); }, []);
  const create = async () => { try { const value = await request<ActivationCodeCreated>("/api/v1/activation-codes", { method: "POST", body: json({ label, days }) }); setCreated(value); await refresh(); } catch (next) { setError(message(next)); } };
  return <><WorkspaceHeader title="激活码" description="激活码一机一次，默认 7 天有效，服务端只保存摘要。" actions={<Button icon={<Plus size={16} />} tone="primary" onClick={() => { setOpen(true); setCreated(null); }}>创建激活码</Button>} />{error ? <InlineError value={error} /> : null}{items.length === 0 ? <EmptyState action={<Button icon={<Plus size={16} />} onClick={() => setOpen(true)}>创建第一枚激活码</Button>} icon={<KeyRound />} title="暂无激活码" description="明文只在创建成功时显示一次。重新激活设备必须使用新码。" /> : <div className="table-wrap"><table><thead><tr><th>备注</th><th>状态</th><th>有效期</th><th>创建时间</th></tr></thead><tbody>{items.map((item) => <tr key={item.id}><td><strong>{item.label || "未备注"}</strong><small>{item.id}</small></td><td><StatusBadge status={item.usedAt ? "muted" : new Date(item.expiresAt) < new Date() ? "error" : "ok"}>{item.usedAt ? "已使用" : new Date(item.expiresAt) < new Date() ? "已过期" : "可使用"}</StatusBadge></td><td>{formatDate(item.expiresAt)}</td><td>{formatDate(item.createdAt)}</td></tr>)}</tbody></table></div>}<Dialog open={open} title={created ? "激活码已创建" : "创建激活码"} onClose={() => setOpen(false)}>{created ? <div className="dialog-body"><p className="one-time-note"><LockKeyhole />此明文只显示一次，请立即保存。</p><div className="secret-line"><code>{created.code}</code><IconButton label="复制激活码" onClick={() => void navigator.clipboard.writeText(created.code)}><Copy /></IconButton></div><p>有效至 {formatDate(created.expiresAt)}</p><div className="dialog-actions"><Button tone="primary" onClick={() => setOpen(false)}>完成</Button></div></div> : <div className="dialog-body"><Field label="设备备注" value={label} onChange={(event) => setLabel(event.target.value)} placeholder="例如：财务室 01" /><Field label="有效天数" max={30} min={1} type="number" value={days} onChange={(event) => setDays(Number(event.target.value))} /><div className="dialog-actions"><Button onClick={() => setOpen(false)}>取消</Button><Button tone="primary" onClick={() => void create()}>创建</Button></div></div>}</Dialog></>;
}

function DownloadsPage() {
  const [items, setItems] = useState<DownloadArtifact[]>([]); const [jobs, setJobs] = useState<ReleaseJob[]>([]); const [version, setVersion] = useState("0.1.0"); const [error, setError] = useState("");
  const refresh = async () => { try { const [artifacts, releaseJobs] = await Promise.all([request<DownloadArtifact[]>("/api/v1/downloads"), request<ReleaseJob[]>("/api/v1/releases/jobs")]); setItems(artifacts); setJobs(releaseJobs); } catch (next) { setError(message(next)); } };
  useEffect(() => { void refresh(); }, []);
  const sync = async () => { try { await request("/api/v1/releases/sync", { method: "POST", body: json({ version }) }); await refresh(); } catch (next) { setError(message(next)); } };
  return <><WorkspaceHeader title="下载中心" description="安装包由 Linux 后台校验并从本地提供，设备无需访问 GitHub。" actions={<div className="release-sync"><input aria-label="Release 版本" value={version} onChange={(event) => setVersion(event.target.value)} /><Button icon={<RefreshCw size={16} />} onClick={() => void sync()}>后台同步</Button></div>} />{error ? <InlineError value={error} /> : null}{jobs[0] ? <div className="release-job"><StatusBadge status={jobs[0].status === "ready" ? "ok" : jobs[0].status === "failed" ? "error" : "warn"}>v{jobs[0].version} {jobs[0].status}</StatusBadge><span>{jobs[0].total > 0 ? `${Math.min(100, Math.round(jobs[0].progress / jobs[0].total * 100))}%` : "等待 manifest"}</span>{jobs[0].error ? <small>{jobs[0].error}</small> : null}</div> : null}{items.length === 0 ? <EmptyState icon={<Download />} title="安装包尚未就绪" description="Setup 完成后的 Release 同步任务会下载并校验 Windows x64、ARM64 安装包。" /> : <section className="download-list">{items.map((item) => <article key={item.filename}><div className="file-icon"><AppWindow /></div><div><strong>{item.filename}</strong><small>v{item.version} · {item.architecture} · {formatBytes(item.size)}</small><code>SHA-256 {item.sha256}</code></div><Button icon={<Download size={16} />} onClick={() => { window.location.href = `/api/v1/downloads/${encodeURIComponent(item.filename)}`; }}>下载</Button></article>)}</section>}</>;
}

function DiagnosticsPage() {
  const { devices, loading } = useDevices(); const [selected, setSelected] = useState(""); const [session, setSession] = useState<DebugSession | null>(null); const [error, setError] = useState(""); const [copied, setCopied] = useState(false);
  const [script, setScript] = useState(""); const [timeoutSeconds, setTimeoutSeconds] = useState(30); const [running, setRunning] = useState(false); const [result, setResult] = useState<{ stdout: string; stderr: string; exitCode: number; outputTruncated: boolean } | null>(null);
  const create = async () => { setError(""); setCopied(false); try { setSession(await request<DebugSession>("/api/v1/runtime-debug/sessions", { method: "POST", body: json({ deviceId: selected }) })); } catch (next) { setError(message(next)); } };
  const close = async () => { if (!session) return; try { await request(`/api/v1/runtime-debug/sessions/${session.id}`, { method: "DELETE" }); setSession(null); setResult(null); setScript(""); setCopied(false); } catch (next) { setError(message(next)); } };
  const copyConnection = async () => {
    if (!session) return;
    const baseURL = `${window.location.origin}/api/v1/runtime-debug/sessions/${session.id}`;
    const device = devices.find((item) => item.id === session.deviceId);
    const content = ["DeskPatrol Runtime 诊断连接", `设备：${device?.name || session.deviceId}`, `有效期：${formatDate(session.expiresAt)}`, `会话地址：${baseURL}`, `访问口令：${session.token}`, "认证方式：Authorization: Bearer <访问口令>", `预置检查：POST ${baseURL}/inspect，JSON 为 {\"kind\":\"status\"}`, `PowerShell：POST ${baseURL}/powershell，JSON 为 {\"script\":\"...\",\"timeoutSeconds\":30}`].join("\n");
    try { await navigator.clipboard.writeText(content); setCopied(true); setError(""); }
    catch (next) { setError(message(next)); }
  };
  const run = async () => { if (!session) return; setRunning(true); setError(""); setResult(null); try { setResult(await request(`/api/v1/runtime-debug/sessions/${session.id}/powershell`, { method: "POST", body: json({ script, timeoutSeconds }) })); } catch (next) { setError(message(next)); } finally { setRunning(false); } };
  const inspect = async (kind: string) => { if (!session) return; setRunning(true); setError(""); setResult(null); try { const value = await request<{ stdout: string; stderr: string; exitCode: number; outputTruncated: boolean }>(`/api/v1/runtime-debug/sessions/${session.id}/inspect`, { method: "POST", body: json({ kind }) }); setResult(value); if (kind === "package") downloadDiagnosticPackage(selected, value); } catch (next) { setError(message(next)); } finally { setRunning(false); } };
  const inspections: Array<[string, string]> = [["status", "系统"], ["processes", "进程"], ["services", "服务"], ["displays", "显示器"], ["network", "网络"], ["events", "事件"], ["meshagent", "MeshAgent"], ["package", "诊断包"]];
  return <><WorkspaceHeader title="Runtime 诊断" description="创建 15 分钟限时连接后，可将访问口令复制给 Codex；全部操作写入审计。" />{error ? <InlineError value={error} /> : null}<section className="diagnostic-layout"><div className="diagnostic-controls"><h2>创建诊断连接</h2><p>诊断流量沿设备既有出站 WSS 通道传输，不开放设备入站端口。</p><SelectField disabled={loading || Boolean(session)} label="目标设备" value={selected} onChange={(event) => setSelected(event.target.value)}><option value="">请选择设备</option>{devices.map((device) => <option key={device.id} value={device.id}>{device.name}</option>)}</SelectField>{session ? <><div className="session-active"><StatusBadge status="ok">连接已开启</StatusBadge><span>诊断访问口令（仅本次显示）</span><code className="session-token">{session.token}</code><span>到期时间 {formatDate(session.expiresAt)}</span></div><Button icon={<Copy size={16} />} tone="primary" onClick={() => void copyConnection()}>{copied ? "已复制给 Codex" : "复制给 Codex"}</Button><Button tone="danger" onClick={() => void close()}>立即关闭</Button></> : <Button disabled={!selected} icon={<Terminal size={16} />} tone="primary" onClick={() => void create()}>创建 15 分钟诊断连接</Button>}</div><div className="diagnostic-surface"><div className="inspection-bar">{inspections.map(([kind, label]) => <Button disabled={!session || running} key={kind} onClick={() => void inspect(kind)}>{label}</Button>)}</div>{session ? <div className="powershell-console"><label><span>PowerShell 脚本</span><textarea maxLength={32 * 1024} spellCheck={false} value={script} onChange={(event) => setScript(event.target.value)} /></label><div className="powershell-actions"><Field label="超时（秒）" max={120} min={1} type="number" value={timeoutSeconds} onChange={(event) => setTimeoutSeconds(Number(event.target.value))} /><Button disabled={running || !script.trim() || timeoutSeconds < 1 || timeoutSeconds > 120} icon={running ? <LoaderCircle className="spin" size={16} /> : <Terminal size={16} />} tone="primary" onClick={() => void run()}>{running ? "正在执行" : "执行"}</Button></div>{result ? <section className="command-result"><header><StatusBadge status={result.exitCode === 0 ? "ok" : "error"}>退出码 {result.exitCode}</StatusBadge>{result.outputTruncated ? <StatusBadge status="warn">输出已截断</StatusBadge> : null}</header><div><span>stdout</span><pre>{result.stdout || "（空）"}</pre></div><div><span>stderr</span><pre>{result.stderr || "（空）"}</pre></div></section> : null}</div> : <EmptyState icon={<ShieldCheck />} title="尚未创建诊断连接" description="选择设备并创建限时连接后，可在管理端操作或把连接信息复制给 Codex。" />}</div></section></>;
}

function downloadDiagnosticPackage(deviceId: string, result: { stdout: string; stderr: string; exitCode: number; outputTruncated: boolean }) {
  const content = JSON.stringify({ schemaVersion: 1, deviceId, capturedAt: new Date().toISOString(), ...result }, null, 2);
  const url = URL.createObjectURL(new Blob([content], { type: "application/json" }));
  const anchor = document.createElement("a"); anchor.href = url; anchor.download = `deskpatrol-diagnostics-${deviceId}.json`; anchor.click(); URL.revokeObjectURL(url);
}

function AuditPage() {
  const [items, setItems] = useState<AuditLog[]>([]); const [loading, setLoading] = useState(true); const [error, setError] = useState("");
  const refresh = async () => { setLoading(true); setError(""); try { setItems(await request<AuditLog[]>("/api/v1/audit-logs")); } catch (next) { setError(message(next)); } finally { setLoading(false); } };
  useEffect(() => { void refresh(); }, []);
  return <><WorkspaceHeader title="审计日志" description="仅保存诊断操作元数据，不保存 PowerShell 文本和命令输出。" actions={<IconButton label="刷新审计日志" onClick={() => void refresh()}><RefreshCw /></IconButton>} />{error ? <InlineError value={error} /> : null}{loading ? <ContentLoading /> : items.length === 0 ? <EmptyState icon={<Clipboard />} title="暂无诊断审计" description="开启 Runtime 诊断会话并执行操作后会生成审计记录。" /> : <div className="table-wrap"><table><thead><tr><th>时间</th><th>管理员</th><th>设备</th><th>操作</th><th>结果</th><th>脚本 SHA-256</th></tr></thead><tbody>{items.map((item) => <tr key={item.id}><td>{formatDate(item.createdAt)}</td><td>{item.administrator}</td><td><strong>{item.deviceName}</strong><small>{item.deviceId}</small></td><td>{item.operation}<small>{item.durationMs} ms · 会话 {item.sessionId}</small></td><td><StatusBadge status={item.exitCode === 0 ? "ok" : "error"}>{item.exitCode === null ? "未完成" : `退出码 ${item.exitCode}`}</StatusBadge>{item.outputTruncated ? <small>输出已截断</small> : null}</td><td><code className="audit-digest">{item.scriptSha256}</code></td></tr>)}</tbody></table></div>}</>;
}

function SettingsPage() { return <><WorkspaceHeader title="系统设置" description="服务地址、Agent 端口、存储和 Release 源在 Setup 时固定。" /><section className="settings-band"><div><Server /><span><strong>运行模式</strong><small>Linux systemd · PostgreSQL · 外部 Nginx</small></span></div><StatusBadge status="ok">配置已锁定</StatusBadge></section><section className="settings-band"><div><ShieldCheck /><span><strong>安全策略</strong><small>诊断连接使用限时口令 · 无自动更新 · 不保存桌面帧</small></span></div><StatusBadge status="ok">已启用</StatusBadge></section><section className="settings-band"><div><Monitor /><span><strong>界面主题</strong><small>可选择浅色、深色或跟随系统。</small></span></div><ThemeControl /></section></>; }

function ContentLoading() { return <div className="content-loading"><LoaderCircle className="spin" /><span>正在读取数据</span></div>; }
function InlineError({ value }: { value: string }) { return <p className="inline-error">{value}</p>; }
function statusText(status: string) { return ({ online: "在线", offline: "离线", locked: "锁屏", pending: "待连接" } as Record<string, string>)[status] || status; }
function message(error: unknown) { return error instanceof Error ? error.message : String(error); }
