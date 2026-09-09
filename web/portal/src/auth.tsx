import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { api } from "./api";
import type { Me, PublicConfig } from "./types";

interface AuthValue {
  me: Me | null;
  config: PublicConfig | null;
  loading: boolean;
  signOut: () => Promise<void>;
  refresh: () => Promise<void>;
}

const AuthContext = createContext<AuthValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [me, setMe] = useState<Me | null>(null);
  const [config, setConfig] = useState<PublicConfig | null>(null);
  const [loading, setLoading] = useState(true);

  const refresh = useCallback(async () => {
    try {
      setMe(await api.me());
    } catch {
      setMe({ signedIn: false });
    }
  }, []);

  useEffect(() => {
    (async () => {
      const [cfg] = await Promise.allSettled([api.config(), refresh()]);
      if (cfg.status === "fulfilled") setConfig(cfg.value);
      setLoading(false);
    })();
  }, [refresh]);

  const signOut = useCallback(async () => {
    await api.logout().catch(() => {});
    setMe({ signedIn: false });
  }, []);

  const value = useMemo(() => ({ me, config, loading, signOut, refresh }), [me, config, loading, signOut, refresh]);
  return <AuthContext value={value}>{children}</AuthContext>;
}

export function useAuth(): AuthValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used inside AuthProvider");
  return ctx;
}

/* ── Google Identity Services ──────────────────────────────────────────────── */

declare global {
  interface Window {
    google?: {
      accounts: {
        id: {
          initialize(opts: { client_id: string; callback: (r: { credential: string }) => void }): void;
          renderButton(el: HTMLElement, opts: Record<string, unknown>): void;
        };
      };
    };
  }
}

const GSI_SRC = "https://accounts.google.com/gsi/client";

/** Loads the GSI script once, no matter how many components ask for it. */
function loadGSI(): Promise<void> {
  const existing = document.querySelector<HTMLScriptElement>(`script[src="${GSI_SRC}"]`);
  if (existing) {
    return existing.dataset.loaded === "true"
      ? Promise.resolve()
      : new Promise((resolve, reject) => {
          existing.addEventListener("load", () => resolve());
          existing.addEventListener("error", () => reject(new Error("could not load Google sign-in")));
        });
  }
  return new Promise((resolve, reject) => {
    const script = document.createElement("script");
    script.src = GSI_SRC;
    script.async = true;
    script.defer = true;
    script.onload = () => { script.dataset.loaded = "true"; resolve(); };
    script.onerror = () => reject(new Error("could not load Google sign-in"));
    document.head.appendChild(script);
  });
}

/**
 * Renders Google's own sign-in button. The credential it returns goes straight
 * to the server, which verifies it against Google's signing keys — the browser
 * never decides who you are.
 */
export function GoogleSignIn({ clientId, onError }: { clientId: string; onError: (msg: string) => void }) {
  const holder = useRef<HTMLDivElement>(null);
  const { refresh } = useAuth();
  const [ready, setReady] = useState(false);

  useEffect(() => {
    let cancelled = false;
    loadGSI()
      .then(() => {
        if (cancelled || !holder.current || !window.google) return;
        window.google.accounts.id.initialize({
          client_id: clientId,
          callback: async (response) => {
            try {
              await api.loginGoogle(response.credential);
              await refresh();
            } catch (err) {
              onError(err instanceof Error ? err.message : "Sign-in failed");
            }
          },
        });
        window.google.accounts.id.renderButton(holder.current, {
          theme: "filled_black",
          size: "large",
          shape: "pill",
          text: "signin_with",
          width: 280,
        });
        setReady(true);
      })
      .catch((err: Error) => onError(err.message));
    return () => { cancelled = true; };
  }, [clientId, refresh, onError]);

  return (
    <div className="gsi">
      <div ref={holder} />
      {!ready && <p className="muted small">Loading Google sign-in…</p>}
    </div>
  );
}
