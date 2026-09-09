export type AdStatus = "pending" | "approved" | "rejected";
export type AdKind = "image" | "video" | "html";

export interface Ad {
  id: string;
  title: string;
  kind: AdKind;
  mediaFile?: string;
  html?: string;
  mime?: string;
  bytes?: number;
  durationMs: number;
  fit: "contain" | "cover";
  background: string;
  status: AdStatus;
  enabled: boolean;
  sortOrder: number;
  submitterEmail: string;
  submitterName: string;
  reviewNote?: string;
  reviewedBy?: string;
  startsAt?: string;
  endsAt?: string;
  createdAt: string;
  updatedAt: string;
  /** Present on admin and "my submissions" responses. */
  plays?: number;
  src?: string;
  playing?: boolean;
}

export interface Me {
  signedIn: boolean;
  email?: string;
  name?: string;
  isAdmin?: boolean;
}

export interface PublicConfig {
  googleClientId: string;
  passwordLogin: boolean;
  allowedDomains: string[] | null;
  maxUploadBytes: number;
  defaultDurationMs: number;
  maxDurationMs: number;
}

export interface Stats {
  pending: number;
  approved: number;
  rejected: number;
  onScreen: number;
  rotationMs: number;
  displaysOnline: number;
}
