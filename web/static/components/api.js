import { S } from "./state.js?v=43";
import { closeModal } from "./modal.js?v=43";
import { closeWS } from "./ws.js?v=43";
import { renderLogin } from "./auth.js?v=43";

export const REQUEST_TIMEOUT_MS = 20000;

/* ---------------- API ---------------- */
export class ApiError extends Error {
  constructor(message, status, code, data = {}) {
    super(message);
    this.status = status;
    this.code = code;
    this.data = data;
  }
}

export async function api(path, opts = {}) {
  const headers = new Headers(opts.headers || {});
  let body = opts.body;
  if (body !== undefined && !(body instanceof FormData)) {
    headers.set("Content-Type", "application/json");
    body = JSON.stringify(body);
  }
  if (S.csrf) headers.set("X-CSRF-Token", S.csrf);
  const timeout = opts.timeout ?? ((opts.method || "GET") === "GET" ? REQUEST_TIMEOUT_MS : 0);
  let res, text;
  try {
    res = await fetch("/api" + path, { ...opts, body, headers, credentials: "same-origin", signal: timeout ? AbortSignal.timeout?.(timeout) : opts.signal });
    text = await res.text();
  } catch (e) {
    if (e.name === "TimeoutError") throw new ApiError("The server took too long to respond.", 0, "timeout");
    throw new ApiError("Network error. Check your connection.", 0, "network");
  }
  let data = {};
  try {
    data = text ? JSON.parse(text) : {};
  } catch {
    if (res.ok) throw new ApiError("Unexpected response from server.", res.status, "bad_response");
  }
  if (res.status === 401 && !opts.noAuthRedirect) {
    handleLoggedOut();
    throw new ApiError(data.error || "Your session expired.", 401, data.code);
  }
  if (!res.ok) throw new ApiError(data.error || `Request failed (${res.status})`, res.status, data.code, data);
  return data;
}

export function handleLoggedOut() {
  if (!S.booted) return;
  S.booted = false;
  closeWS();
  closeModal();
  renderLogin("Your session has expired. Sign in again to continue.");
}
