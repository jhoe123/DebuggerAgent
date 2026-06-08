import { NavLink, Outlet } from "react-router-dom";
import { useAppData } from "../context/AppDataContext";
import { useSettings } from "../context/SettingsContext";
import { agoLabel } from "../hooks/usePolling";
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
  const { mock, refreshProblems, refreshTestStatus, problemsUpdatedAt } = useAppData();
  const { theme, setTheme } = useSettings();

  function cycleTheme() {
    setTheme(theme === "system" ? "light" : theme === "light" ? "dark" : "system");
  }

  return (
    <div className="shell">
      <aside className="sidebar">
        <h1 className="sidebar-brand">DebuggerAgent</h1>
        <p className="sidebar-tag">AI SRE · Gemini + Dynatrace</p>
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

        <main className="shell-content">
          <Outlet />
        </main>
      </div>

      <ToastViewport />
    </div>
  );
}
