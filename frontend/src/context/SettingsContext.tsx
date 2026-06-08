import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import { BACKEND_URL_KEY } from "../api";

export type ThemeMode = "light" | "dark" | "system";

export interface AutonomyDefaults {
  apply: boolean;
  test: boolean;
  build: boolean;
  deploy: boolean;
}

const THEME_KEY = "da.theme";
const AUTONOMY_KEY = "da.autonomy";

interface SettingsValue {
  theme: ThemeMode;
  setTheme: (t: ThemeMode) => void;
  resolvedTheme: "light" | "dark";
  backendUrl: string; // "" => use build-time default
  setBackendUrl: (v: string) => void;
  autonomy: AutonomyDefaults;
  setAutonomy: (a: AutonomyDefaults) => void;
}

const SettingsContext = createContext<SettingsValue | null>(null);

function loadJSON<T>(key: string, fallback: T): T {
  try {
    const v = localStorage.getItem(key);
    if (v != null) return JSON.parse(v) as T;
  } catch {
    /* ignore */
  }
  return fallback;
}

function systemPrefersDark(): boolean {
  return typeof matchMedia !== "undefined" && matchMedia("(prefers-color-scheme: dark)").matches;
}

export function SettingsProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<ThemeMode>(() => loadJSON<ThemeMode>(THEME_KEY, "system"));
  const [backendUrl, setBackendUrlState] = useState<string>(() => {
    try {
      return localStorage.getItem(BACKEND_URL_KEY) ?? "";
    } catch {
      return "";
    }
  });
  const [autonomy, setAutonomyState] = useState<AutonomyDefaults>(() =>
    loadJSON<AutonomyDefaults>(AUTONOMY_KEY, { apply: true, test: true, build: true, deploy: true }),
  );
  const [sysDark, setSysDark] = useState<boolean>(systemPrefersDark);

  useEffect(() => {
    if (typeof matchMedia === "undefined") return;
    const mq = matchMedia("(prefers-color-scheme: dark)");
    const onChange = () => setSysDark(mq.matches);
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, []);

  const resolvedTheme: "light" | "dark" = theme === "system" ? (sysDark ? "dark" : "light") : theme;

  useEffect(() => {
    document.documentElement.setAttribute("data-theme", resolvedTheme);
  }, [resolvedTheme]);

  const setTheme = (t: ThemeMode) => {
    setThemeState(t);
    try {
      localStorage.setItem(THEME_KEY, JSON.stringify(t));
    } catch {
      /* ignore */
    }
  };
  const setBackendUrl = (v: string) => {
    setBackendUrlState(v);
    try {
      if (v) localStorage.setItem(BACKEND_URL_KEY, v);
      else localStorage.removeItem(BACKEND_URL_KEY);
    } catch {
      /* ignore */
    }
  };
  const setAutonomy = (a: AutonomyDefaults) => {
    setAutonomyState(a);
    try {
      localStorage.setItem(AUTONOMY_KEY, JSON.stringify(a));
    } catch {
      /* ignore */
    }
  };

  return (
    <SettingsContext.Provider
      value={{ theme, setTheme, resolvedTheme, backendUrl, setBackendUrl, autonomy, setAutonomy }}
    >
      {children}
    </SettingsContext.Provider>
  );
}

export function useSettings(): SettingsValue {
  const v = useContext(SettingsContext);
  if (!v) throw new Error("useSettings must be used within SettingsProvider");
  return v;
}
