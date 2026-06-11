import { useState } from "react";
import { NavLink, Outlet } from "react-router-dom";
import { useAppData } from "../context/AppDataContext";
import { useAutopilot } from "../context/AutopilotContext";
import { useSettings } from "../context/SettingsContext";
import { useToast } from "../context/ToastContext";
import { agoLabel } from "../hooks/usePolling";
import { AutomationBanner } from "../components/AutomationBanner";
import { DebugMenu } from "../components/DebugMenu";
import { ToastViewport } from "../components/Toast";

const NAV = [
  { to: "/", label: "Dashboard", icon: "▦", end: true },
  { to: "/problems", label: "Problems", icon: "⚠", end: false },
  { to: "/instrument", label: "Instrument", icon: "✦", end: false },
  { to: "/history", label: "History", icon: "≡", end: false },
  { to: "/settings", label: "Settings", icon: "⚙", end: false },
];

const THEME_ICON = { light: "☀", dark: "☾", system: "◐" } as const;

export function AppShell() {
  const { mock, refreshProblems, refreshTestStatus, problemsUpdatedAt, demoAppUrl, demoAppName } = useAppData();
  const { available, config, activeCount, setConfig } = useAutopilot();
  const { theme, setTheme } = useSettings();
  const toast = useToast();
  const [togglingAuto, setTogglingAuto] = useState(false);

  function cycleTheme() {
    setTheme(theme === "system" ? "light" : theme === "light" ? "dark" : "system");
  }

  async function toggleAutopatch() {
    setTogglingAuto(true);
    try {
      await setConfig({ ...config, enabled: !config.enabled });
      toast.success(config.enabled ? "Autopatch paused" : "Autopatch resumed");
    } catch (e) {
      toast.error(`Couldn't update autopatch: ${String(e)}`);
    } finally {
      setTogglingAuto(false);
    }
  }

  return (
    <div className="shell">
      <aside className="sidebar">
        <h1 className="sidebar-brand">PatchPilot</h1>
        <p className="sidebar-tag">Detect → diagnose → patch → verify</p>
        <nav className="nav">
          {NAV.map((n) => (
            <NavLink
              key={n.to}
              to={n.to}
              end={n.end}
              className={({ isActive }) => `nav-link${isActive ? " active" : ""}`}
            >
              <span className="nav-ico" aria-hidden>
                {n.icon}
              </span>
              {n.label}
            </NavLink>
          ))}
        </nav>
      </aside>

      <div className="shell-main">
        <header className="shell-topbar">
          {mock && (
            <span className="mock-badge" title="The backend is unreachable — showing demo data.">
              demo data (mock) — backend not connected
            </span>
          )}
          <div className="topbar-spacer" />
          {problemsUpdatedAt && <span className="muted topbar-updated">updated {agoLabel(problemsUpdatedAt)}</span>}
          {available && (
            <button
              className={`ghost-btn autopatch-pill${config.enabled ? " on" : " off"}`}
              onClick={toggleAutopatch}
              disabled={togglingAuto}
              title={
                config.enabled
                  ? "Autopatch is ON — click to pause (halts the active batch and queued problems)"
                  : "Autopatch is PAUSED — click to resume (open problems are picked up immediately)"
              }
            >
              {config.enabled ? `● Autopatch ON${activeCount > 0 ? ` (${activeCount} active)` : ""}` : "○ Autopatch PAUSED"}
            </button>
          )}
          <DebugMenu />
          {demoAppUrl && (
            <a
              className="ghost-btn"
              href={demoAppUrl}
              target="_blank"
              rel="noreferrer"
              title={`Open ${demoAppName ?? "the deployed app"} in a new tab`}
            >
              ↗ Open app
            </a>
          )}
          <button
            className="ghost-btn"
            onClick={() => {
              refreshProblems();
              refreshTestStatus();
            }}
            title="Refresh data"
          >
            ↻ Refresh
          </button>
          <button
            className="ghost-btn"
            onClick={cycleTheme}
            title={`Theme: ${theme} (click to change)`}
            aria-label={`Theme: ${theme}`}
          >
            {THEME_ICON[theme]} {theme}
          </button>
        </header>

        <AutomationBanner />

        <main className="shell-content">
          <Outlet />
        </main>
      </div>

      <ToastViewport />
    </div>
  );
}
