// Node.js runner for frontend test logic (no DOM needed for pure logic tests)

let passed = 0, failed = 0;
let currentSuite = null;
const suites = [];

function describe(name, fn) {
  currentSuite = { name, tests: [] };
  suites.push(currentSuite);
  fn();
  currentSuite = null;
}
function it(name, fn) { currentSuite.tests.push({ name, fn }); }

function expect(actual) {
  return {
    toBe(expected) {
      if (actual !== expected) throw new Error(`Expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
    },
    toContain(str) {
      if (!String(actual).includes(str)) throw new Error(`Expected "${actual}" to contain "${str}"`);
    },
    toNotContain(str) {
      if (String(actual).includes(str)) throw new Error(`Expected "${actual}" NOT to contain "${str}"`);
    },
    toBeTruthy() { if (!actual) throw new Error(`Expected truthy, got ${JSON.stringify(actual)}`); },
    toBeFalsy()  { if (actual)  throw new Error(`Expected falsy, got ${JSON.stringify(actual)}`); },
    toEqual(expected) {
      if (JSON.stringify(actual) !== JSON.stringify(expected))
        throw new Error(`Expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
    },
  };
}

const navigator = { onLine: true };

// ── Pure helpers mirrored from app.js / tests.html ────────────────────────────

function friendlyError(raw, status) {
  if (!navigator.onLine) return "You're offline — check your connection.";
  if (status === 413) return "Image is too large. Try a smaller screenshot (max 20 MB).";
  if (status === 400) return "Couldn't read that image. Try saving it as PNG and uploading again.";
  if (status === 405) return "Unexpected request. Please refresh the page.";
  if (status >= 500)  return "Server ran into a problem. Try again in a moment.";
  const msg = typeof raw === 'string' ? raw.toLowerCase() : '';
  if (msg.includes('api error')) return "The AI service hit an issue. Try again shortly.";
  if (msg.includes('fetch') || msg.includes('network') || msg.includes('failed to fetch'))
    return "Can't reach the server. Is GhostOperator still running?";
  return "Something went wrong — please try again.";
}

function parseSSEChunks(rawText) {
  const results = [];
  for (const line of rawText.split('\n')) {
    const trimmed = line.trim();
    if (!trimmed.startsWith('data: ')) continue;
    const data = trimmed.slice(6).trim();
    if (data === '[DONE]') { results.push({ done: true }); continue; }
    try { results.push(JSON.parse(data)); } catch { /* skip malformed */ }
  }
  return results;
}

function isImageFile(file) {
  return file && file.type.startsWith('image/');
}

// ── Test suites ───────────────────────────────────────────────────────────────

describe('friendlyError — HTTP status codes', () => {
  it('maps 413 to file-too-large message', () => {
    expect(friendlyError('body too large', 413)).toContain('too large');
  });
  it('maps 400 to unreadable image message', () => {
    expect(friendlyError('bad request', 400)).toContain("Couldn't read");
  });
  it('maps 405 to refresh message', () => {
    expect(friendlyError('method not allowed', 405)).toContain('refresh');
  });
  it('maps 500 to server problem message', () => {
    expect(friendlyError('internal error', 500)).toContain('Server ran into');
  });
  it('maps 503 (>=500) to server problem message', () => {
    expect(friendlyError('service unavailable', 503)).toContain('Server ran into');
  });
  it('never leaks the raw HTTP status number to user', () => {
    expect(friendlyError('internal error', 500)).toNotContain('500');
  });
});

describe('friendlyError — network / AI errors', () => {
  it('maps "failed to fetch" to server-unreachable message', () => {
    expect(friendlyError('Failed to fetch')).toContain("Can't reach");
  });
  it('maps "network error" to server-unreachable message', () => {
    expect(friendlyError('network error')).toContain("Can't reach");
  });
  it('maps "api error" to AI service message', () => {
    expect(friendlyError('API error 429: quota exceeded')).toContain('AI service');
  });
  it('has a safe fallback for unknown errors', () => {
    const msg = friendlyError('something totally unknown');
    expect(msg).toBeTruthy();
    expect(msg).toNotContain('unknown');
  });
});

describe('parseSSEChunks — SSE stream parsing', () => {
  it('parses a valid text chunk', () => {
    const c = parseSSEChunks('data: {"text":"hello"}\n');
    expect(c.length).toBe(1);
    expect(c[0].text).toBe('hello');
  });
  it('parses multiple chunks', () => {
    const c = parseSSEChunks('data: {"text":"A"}\ndata: {"text":"B"}\ndata: {"text":"C"}\n');
    expect(c.length).toBe(3);
    expect(c.map(x => x.text).join('')).toBe('ABC');
  });
  it('detects [DONE] sentinel', () => {
    const c = parseSSEChunks('data: {"text":"hi"}\ndata: [DONE]\n');
    expect(c[c.length - 1].done).toBe(true);
  });
  it('skips lines without "data: " prefix', () => {
    const c = parseSSEChunks('event: ping\n: comment\ndata: {"text":"ok"}\n');
    expect(c.length).toBe(1);
    expect(c[0].text).toBe('ok');
  });
  it('skips malformed JSON without crashing', () => {
    const c = parseSSEChunks('data: {broken json}\ndata: {"text":"fine"}\n');
    expect(c.length).toBe(1);
    expect(c[0].text).toBe('fine');
  });
  it('returns empty array for empty input', () => {
    expect(parseSSEChunks('').length).toBe(0);
  });
  it('parses error events from the stream', () => {
    const c = parseSSEChunks('data: {"error":"something went wrong"}\n');
    expect(c[0].error).toBe('something went wrong');
  });
});

describe('isImageFile — file type validation', () => {
  it('accepts image/png',       () => { expect(isImageFile({ type: 'image/png' })).toBeTruthy(); });
  it('accepts image/jpeg',      () => { expect(isImageFile({ type: 'image/jpeg' })).toBeTruthy(); });
  it('accepts image/webp',      () => { expect(isImageFile({ type: 'image/webp' })).toBeTruthy(); });
  it('rejects application/pdf', () => { expect(isImageFile({ type: 'application/pdf' })).toBeFalsy(); });
  it('rejects text/plain',      () => { expect(isImageFile({ type: 'text/plain' })).toBeFalsy(); });
  it('rejects null',            () => { expect(isImageFile(null)).toBeFalsy(); });
});

describe('Toast — structure', () => {
  // DOM-free: test the data model that would produce the toast element
  function makeToastData(message, type) {
    return { classes: ['toast', `toast-${type}`], text: message, closeLabel: '✕' };
  }
  it('error toast has toast-error class', () => {
    const t = makeToastData('oops', 'error');
    expect(t.classes.includes('toast-error')).toBeTruthy();
  });
  it('success toast has toast-success class', () => {
    const t = makeToastData('done', 'success');
    expect(t.classes.includes('toast-success')).toBeTruthy();
  });
  it('info toast has toast-info class', () => {
    const t = makeToastData('note', 'info');
    expect(t.classes.includes('toast-info')).toBeTruthy();
  });
  it('displays the correct message text', () => {
    const t = makeToastData('Something went wrong', 'error');
    expect(t.text).toBe('Something went wrong');
  });
  it('has a dismiss button labelled ✕', () => {
    const t = makeToastData('msg', 'error');
    expect(t.closeLabel).toBe('✕');
  });
});

// ── Run & report ──────────────────────────────────────────────────────────────

const R = '\x1b[0m', G = '\x1b[32m', RED = '\x1b[31m', DIM = '\x1b[2m', B = '\x1b[1m';

for (const suite of suites) {
  console.log(`\n  ${DIM}${suite.name}${R}`);
  for (const test of suite.tests) {
    let err = null;
    try { test.fn(); } catch (e) { err = e; }
    if (!err) { passed++; console.log(`    ${G}✓${R} ${test.name}`); }
    else       { failed++; console.log(`    ${RED}✗ ${test.name}${R}\n      ${RED}↳ ${err.message}${R}`); }
  }
}

const total = passed + failed;
console.log(`\n  ${B}${failed === 0 ? G : RED}${passed}/${total} passed${failed > 0 ? ` · ${failed} failed` : ' · all green'}${R}\n`);
process.exit(failed > 0 ? 1 : 0);
