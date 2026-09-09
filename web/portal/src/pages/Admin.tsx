import { useCallback, useEffect, useState } from "react";
import { api } from "../api";
import type { Ad, Stats } from "../types";

export default function Admin() {
  const [ads, setAds] = useState<Ad[]>([]);
  const [stats, setStats] = useState<Stats | null>(null);
  const [error, setError] = useState("");
  const [previewing, setPreviewing] = useState<Ad | null>(null);

  const load = useCallback(async () => {
    try {
      const [a, s] = await Promise.all([api.adminAds(), api.adminStats()]);
      setAds(a);
      setStats(s);
      setError("");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not load the queue");
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  // The console follows the same event stream as the TV, so a decision made on
  // someone else's laptop shows up here without a refresh.
  useEffect(() => {
    const es = new EventSource("/api/events");
    const onChange = () => { void load(); };
    es.addEventListener("playlist", onChange);
    return () => es.close();
  }, [load]);

  async function act<T>(fn: () => Promise<T>) {
    try {
      await fn();
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "That did not work");
    }
  }

  const pending = ads.filter((a) => a.status === "pending");
  const approved = ads.filter((a) => a.status === "approved");
  const rejected = ads.filter((a) => a.status === "rejected");

  return (
    <div className="admin">
      <div className="stats">
        <Stat label="Waiting" value={stats?.pending ?? 0} tone={pending.length > 0 ? "alert" : undefined} />
        <Stat label="On screen" value={stats?.onScreen ?? 0} />
        <Stat label="Loop length" value={formatDuration(stats?.rotationMs ?? 0)} />
        <Stat
          label="Displays"
          value={stats?.displaysOnline ?? 0}
          tone={(stats?.displaysOnline ?? 0) === 0 ? "alert" : "good"}
        />
        <div className="stat-actions">
          <button className="btn ghost small" onClick={() => act(() => api.displayCommand("prev"))}>◀ Back</button>
          <button className="btn ghost small" onClick={() => act(() => api.displayCommand("next"))}>Skip ▶</button>
          <button className="btn ghost small" onClick={() => act(() => api.displayCommand("reload"))}>Reload TV</button>
        </div>
      </div>

      {error && <p className="error">{error}</p>}

      <Section title="Waiting for review" count={pending.length}>
        {pending.length === 0 ? (
          <p className="muted">Queue is clear.</p>
        ) : (
          pending.map((ad) => (
            <AdRow key={ad.id} ad={ad} onPreview={setPreviewing}>
              <button className="btn primary small" onClick={() => act(() => api.review(ad.id, "approve"))}>
                Approve
              </button>
              <button
                className="btn danger small"
                onClick={() => {
                  const note = prompt("Why? The submitter sees this.") ?? "";
                  void act(() => api.review(ad.id, "reject", note));
                }}
              >
                Reject
              </button>
            </AdRow>
          ))
        )}
      </Section>

      <Section title="In rotation" count={approved.length}>
        {approved.length === 0 ? (
          <p className="muted">Nothing is on the TV. Approve something above.</p>
        ) : (
          approved.map((ad, i) => (
            <AdRow key={ad.id} ad={ad} onPreview={setPreviewing}>
              <button
                className="btn ghost small"
                disabled={i === 0}
                onClick={() => act(() => api.reorder(move(approved, i, i - 1).map((a) => a.id)))}
                title="Move earlier"
              >↑</button>
              <button
                className="btn ghost small"
                disabled={i === approved.length - 1}
                onClick={() => act(() => api.reorder(move(approved, i, i + 1).map((a) => a.id)))}
                title="Move later"
              >↓</button>
              <button className="btn ghost small" onClick={() => act(() => api.patch(ad.id, { enabled: !ad.enabled }))}>
                {ad.enabled ? "Pause" : "Resume"}
              </button>
              <button className="btn danger small" onClick={() => confirmDelete(ad, act)}>Delete</button>
            </AdRow>
          ))
        )}
      </Section>

      {rejected.length > 0 && (
        <Section title="Rejected" count={rejected.length}>
          {rejected.map((ad) => (
            <AdRow key={ad.id} ad={ad} onPreview={setPreviewing}>
              <button className="btn ghost small" onClick={() => act(() => api.review(ad.id, "approve"))}>
                Approve after all
              </button>
              <button className="btn danger small" onClick={() => confirmDelete(ad, act)}>Delete</button>
            </AdRow>
          ))}
        </Section>
      )}

      {previewing && <Preview ad={previewing} onClose={() => setPreviewing(null)} />}
    </div>
  );
}

function confirmDelete(ad: Ad, act: (fn: () => Promise<unknown>) => Promise<void>) {
  if (confirm(`Delete “${ad.title}” permanently? The file goes too.`)) {
    void act(() => api.remove(ad.id));
  }
}

function move<T>(list: T[], from: number, to: number): T[] {
  const copy = [...list];
  const [item] = copy.splice(from, 1);
  copy.splice(to, 0, item);
  return copy;
}

function Section({ title, count, children }: { title: string; count: number; children: React.ReactNode }) {
  return (
    <section className="card">
      <h2 className="card-title">
        {title} <span className="count">{count}</span>
      </h2>
      <div className="rows">{children}</div>
    </section>
  );
}

function Stat({ label, value, tone }: { label: string; value: string | number; tone?: "alert" | "good" }) {
  return (
    <div className={tone ? `stat ${tone}` : "stat"}>
      <div className="stat-value">{value}</div>
      <div className="stat-label">{label}</div>
    </div>
  );
}

function AdRow({ ad, onPreview, children }: { ad: Ad; onPreview: (a: Ad) => void; children: React.ReactNode }) {
  return (
    <div className="adrow">
      <button className="adthumb" onClick={() => onPreview(ad)} title="Preview">
        {ad.kind === "image" && ad.src && <img src={ad.src} alt="" />}
        {ad.kind === "video" && ad.src && <video src={ad.src} muted playsInline preload="metadata" />}
        {ad.kind === "html" && <span className="adthumb-html">HTML</span>}
      </button>
      <div className="adrow-main">
        <div className="strong">{ad.title}</div>
        <div className="muted small">
          {ad.submitterName || ad.submitterEmail} · {Math.round(ad.durationMs / 1000)}s · {ad.fit}
          {ad.endsAt && ` · until ${new Date(ad.endsAt).toLocaleDateString()}`}
          {typeof ad.plays === "number" && ad.plays > 0 && ` · ${ad.plays} plays`}
        </div>
        {ad.status === "approved" && !ad.playing && (
          <div className="note">Not currently on screen — paused or outside its dates.</div>
        )}
      </div>
      <div className="adrow-actions">{children}</div>
    </div>
  );
}

function Preview({ ad, onClose }: { ad: Ad; onClose: () => void }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div className="modal" onClick={onClose}>
      <div className="modal-body" onClick={(e) => e.stopPropagation()} style={{ background: ad.background }}>
        {ad.kind === "image" && ad.src && <img src={ad.src} alt="" style={{ objectFit: ad.fit }} />}
        {ad.kind === "video" && ad.src && <video src={ad.src} controls autoPlay muted style={{ objectFit: ad.fit }} />}
        {ad.kind === "html" && (
          // Same sandbox the TV uses, so what you see here is what plays there.
          <iframe sandbox="allow-scripts" srcDoc={ad.html ?? ""} title={ad.title} />
        )}
      </div>
      <button className="modal-close" onClick={onClose}>Close</button>
    </div>
  );
}

function formatDuration(ms: number): string {
  const total = Math.round(ms / 1000);
  const m = Math.floor(total / 60);
  const s = total % 60;
  return m > 0 ? `${m}m ${s}s` : `${s}s`;
}
