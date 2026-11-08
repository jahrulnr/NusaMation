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

export function composerFor(key) {
  if (!composers.has(key)) composers.set(key, { draft: "", attachments: [], sendDelivery: "steer", replyToChannel: false, sending: false });
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
