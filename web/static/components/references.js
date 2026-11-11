// Positions in the editor are UTF-16 offsets. Convert only at the API boundary.
export function referenceTrigger(text, caret) {
  const before = text.slice(0, caret);
  const match = /(?:^|\s)([@#/])([^\s@#]*)$/.exec(before);
  if (!match) return null;
  const suffix = /^[^\s@#]*/.exec(text.slice(caret))[0];
  return { start: caret - match[2].length - 1, end: caret + suffix.length, prefix: match[1], query: match[2] };
}

export function reconcileReferences(oldText, newText, refs, selectionStart, selectionEnd, inputType = "") {
  if (oldText === newText) return refs;
  let start = selectionStart, end = selectionEnd;
  if (start == null || end == null) { start = 0; end = oldText.length; }
  if (start === end && newText.length < oldText.length && inputType.startsWith("delete")) {
    const removed = oldText.length - newText.length;
    if (inputType.toLowerCase().includes("backward")) start = Math.max(0, start - removed);
    else end = Math.min(oldText.length, end + removed);
  }
  let inserted = newText.length - (oldText.length - (end - start));
  if (inserted < 0 || oldText.slice(0, start) !== newText.slice(0, start) || oldText.slice(end) !== newText.slice(start + inserted)) {
    start = 0;
    while (start < oldText.length && start < newText.length && oldText[start] === newText[start]) start++;
    end = oldText.length;
    let newEnd = newText.length;
    while (end > start && newEnd > start && oldText[end - 1] === newText[newEnd - 1]) { end--; newEnd--; }
    inserted = newEnd - start;
  }
  const delta = inserted - (end - start);
  return refs.flatMap((ref) => {
    if (ref.end <= start) return [ref];
    if (ref.start >= end) return [{ ...ref, start: ref.start + delta, end: ref.end + delta }];
    return [];
  }).filter((ref) => newText.slice(ref.start, ref.end) === ref.label);
}

export function referencesForSend(text, refs) {
  const trimmed = text.trim();
  const leading = text.length - text.trimStart().length;
  const encoder = new TextEncoder();
  return refs.filter((r) => r.start >= leading && r.end <= leading + trimmed.length && text.slice(r.start, r.end) === r.label)
    .map((r) => ({ ...r, start: encoder.encode(text.slice(leading, r.start)).length, end: encoder.encode(text.slice(leading, r.end)).length }))
    .sort((a, b) => a.start - b.start);
}

export function referencesFromStored(text, refs) {
  const bytes = new TextEncoder().encode(text), decoder = new TextDecoder("utf-8", { fatal: true });
  return (Array.isArray(refs) ? refs : []).flatMap((r) => {
    try {
      const start = decoder.decode(bytes.slice(0, r.start)).length;
      const end = decoder.decode(bytes.slice(0, r.end)).length;
      return text.slice(start, end) === r.label ? [{ ...r, start, end }] : [];
    } catch { return []; }
  });
}
