import type { ButtonHTMLAttributes, InputHTMLAttributes, ReactNode } from "react";
import { useTheme, type ThemeMode } from "@deskpatrol/design-tokens";

export function Button({ tone = "default", icon, children, className = "", ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { tone?: "default" | "primary" | "danger"; icon?: ReactNode }) {
  return <button className={`button button--${tone} ${className}`} {...props}>{icon}{children}</button>;
}

export function IconButton({ label, children, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { label: string; children: ReactNode }) {
  return <button aria-label={label} className="icon-button" title={label} {...props}>{children}</button>;
}

export function Field({ label, hint, ...props }: InputHTMLAttributes<HTMLInputElement> & { label: string; hint?: string }) {
  return <label className="field"><span>{label}</span><input {...props} />{hint ? <small>{hint}</small> : null}</label>;
}

export function SelectField({ label, children, ...props }: React.SelectHTMLAttributes<HTMLSelectElement> & { label: string; children: ReactNode }) {
  return <label className="field"><span>{label}</span><select {...props}>{children}</select></label>;
}

export function StatusBadge({ status, children }: { status: "ok" | "warn" | "error" | "muted"; children: ReactNode }) {
  return <span className={`status status--${status}`}><i />{children}</span>;
}

const themeOptions: Array<{ value: ThemeMode; label: string }> = [
  { value: "light", label: "浅色" },
  { value: "dark", label: "深色" },
  { value: "system", label: "系统" },
];

export function ThemeControl({ compact = false }: { compact?: boolean }) {
  const { theme, setTheme } = useTheme();
  return <div aria-label="界面主题" className={`theme-control${compact ? " theme-control--compact" : ""}`} role="group">{themeOptions.map((option) => <button aria-pressed={theme === option.value} className={theme === option.value ? "active" : ""} key={option.value} onClick={() => setTheme(option.value)} type="button">{option.label}</button>)}</div>;
}

export function EmptyState({ icon, title, description, action }: { icon: ReactNode; title: string; description: string; action?: ReactNode }) {
  return <div className="empty-state">{icon}<strong>{title}</strong><p>{description}</p>{action}</div>;
}

export function Dialog({ open, title, children, onClose }: { open: boolean; title: string; children: ReactNode; onClose: () => void }) {
  if (!open) return null;
  return <div className="dialog-backdrop" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}><section aria-modal="true" className="dialog" role="dialog"><header><h2>{title}</h2><button aria-label="关闭" className="dialog-close" onClick={onClose}>×</button></header>{children}</section></div>;
}
