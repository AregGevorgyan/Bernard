import { useState } from "react";
import { Link, Navigate, Route, Routes, useLocation } from "react-router-dom";
import { useAuth, GoogleSignIn } from "./auth";
import { api } from "./api";
import Submit from "./pages/Submit";
import Admin from "./pages/Admin";

export default function App() {
  const { me, loading } = useAuth();

  if (loading) return <div className="boot">Loading…</div>;
  if (!me?.signedIn) return <SignIn />;

  return (
    <div className="app">
      <Header />
      <main className="main">
        <Routes>
          <Route path="/" element={<Submit />} />
          <Route path="/admin" element={me.isAdmin ? <Admin /> : <Navigate to="/" replace />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </main>
    </div>
  );
}

function Header() {
  const { me, signOut } = useAuth();
  const { pathname } = useLocation();

  return (
    <header className="header">
      <div className="brand">
        <img className="brand-logo" src="/startup-shell-logo.svg" alt="Startup Shell" />
        <span className="brand-sub">Billboard</span>
      </div>
      <nav className="nav">
        <Link className={pathname === "/" ? "tab active" : "tab"} to="/">Submit</Link>
        {me?.isAdmin && (
          <Link className={pathname === "/admin" ? "tab active" : "tab"} to="/admin">Review</Link>
        )}
        <a className="tab" href="/display" target="_blank" rel="noreferrer">Preview&nbsp;↗</a>
      </nav>
      <div className="who">
        <span className="who-email">{me?.email}</span>
        <button className="btn ghost small" onClick={signOut}>Sign out</button>
      </div>
    </header>
  );
}

function SignIn() {
  const { config, refresh } = useAuth();
  const [error, setError] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);

  async function submitPassword(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.loginPassword(email, password);
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Sign-in failed");
    } finally {
      setBusy(false);
    }
  }

  const domains = config?.allowedDomains ?? [];

  return (
    <div className="signin">
      <div className="signin-card">
        <img className="signin-logo" src="/startup-shell-logo.svg" alt="Startup Shell" />
        <p className="signin-lede">
          Put something on the Shell TV. Sign in, upload it, an organizer approves it.
        </p>

        {config?.googleClientId ? (
          <GoogleSignIn clientId={config.googleClientId} onError={setError} />
        ) : config?.passwordLogin ? (
          <form className="stack" onSubmit={submitPassword}>
            <label className="field">
              <span>Email</span>
              <input value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@example.com" />
            </label>
            <label className="field">
              <span>Password</span>
              <input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
                autoFocus
              />
            </label>
            <button className="btn primary" disabled={busy}>{busy ? "Signing in…" : "Sign in"}</button>
            <p className="muted small">
              This server is in password mode. Set a Google client ID before putting it on the internet.
            </p>
          </form>
        ) : (
          <p className="error">This server has no sign-in method configured.</p>
        )}

        {domains.length > 0 && (
          <p className="muted small signin-domains">
            Sign in with your {domains.map((d) => `@${d}`).join(" or ")} account.
          </p>
        )}
        {error && <p className="error">{error}</p>}
      </div>
    </div>
  );
}
