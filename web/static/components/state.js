export function readActivityState() {
  try { return JSON.parse(sessionStorage.getItem("nusamation:activity-state") || "{}"); } catch { return {}; }
}

export const activityState = readActivityState();

export const S = {
  authInitialized: false,
  appVersion: "",
  csrf: "",
  me: null,
  bootstrap: null,
  agents: [],
  mcp: [],
  channels: [],
  conversations: [],
  projects: [],
  selectedProjectId: (() => { try { return localStorage.getItem("nusamation:selected-project") || ""; } catch { return ""; } })(),
  expandedProjects: new Set(),
  approvals: [],
  approvalsById: new Map(),
  elicitations: [],
  current: null,
  currentAgent: null,
  messages: [],
  turns: {},
  configs: [],
  active: null,
  unread: new Map(),
  openThoughts: new Set(activityState.thoughts || []),
  openTools: new Set(activityState.tools || []),
  activityExpanded: new Map(activityState.groups || []),
  unwrappedTools: new Set(activityState.unwrapped || []),
  resolving: new Set(),
  attachments: [],
  replyToChannel: false,
  draft: "",
  whatsappSettings: null,
  routingDefaults: null,
  agentDefaults: { agentId: "", selections: [] },
  defaultDraft: null,
  defaultConfigs: [],
  defaultBusy: false,
  defaultError: "",
  defaultSeq: 0,
  automations: [],
  automationView: "list",
  automationJob: null,
  automationRuns: [],
  automationLoading: false,
  automationError: "",
  automationServerZone: "",
  ws: null,
  wsState: "offline",
  reconnectAttempt: 0,
  reconnectTimer: null,
  route: location.pathname,
  query: "",
  welcomeAgent: "",
  settingsTab: "agents",
  agentForm: null,
  mcpForm: null,
  editingAgent: null,
  editingMCP: null,
  modal: null,
  qrTimer: null,
  renderTimer: null,
  sending: false,
  sendDelivery: "steer",
  scrollPinned: true,
  planExpanded: false,
  draftSelections: [],
  draftPreviewSeq: 0,
  booted: false,
  loadingConversation: false,
  loadingDeltas: [],
  loadError: "",
  loadSeq: 0,
  historyHasMore: false,
  historyLoading: false,
  historyError: "",
  preparing: "",
  prepareController: null,
  slowTimer: null,
  authTerminal: null,
  lastNotice: "",
  updateInfo: null,
  updateBusy: "",
  notifySettings: { enabled: true, available: false, backend: "" },
};

// Composer state belongs to a conversation; the empty key is the welcome draft.
export const composers = new Map();

export let composerKey = "";

export function defaultSendDelivery() {
  try { return localStorage.getItem("nusamation:sendDelivery") === "queue" ? "queue" : "steer"; } catch { return "steer"; }
}

export const UI_FONTS = [
  { id: "", name: "System default" },
  { id: "inter", name: "Inter (sans-serif)" },
  { id: "serif", name: "Tinos (serif)" },
  { id: "toddler", name: "Toddler Writing (handwriting)" },
];

const FONT_KEY = "nusamation:font";

export function uiFont() {
  try {
    const v = localStorage.getItem(FONT_KEY) || "";
    return UI_FONTS.some((f) => f.id === v) ? v : "";
  } catch { return ""; }
}

export function applyFont() {
  const v = uiFont();
  if (v) document.documentElement.dataset.font = v;
  else delete document.documentElement.dataset.font;
}

export function setUiFont(v) {
  try {
    if (v) localStorage.setItem(FONT_KEY, v);
    else localStorage.removeItem(FONT_KEY);
  } catch {}
  applyFont();
}

// Interface text size. The stylesheet is px-based, so "size" is applied as a
// proportional zoom on <body> against the 14.5px base in base.css. Range
// follows common text-scaling bounds (9–20px); empty means no override.
export const UI_FONT_SIZES = [9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20];

const FONT_SIZE_KEY = "nusamation:font-size";
const BASE_FONT_SIZE = 14.5;

export function uiFontSize() {
  try {
    const v = localStorage.getItem(FONT_SIZE_KEY) || "";
    return UI_FONT_SIZES.some((s) => String(s) === v) ? v : "";
  } catch { return ""; }
}

export function applyFontSize() {
  const v = uiFontSize();
  if (v) document.documentElement.style.setProperty("--ui-zoom", String(Number(v) / BASE_FONT_SIZE));
  else document.documentElement.style.removeProperty("--ui-zoom");
}

export function setUiFontSize(v) {
  try {
    if (v) localStorage.setItem(FONT_SIZE_KEY, v);
    else localStorage.removeItem(FONT_SIZE_KEY);
  } catch {}
  applyFontSize();
}

export function composerFor(key) {
  if (!composers.has(key)) composers.set(key, { draft: "", attachments: [], references: [], sendDelivery: defaultSendDelivery(), replyToChannel: false, sending: false });
  return composers.get(key);
}

for (const field of ["draft", "attachments", "sendDelivery", "replyToChannel", "sending"]) {
  Object.defineProperty(S, field, {
    get: () => composerFor(composerKey)[field],
    set: (value) => { composerFor(composerKey)[field] = value; },
  });
}

const composerSwitchListeners = new Set();

// Registered by feature modules (e.g. the browser pane) that must react when the
// visible conversation — and with it the composer key — changes.
export function onComposerSwitch(fn) { composerSwitchListeners.add(fn); }

export function switchComposer(key) {
  composerKey = key || "";
  for (const fn of composerSwitchListeners) {
    try { fn(composerKey); } catch (e) { console.warn("composer switch listener failed", e); }
  }
}

export function moveComposer(from, to) {
  composers.set(to, composerFor(from));
  composers.delete(from);
  if (composerKey === from) switchComposer(to);
}

const SELECTED_PROJECT_KEY = "nusamation:selected-project";

export function setSelectedProject(id) {
  S.selectedProjectId = id || "";
  try {
    if (S.selectedProjectId) localStorage.setItem(SELECTED_PROJECT_KEY, S.selectedProjectId);
    else localStorage.removeItem(SELECTED_PROJECT_KEY);
  } catch {}
}

export function saveActivityState() {
  try {
    sessionStorage.setItem("nusamation:activity-state", JSON.stringify({
      thoughts: [...S.openThoughts], tools: [...S.openTools], groups: [...S.activityExpanded], unwrapped: [...S.unwrappedTools],
    }));
  } catch {}
}
