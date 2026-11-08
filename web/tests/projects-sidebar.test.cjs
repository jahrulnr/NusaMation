const { test } = require("node:test");
const assert = require("node:assert/strict");

let appendSource, baseName, buildSaveBody, groupProjectless, makeSourcePrimary,
  partitionProjects, projectSources, removeSourceAt, staleProjectSnapshot, visibleProjectId;

test.before(async () => {
  ({
    appendSource, baseName, buildSaveBody, groupProjectless, makeSourcePrimary,
    partitionProjects, projectSources, removeSourceAt, staleProjectSnapshot, visibleProjectId,
  } = await import("../static/components/projectmodel.js"));
});

test("basename keeps the full path out of the visible label", () => {
  assert.equal(baseName("/home/ada/atlas"), "atlas");
  assert.equal(baseName("C:\\work\\atlas\\"), "atlas");
  assert.equal(baseName("atlas"), "atlas");
});

test("read sources from rootPaths and fall back to sources", () => {
  assert.deepEqual(projectSources({ rootPaths: ["/a"], sources: ["/b"] }), ["/a"]);
  assert.deepEqual(projectSources({ sources: ["/b"] }), ["/b"]);
  assert.deepEqual(projectSources({}), []);
});

test("added folders stay at the end and duplicates are ignored", () => {
  assert.deepEqual(appendSource(["/primary"], "/other"), ["/primary", "/other"]);
  assert.deepEqual(appendSource(["/primary"], "/primary"), ["/primary"]);
  assert.deepEqual(appendSource(["/primary"], ""), ["/primary"]);
});

test("make primary reorders the draft and removing it promotes the next path", () => {
  assert.deepEqual(makeSourcePrimary(["/a", "/b", "/c"], 2), ["/c", "/a", "/b"]);
  assert.deepEqual(makeSourcePrimary(["/a", "/b"], 0), ["/a", "/b"]);
  assert.deepEqual(removeSourceAt(["/a", "/b", "/c"], 0), ["/b", "/c"]);
  assert.deepEqual(removeSourceAt(["/only"], 0), []);
});

test("an older project snapshot does not replace a newer row", () => {
  const stored = { id: "p1", pinned: true, rootPaths: ["/now"], updatedAt: 20 };
  assert.equal(staleProjectSnapshot(stored, { id: "p1", pinned: false, updatedAt: 19 }), true);
  assert.equal(staleProjectSnapshot(stored, { id: "p1", updatedAt: 20 }), false);
  assert.equal(staleProjectSnapshot(stored, { id: "p1", rootPaths: ["/next"], updatedAt: 21 }), false);
  assert.equal(staleProjectSnapshot(stored, { id: "p1", deleted: true, updatedAt: 1 }), false);
  assert.equal(staleProjectSnapshot(stored, { id: "p1" }), true);
  assert.equal(staleProjectSnapshot(stored, { id: "p1", updatedAt: "20" }), true);
  assert.equal(staleProjectSnapshot({ id: "p1" }, { id: "p1" }), false);
  assert.equal(staleProjectSnapshot({ id: "p1" }, { id: "p1", updatedAt: 4 }), false);
  assert.equal(staleProjectSnapshot(null, { id: "p1", updatedAt: 1 }), false);
});

test("save body confirms folders and sends updatedAt only when editing", () => {
  assert.deepEqual(buildSaveBody({ name: " Atlas ", sources: ["/a"], foldersConfirmed: false }), {
    name: "Atlas", sources: ["/a"], foldersConfirmed: false,
  });
  const edit = buildSaveBody({ id: "p1", name: "Atlas", sources: ["/a"], updatedAt: 9, foldersConfirmed: true });
  assert.equal(edit.updatedAt, 9);
  assert.equal(edit.foldersConfirmed, true);
  assert.equal("updatedAt" in buildSaveBody({ id: "", name: "Atlas", sources: ["/a"], updatedAt: 9 }), false);
});

test("pinned projects sort before loose ones, both by name", () => {
  const parts = partitionProjects([
    { id: "pin", name: "Beta", pinned: true },
    { id: "zed", name: "Zed", pinned: false },
    { id: "in", name: "Alpha", pinned: false },
  ]);
  assert.deepEqual(parts.pinned.map((p) => p.id), ["pin"]);
  assert.deepEqual(parts.loose.map((p) => p.id), ["in", "zed"]);
});

test("projectless chats keep date groups and project chats stay out", () => {
  const now = new Date(2026, 10, 8, 15, 0, 0).getTime();
  const projects = [{ id: "p1", name: "Atlas" }];
  const groups = groupProjectless([
    { id: "pinned", pinned: true, projectId: "", lastMessageAt: now },
    { id: "in-project", pinned: true, projectId: "p1", lastMessageAt: now },
    { id: "today", lastMessageAt: now },
    { id: "orphan", projectId: "missing", lastMessageAt: now },
    { id: "yesterday", lastMessageAt: new Date(2026, 10, 7, 12).getTime() },
    { id: "earlier", lastMessageAt: new Date(2026, 9, 1, 12).getTime() },
    { id: "archived", archived: true, lastMessageAt: now },
  ], projects, now);
  const labels = Object.fromEntries(groups);
  assert.deepEqual(labels.Pinned.map((c) => c.id), ["pinned"]);
  assert.deepEqual(labels.Today.map((c) => c.id), ["today", "orphan"]);
  assert.deepEqual(labels.Yesterday.map((c) => c.id), ["yesterday"]);
  assert.deepEqual(labels.Earlier.map((c) => c.id), ["earlier"]);
  assert.equal(visibleProjectId({ projectId: "p1" }, projects), "p1");
  assert.equal(visibleProjectId({ projectId: "missing" }, projects), "");
  assert.equal(visibleProjectId({ projectId: "" }, projects), "");
});
