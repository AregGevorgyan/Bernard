import type { Ad, Me, PublicConfig, Stats } from "./types";

/** Thrown for any non-2xx response, carrying the server's own message. */
export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, { credentials: "same-origin", ...init });
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  const body = text ? JSON.parse(text) : null;
  if (!res.ok) {
    throw new ApiError(res.status, body?.error ?? `Request failed (${res.status})`);
  }
  return body as T;
}

const json = (method: string, body?: unknown): RequestInit => ({
  method,
  headers: { "Content-Type": "application/json" },
  body: body === undefined ? undefined : JSON.stringify(body),
});

export const api = {
  config: () => request<PublicConfig>("/api/config"),
  me: () => request<Me>("/api/me"),

  loginGoogle: (credential: string) =>
    request<Me>("/api/auth/google", json("POST", { credential })),
  loginPassword: (email: string, password: string) =>
    request<Me>("/api/auth/password", json("POST", { email, password })),
  logout: () => request<void>("/api/auth/logout", { method: "POST" }),

  mySubmissions: () => request<Ad[]>("/api/submissions"),
  withdraw: (id: string) => request<void>(`/api/submissions/${id}`, { method: "DELETE" }),

  /**
   * Uploads use XMLHttpRequest rather than fetch purely for upload progress —
   * a member sending a 100 MB video over campus wifi needs to see it moving.
   */
  submit: (form: FormData, onProgress?: (pct: number) => void) =>
    new Promise<Ad>((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open("POST", "/api/submissions");
      xhr.withCredentials = true;
      xhr.upload.onprogress = (e) => {
        if (e.lengthComputable && onProgress) onProgress(Math.round((e.loaded / e.total) * 100));
      };
      xhr.onload = () => {
        let body: any = null;
        try { body = xhr.responseText ? JSON.parse(xhr.responseText) : null; } catch { /* keep null */ }
        if (xhr.status >= 200 && xhr.status < 300) resolve(body as Ad);
        else reject(new ApiError(xhr.status, body?.error ?? `Upload failed (${xhr.status})`));
      };
      xhr.onerror = () => reject(new ApiError(0, "Network error during upload"));
      xhr.send(form);
    }),

  adminAds: () => request<Ad[]>("/api/admin/ads"),
  adminStats: () => request<Stats>("/api/admin/stats"),
  review: (id: string, decision: "approve" | "reject", note = "") =>
    request<Ad>(`/api/admin/ads/${id}/review`, json("POST", { decision, note })),
  patch: (id: string, changes: Partial<Ad>) =>
    request<Ad>(`/api/admin/ads/${id}`, json("PATCH", changes)),
  remove: (id: string) => request<void>(`/api/admin/ads/${id}`, { method: "DELETE" }),
  reorder: (ids: string[]) => request<void>("/api/admin/order", json("PUT", { ids })),
  displayCommand: (command: "next" | "prev" | "reload") =>
    request<{ sent: string; displays: number }>(`/api/admin/display/${command}`, { method: "POST" }),
};
