/* Pure project sidebar and editor rules. No DOM. */

export function baseName(path) {
  const raw = String(path ?? "");
  const trimmed = raw.replace(/[\\/]+$/, "");
  const cut = trimmed.split(/[\\/]/).pop();
  return cut || raw;
}

export function projectSources(project) {
  if (Array.isArray(project?.rootPaths)) return project.rootPaths.slice();
  if (Array.isArray(project?.sources)) return project.sources.slice();
  return [];
}

export function appendSource(sources, path) {
  const next = Array.isArray(sources) ? sources.slice() : [];
  if (!path || next.includes(path)) return next;
  next.push(path);
  return next;
}

export function makeSourcePrimary(sources, index) {
  const next = Array.isArray(sources) ? sources.slice() : [];
  const i = Number(index);
  if (!Number.isInteger(i) || i <= 0 || i >= next.length) return next;
  const [item] = next.splice(i, 1);
  next.unshift(item);
  return next;
}

export function removeSourceAt(sources, index) {
  const next = Array.isArray(sources) ? sources.slice() : [];
  const i = Number(index);
  if (!Number.isInteger(i) || i < 0 || i >= next.length) return next;
  next.splice(i, 1);
  return next;
}

// staleProjectSnapshot reports an incoming non-delete that must not replace the
// stored row. A missing updatedAt counts as older when the stored row has one.
// Deleted payloads are never stale; the caller removes the row.
export function staleProjectSnapshot(stored, incoming) {
  if (!stored || incoming?.deleted) return false;
  const next = incoming?.updatedAt;
  const prev = stored.updatedAt;
  if (typeof next !== "number") return typeof prev === "number";
  if (typeof prev !== "number") return false;
  return next < prev;
}

export function buildSaveBody(snapshot) {
  const body = {
    name: String(snapshot?.name ?? "").trim(),
    sources: Array.isArray(snapshot?.sources) ? snapshot.sources.slice() : [],
    foldersConfirmed: !!snapshot?.foldersConfirmed,
  };
  if (snapshot?.id) body.updatedAt = snapshot.updatedAt;
  return body;
}

export function visibleProjectId(conversation, projects) {
  const id = conversation?.projectId || "";
  if (!id) return "";
  return (projects || []).some((p) => p && p.id === id) ? id : "";
}

export function partitionProjects(projects) {
  const list = Array.isArray(projects) ? projects.filter((p) => p && p.id) : [];
  const byName = (a, b) => String(a.name || "").localeCompare(String(b.name || ""), undefined, { sensitivity: "base" });
  const pinned = list.filter((p) => p.pinned).sort(byName);
  const loose = list.filter((p) => !p.pinned).sort(byName);
  return { pinned, loose };
}

export function groupProjectless(conversations, projects, nowMs = Date.now()) {
  const convs = (conversations || []).filter((c) => c && !c.archived && !visibleProjectId(c, projects));
  const pinned = convs.filter((c) => c.pinned);
  const rest = convs.filter((c) => !c.pinned);
  const today = new Date(nowMs);
  today.setHours(0, 0, 0, 0);
  const yesterday = new Date(today.getTime() - 864e5);
  const gToday = [];
  const gYesterday = [];
  const gEarlier = [];
  rest.forEach((c) => {
    const t = c.lastMessageAt || c.updatedAt || 0;
    if (t >= today.getTime()) gToday.push(c);
    else if (t >= yesterday.getTime()) gYesterday.push(c);
    else gEarlier.push(c);
  });
  const groups = [];
  if (pinned.length) groups.push(["Pinned", pinned]);
  if (gToday.length) groups.push(["Today", gToday]);
  if (gYesterday.length) groups.push(["Yesterday", gYesterday]);
  if (gEarlier.length) groups.push(["Earlier", gEarlier]);
  return groups;
}

export function sortConversations(list) {
  return (list || []).slice().sort((a, b) => (Number(!!b.pinned) - Number(!!a.pinned)) || ((b.lastMessageAt || b.updatedAt || 0) - (a.lastMessageAt || a.updatedAt || 0)));
}
