import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "../api";
import { useAuth } from "../auth";
import type { Ad } from "../types";

type Mode = "file" | "html";

export default function Submit() {
  const { config } = useAuth();
  const [mode, setMode] = useState<Mode>("file");
  const [file, setFile] = useState<File | null>(null);
  const [preview, setPreview] = useState<string>("");
  const [title, setTitle] = useState("");
  const [html, setHtml] = useState("");
  const [durationSec, setDurationSec] = useState(10);
  const [fit, setFit] = useState<"contain" | "cover">("contain");
  const [background, setBackground] = useState("#000000");
  const [endsAt, setEndsAt] = useState("");
  const [dragging, setDragging] = useState(false);
  const [progress, setProgress] = useState<number | null>(null);
  const [error, setError] = useState("");
  const [mine, setMine] = useState<Ad[]>([]);
  const fileInput = useRef<HTMLInputElement>(null);

  const maxBytes = config?.maxUploadBytes ?? 256 * 1024 * 1024;
  const maxSec = Math.floor((config?.maxDurationMs ?? 120000) / 1000);

  const loadMine = useCallback(async () => {
    try {
      setMine(await api.mySubmissions());
    } catch {
      /* the list is secondary; a failure here should not block submitting */
    }
  }, []);

  useEffect(() => { void loadMine(); }, [loadMine]);

  // Object URLs are a classic leak: revoke the previous one whenever the file
  // changes, and on unmount.
  useEffect(() => {
    if (!file) { setPreview(""); return; }
    const url = URL.createObjectURL(file);
    setPreview(url);
    return () => URL.revokeObjectURL(url);
  }, [file]);

  function chooseFile(f: File | null) {
    setError("");
    if (!f) return;
    if (f.size > maxBytes) {
      setError(`That file is ${formatBytes(f.size)}. The limit is ${formatBytes(maxBytes)}.`);
      return;
    }
    setFile(f);
    if (!title) setTitle(f.name.replace(/\.[^.]+$/, ""));
    // A video's real length is measured server-side by ffprobe; this is just a
    // sensible starting point for the slider.
    if (f.type.startsWith("video/")) setDurationSec(15);
  }

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError("");

    if (!title.trim()) return setError("Give your ad a title.");
    if (mode === "file" && !file) return setError("Choose an image or video.");
    if (mode === "html" && !html.trim()) return setError("Write some HTML, or switch to a file.");

    const form = new FormData();
    form.set("title", title.trim());
    form.set("durationMs", String(durationSec * 1000));
    form.set("fit", fit);
    form.set("background", background);
    if (endsAt) form.set("endsAt", endsAt);
    if (mode === "file" && file) form.set("file", file);
    if (mode === "html") form.set("html", html);

    setProgress(0);
    try {
      await api.submit(form, setProgress);
      setFile(null);
      setTitle("");
      setHtml("");
      setEndsAt("");
      if (fileInput.current) fileInput.current.value = "";
      await loadMine();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Upload failed");
    } finally {
      setProgress(null);
    }
  }

  async function withdraw(id: string) {
    if (!confirm("Withdraw this submission? The file is deleted.")) return;
    try {
      await api.withdraw(id);
      await loadMine();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not withdraw");
    }
  }

  return (
    <div className="cols">
      <section className="card">
        <h2 className="card-title">New ad</h2>

        <div className="seg">
          <button type="button" className={mode === "file" ? "seg-btn on" : "seg-btn"} onClick={() => setMode("file")}>
            Image or video
          </button>
          <button type="button" className={mode === "html" ? "seg-btn on" : "seg-btn"} onClick={() => setMode("html")}>
            HTML slide
          </button>
        </div>

        <form className="stack" onSubmit={onSubmit}>
          {mode === "file" ? (
            <div
              className={dragging ? "drop over" : "drop"}
              onDragOver={(e) => { e.preventDefault(); setDragging(true); }}
              onDragLeave={() => setDragging(false)}
              onDrop={(e) => {
                e.preventDefault();
                setDragging(false);
                chooseFile(e.dataTransfer.files[0] ?? null);
              }}
              onClick={() => fileInput.current?.click()}
            >
              <input
                ref={fileInput}
                type="file"
                accept="image/jpeg,image/png,image/gif,image/webp,image/avif,video/mp4,video/webm,video/quicktime"
                hidden
                onChange={(e) => chooseFile(e.target.files?.[0] ?? null)}
              />
              {file ? (
                <div className="drop-filled">
                  {file.type.startsWith("video/")
                    ? <video src={preview} muted playsInline className="thumb" />
                    : <img src={preview} alt="" className="thumb" />}
                  <div>
                    <div className="strong">{file.name}</div>
                    <div className="muted small">{formatBytes(file.size)} · click to replace</div>
                  </div>
                </div>
              ) : (
                <div className="drop-empty">
                  <div className="drop-icon">＋</div>
                  <div className="strong">Drop a file, or click to browse</div>
                  <div className="muted small">
                    JPEG, PNG, GIF, WebP, AVIF, MP4, WebM, MOV · up to {formatBytes(maxBytes)}
                  </div>
                </div>
              )}
            </div>
          ) : (
            <label className="field">
              <span>HTML</span>
              <textarea
                value={html}
                onChange={(e) => setHtml(e.target.value)}
                rows={10}
                spellCheck={false}
                placeholder={"<h1>Demo Night</h1>\n<p>Thursday, 6pm — Ideation Lab</p>"}
              />
              <span className="hint">
                Runs in a sandbox on the TV: styles and scripts work, but it cannot reach the rest of the page.
              </span>
            </label>
          )}

          <label className="field">
            <span>Title</span>
            <input value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Demo Night — Thursday" />
            <span className="hint">Only organizers see this. It is not shown on the TV.</span>
          </label>

          <div className="row">
            <label className="field grow">
              <span>Time on screen — {durationSec}s</span>
              <input
                type="range" min={2} max={maxSec} step={1}
                value={durationSec}
                onChange={(e) => setDurationSec(Number(e.target.value))}
              />
              {mode === "file" && file?.type.startsWith("video/") && (
                <span className="hint">Leave it as-is and we will use the clip's real length.</span>
              )}
            </label>
          </div>

          <div className="row">
            <label className="field">
              <span>Sizing</span>
              <select value={fit} onChange={(e) => setFit(e.target.value as "contain" | "cover")}>
                <option value="contain">Fit — show all of it</option>
                <option value="cover">Fill — crop to the screen</option>
              </select>
            </label>
            <label className="field">
              <span>Backdrop</span>
              <input type="color" value={background} onChange={(e) => setBackground(e.target.value)} />
              <span className="hint">Behind the image when it does not fill the screen.</span>
            </label>
            <label className="field">
              <span>Take down after</span>
              <input type="date" value={endsAt} onChange={(e) => setEndsAt(e.target.value)} />
              <span className="hint">Optional. Stops an old event sitting on the TV forever.</span>
            </label>
          </div>

          {error && <p className="error">{error}</p>}

          {progress !== null && (
            <div className="progress"><div className="progress-bar" style={{ width: `${progress}%` }} /></div>
          )}

          <button className="btn primary" disabled={progress !== null}>
            {progress !== null ? `Uploading… ${progress}%` : "Submit for review"}
          </button>
        </form>
      </section>

      <section className="card">
        <h2 className="card-title">Your submissions</h2>
        {mine.length === 0 ? (
          <p className="muted">Nothing yet.</p>
        ) : (
          <ul className="list">
            {mine.map((ad) => (
              <li key={ad.id} className="item">
                <div className="item-main">
                  <div className="strong">{ad.title}</div>
                  <div className="muted small">
                    {new Date(ad.createdAt).toLocaleDateString()} · {ad.kind}
                    {typeof ad.plays === "number" && ad.plays > 0 && ` · shown ${ad.plays}×`}
                  </div>
                  {ad.reviewNote && <div className="note">“{ad.reviewNote}”</div>}
                </div>
                <div className="item-side">
                  <StatusPill ad={ad} />
                  {ad.status === "pending" && (
                    <button className="btn ghost small" onClick={() => withdraw(ad.id)}>Withdraw</button>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

function StatusPill({ ad }: { ad: Ad }) {
  if (ad.status === "approved") {
    const expired = ad.endsAt && new Date(ad.endsAt) < new Date();
    if (expired) return <span className="pill done">Finished</span>;
    if (!ad.enabled) return <span className="pill hold">Paused</span>;
    return <span className="pill live">On the TV</span>;
  }
  if (ad.status === "rejected") return <span className="pill no">Not approved</span>;
  return <span className="pill wait">Waiting for review</span>;
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(v >= 10 ? 0 : 1)} ${units[i]}`;
}
