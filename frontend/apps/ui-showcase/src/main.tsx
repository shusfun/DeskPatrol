import { createRoot } from "react-dom/client";
import { ThemeProvider, initializeTheme } from "@deskpatrol/design-tokens";
import { Activity, Plus, RefreshCw } from "@deskpatrol/icons";
import { Button, EmptyState, Field, StatusBadge, ThemeControl } from "@deskpatrol/ui-admin";
import "@deskpatrol/ui-admin/styles.css";
import "./styles.css";

function Showcase() {
  return <main><header><div><span>DeskPatrol</span><h1>管理端组件</h1></div><ThemeControl /></header><section><h2>品牌 Token</h2><div className="token-swatches"><i className="token-primary" title="Primary" /><i className="token-accent" title="Accent" /><i className="token-success" title="Success" /><i className="token-warning" title="Warning" /><i className="token-destructive" title="Destructive" /></div></section><section><h2>操作</h2><div className="row"><Button icon={<Plus size={16} />} tone="primary">创建激活码</Button><Button icon={<RefreshCw size={16} />}>刷新</Button><Button tone="danger">关闭会话</Button></div></section><section><h2>状态</h2><div className="row"><StatusBadge status="ok">在线</StatusBadge><StatusBadge status="warn">锁屏</StatusBadge><StatusBadge status="error">失败</StatusBadge><StatusBadge status="muted">离线</StatusBadge></div></section><section><h2>输入</h2><div className="fields"><Field label="设备名称" placeholder="输入设备名称" /><Field label="诊断访问口令" readOnly value="创建诊断连接后生成" /></div></section><section><EmptyState icon={<Activity />} title="暂无诊断记录" description="诊断会话开启后会显示服务、进程和连接状态。" /></section></main>;
}
initializeTheme();
createRoot(document.getElementById("root")!).render(<ThemeProvider><Showcase /></ThemeProvider>);
