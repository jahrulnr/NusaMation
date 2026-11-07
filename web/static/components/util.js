import { S } from "./state.js?v=33";

export const $ = (sel, root = document) => root.querySelector(sel);

export const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

export const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

export const clamp = (n, a, b) => Math.max(a, Math.min(b, n));

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

export const now = () => Date.now();

export const uid = (p = "x") => p + "_" + Math.random().toString(36).slice(2, 10);

export const isMobile = () => window.matchMedia("(max-width: 800px)").matches;

export const isShort = () => window.matchMedia("(max-height: 500px)").matches;

export function icon(name, cls = "") {
  return `<svg class="icon ${cls}" aria-hidden="true"><use href="#i-${name}"></use></svg>`;
}

export const AGENT_COLORS = {
  amber: "linear-gradient(145deg,#ffd27b,#d99125)",
  violet: "linear-gradient(145deg,#c8b6ff,#7a5cff)",
  emerald: "linear-gradient(145deg,#9af0d6,#2fae8d)",
  blue: "linear-gradient(145deg,#a9d2ff,#4b8fe8)",
  rose: "linear-gradient(145deg,#ffc0cb,#e5678f)",
  slate: "linear-gradient(145deg,#c5ccd8,#6c7a8e)",
};

// Tool kinds the UI knows how to style. t.kind is agent-supplied, so anything
// outside this set falls back to "other" before it reaches a class attribute.
export const TOOL_KINDS = new Set(["read", "edit", "delete", "move", "search", "execute", "think", "fetch", "switch_mode", "other"]);

/* ---------------- time / formatting ---------------- */
export function timeAgo(ms) {
  if (!ms) return "";
  const d = Math.max(0, now() - ms);
  if (d < 45e3) return "now";
  if (d < 36e5) return Math.floor(d / 60e3) + "m";
  if (d < 864e5) return Math.floor(d / 36e5) + "h";
  if (d < 7 * 864e5) return Math.floor(d / 864e5) + "d";
  return new Date(ms).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

export function clock(ms) {
  if (!ms) return "";
  return new Date(ms).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

export function dayLabel(ms) {
  if (!ms) return "";
  const d = new Date(ms);
  const today = new Date();
  const y = new Date(today.getFullYear(), today.getMonth(), today.getDate() - 1);
  if (d.toDateString() === today.toDateString()) return "Today";
  if (d.toDateString() === y.toDateString()) return "Yesterday";
  return d.toLocaleDateString(undefined, { weekday: "long", month: "long", day: "numeric" });
}

export function fmtBytes(n) {
  n = Number(n || 0);
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
  return (n / (1024 * 1024)).toFixed(1) + " MB";
}

export function truncate(s, n) {
  s = String(s || "");
  return s.length > n ? s.slice(0, n - 1).trimEnd() + "…" : s;
}

export function initials(name) {
  const s = String(name || "?").trim();
  if (!s) return "?";
  const parts = s.split(/\s+/).slice(0, 2);
  return parts.map((p) => Array.from(p)[0]).join("").toUpperCase();
}

export function agentOrb(agent, cls = "agent-orb") {
  const color = AGENT_COLORS[(agent && agent.color) || "amber"] || AGENT_COLORS.amber;
  return `<span class="${cls}" style="background:${color}">${esc(initials(agent && agent.name))}</span>`;
}

export function agentName(agent) {
  return (agent && agent.name) || "Agent";
}

export function currentAgent() {
  if (S.current) return S.agents.find((a) => a.id === S.current.agentId) || (S.currentAgent?.id === S.current.agentId ? S.currentAgent : null);
  return S.agents.find((a) => a.id === S.welcomeAgent) || S.agents.find((a) => a.enabled) || S.agents[0] || null;
}

export function agentById(id) {
  return S.agents.find((a) => a.id === id);
}

export function configLabel(o) {
  const choice = configChoices(o).find((v) => String(v.value) === String(o.currentValue));
  return o.type === "boolean" ? (o.currentValue === true || o.currentValue === "true" ? "On" : "Off") : (choice?.name || o.currentValue || o.name || o.id);
}

export function configChoices(o) {
  return (o.options || []).flatMap((v) => Array.isArray(v.options) ? v.options.map((c) => ({ ...c, groupName: v.name || v.group })) : [v]);
}

export function pretty(v) {
  if (v === undefined || v === null) return "";
  if (typeof v === "string") {
    const t = v.trim();
    if ((t.startsWith("{") && t.endsWith("}")) || (t.startsWith("[") && t.endsWith("]"))) {
      try { return JSON.stringify(JSON.parse(t), null, 2); } catch {}
    }
    return v;
  }
  try { return JSON.stringify(v, null, 2); } catch { return String(v); }
}

export function parseJSON(v, fallback = null) {
  if (v == null) return fallback;
  if (typeof v === "object") return v;
  try { return JSON.parse(v); } catch { return fallback; }
}

export function parseConfigs(raw) {
  const v = parseJSON(raw, []);
  return Array.isArray(v) ? v : [];
}
