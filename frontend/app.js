'use strict';

// ── DOM refs ──────────────────────────────────────────────────────────────────

const dropZone      = document.getElementById('drop-zone');
const fileInput     = document.getElementById('file-input');
const browseBtn     = document.getElementById('browse-btn');
const removeBtn     = document.getElementById('remove-btn');
const preview       = document.getElementById('preview');
const fileName      = document.getElementById('file-name');
const generateBtn   = document.getElementById('generate-btn');
const clearBtn      = document.getElementById('clear-btn');
const outputSection = document.getElementById('output-section');
const replyBox      = document.getElementById('reply-box');
const copyBtn       = document.getElementById('copy-btn');
const toastContainer = document.getElementById('toast-container');

const modelWrap  = document.getElementById('model-wrap');
const modelBtn   = document.getElementById('model-btn');
const modelMenu  = document.getElementById('model-menu');
const modelIcon  = document.getElementById('model-icon');
const modelName  = document.getElementById('model-name');

const contextPanel  = document.getElementById('context-panel');
const contextToggle = document.getElementById('context-toggle');
const contextBody   = document.getElementById('context-body');
const contextInput  = document.getElementById('context-input');
const contextCount  = document.getElementById('context-count');

const tokenWrap  = document.getElementById('token-wrap');
const tokenBar   = document.getElementById('token-bar');
const tokenFill  = document.getElementById('token-fill');
const tokenLabel = document.getElementById('token-label');

// ── State ─────────────────────────────────────────────────────────────────────

let currentFile    = null;
let currentModelId = 'gemini-2.5-flash';
let currentModelRPM = 10;

// RPM tracking for near-limit warning
const reqTimestamps = [];
let rpmWarnedAt = 0;

// Rate-limit cooldown
let rateLimitUntil = 0;

// ── Model selector ────────────────────────────────────────────────────────────

const modelOptions = document.querySelectorAll('.model-option');

function initModel() {
  const first = document.querySelector('.model-option');
  if (first) selectModel(first, false);
}

function selectModel(el, closeMenu = true) {
  currentModelId  = el.dataset.model;
  currentModelRPM = parseInt(el.dataset.rpm, 10) || 10;

  modelIcon.textContent = el.dataset.icon;
  modelName.textContent = el.querySelector('.mo-name').textContent;

  modelOptions.forEach(o => o.classList.toggle('selected', o === el));

  if (closeMenu) closeModelMenu();
}

function openModelMenu() {
  modelMenu.hidden = false;
  modelBtn.setAttribute('aria-expanded', 'true');
  modelWrap.classList.add('open');
}

function closeModelMenu() {
  modelMenu.hidden = true;
  modelBtn.setAttribute('aria-expanded', 'false');
  modelWrap.classList.remove('open');
}

modelBtn.addEventListener('click', (e) => {
  e.stopPropagation();
  modelMenu.hidden ? openModelMenu() : closeModelMenu();
});

modelOptions.forEach(opt => {
  opt.addEventListener('click', () => selectModel(opt));
});

document.addEventListener('click', (e) => {
  if (!modelWrap.contains(e.target)) closeModelMenu();
});

document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') closeModelMenu();
});

// Quick-switch to next model (called from rate-limit toast)
function switchToNextModel() {
  const opts = [...modelOptions];
  const idx  = opts.findIndex(o => o.dataset.model === currentModelId);
  const next = opts[(idx + 1) % opts.length];
  selectModel(next, true);
  toast(`Switched to ${next.querySelector('.mo-name').textContent}.`, 'info', 3000);
}

initModel();

// ── Context panel ─────────────────────────────────────────────────────────────

contextToggle.addEventListener('click', () => {
  const open = contextPanel.classList.toggle('open');
  contextToggle.setAttribute('aria-expanded', open);
  contextBody.setAttribute('aria-hidden', !open);
  if (open) setTimeout(() => contextInput.focus(), 280);
});

contextInput.addEventListener('input', () => {
  contextCount.textContent = `${contextInput.value.length} / 300`;
});

// ── Browse / file picker ──────────────────────────────────────────────────────

browseBtn.addEventListener('click', (e) => {
  e.stopPropagation();
  fileInput.click();
});

fileInput.addEventListener('change', () => {
  const file = fileInput.files[0];
  if (file) setImage(file);
  fileInput.value = '';
});

// ── Drag and drop ─────────────────────────────────────────────────────────────

dropZone.addEventListener('dragover', (e) => {
  e.preventDefault();
  dropZone.classList.add('drag-over');
});

dropZone.addEventListener('dragleave', (e) => {
  if (!dropZone.contains(e.relatedTarget)) dropZone.classList.remove('drag-over');
});

dropZone.addEventListener('drop', (e) => {
  e.preventDefault();
  dropZone.classList.remove('drag-over');
  const file = e.dataTransfer.files[0];
  if (!file) return;
  if (!file.type.startsWith('image/')) {
    toast('Only image files are supported (PNG, JPG, WebP).', 'error');
    return;
  }
  setImage(file);
});

// ── Paste ─────────────────────────────────────────────────────────────────────

document.addEventListener('paste', (e) => {
  const items = e.clipboardData?.items;
  if (!items) return;
  for (const item of items) {
    if (item.type.startsWith('image/')) {
      const file = item.getAsFile();
      if (file) setImage(file);
      break;
    }
  }
});

// ── Remove ────────────────────────────────────────────────────────────────────

removeBtn.addEventListener('click', (e) => {
  e.stopPropagation();
  reset();
});

// ── Set image ─────────────────────────────────────────────────────────────────

function setImage(file) {
  currentFile = file;
  preview.src = URL.createObjectURL(file);
  fileName.textContent = file.name || 'screenshot.png';
  dropZone.classList.add('has-image');
  generateBtn.disabled = false;
  clearOutput();
}

// ── Generate ──────────────────────────────────────────────────────────────────

generateBtn.addEventListener('click', generate);

document.addEventListener('keydown', (e) => {
  if ((e.ctrlKey || e.metaKey) && e.key === 'Enter' && !generateBtn.disabled) generate();
});

async function generate() {
  if (!currentFile) return;

  // Check if still in rate-limit cooldown
  const cooldown = Math.ceil((rateLimitUntil - Date.now()) / 1000);
  if (cooldown > 0) {
    toast(`Still rate-limited — wait ${cooldown}s or switch models.`, 'warning', 4000);
    return;
  }

  trackRPM();
  setGenerating(true);
  clearOutput();
  showOutput();
  showTokenBar(true);

  const formData = new FormData();
  formData.append('screenshot', currentFile, currentFile.name || 'screenshot.png');
  formData.append('model', currentModelId);
  const prompt = contextInput.value.trim();
  if (prompt) formData.append('custom_prompt', prompt);

  try {
    const response = await fetch('/reply', { method: 'POST', body: formData });

    if (!response.ok) {
      const text = await response.text();
      showTokenBar(false);
      toast(friendlyError(text, response.status));
      hideOutput();
      return;
    }

    replyBox.classList.add('streaming');
    const ok = await streamResponse(response);
    replyBox.classList.remove('streaming');
    if (ok) copyBtn.disabled = false;

  } catch (err) {
    showTokenBar(false);
    toast(friendlyError(err.message));
    hideOutput();
  } finally {
    setGenerating(false);
  }
}

async function streamResponse(response) {
  const reader  = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;

    buffer += decoder.decode(value, { stream: true });
    const lines = buffer.split('\n');
    buffer = lines.pop();

    for (const line of lines) {
      if (!line.startsWith('data: ')) continue;
      const data = line.slice(6).trim();
      if (data === '[DONE]') return true;

      let parsed;
      try { parsed = JSON.parse(data); } catch { continue; }

      if (parsed.errCode === 'RATE_LIMIT') {
        showTokenBar(false);
        handleRateLimit(parsed.retryAfter || 60);
        return false;
      }
      if (parsed.error) {
        showTokenBar(false);
        toast(friendlyError(parsed.error));
        return false;
      }
      if (parsed.tokens != null) {
        animateTokenBar(parsed.tokens, parsed.limit);
        continue;
      }
      if (parsed.text) {
        replyBox.textContent += parsed.text;
      }
    }
  }
  return true;
}

// ── Rate-limit handling ───────────────────────────────────────────────────────

function handleRateLimit(retryAfter) {
  rateLimitUntil = Date.now() + retryAfter * 1000;

  const mins  = Math.floor(retryAfter / 60);
  const secs  = retryAfter % 60;
  const wait  = mins > 0 ? `${mins}m ${secs}s` : `${secs}s`;

  // Custom toast with embedded Switch Model button
  const el    = document.createElement('div');
  el.className = 'toast toast-error';
  el.setAttribute('role', 'alert');

  const msg = document.createElement('span');
  msg.textContent = `Rate limit hit — retry in ${wait}.`;

  const switchBtn = document.createElement('button');
  switchBtn.className = 'toast-action';
  switchBtn.textContent = 'Switch Model';
  switchBtn.addEventListener('click', () => {
    switchToNextModel();
    dismissToast(el);
  });

  const closeBtn = document.createElement('button');
  closeBtn.className = 'toast-close';
  closeBtn.textContent = '✕';
  closeBtn.addEventListener('click', () => dismissToast(el));

  el.appendChild(msg);
  el.appendChild(switchBtn);
  el.appendChild(closeBtn);
  toastContainer.appendChild(el);
  requestAnimationFrame(() => requestAnimationFrame(() => el.classList.add('toast-show')));
  el.dataset.timer = setTimeout(() => dismissToast(el), 10000);
}

// ── RPM tracking & near-limit warning ────────────────────────────────────────

function trackRPM() {
  const now = Date.now();
  reqTimestamps.push(now);

  // Prune entries older than 60s
  const cutoff = now - 60_000;
  while (reqTimestamps.length && reqTimestamps[0] < cutoff) reqTimestamps.shift();

  const ratio = reqTimestamps.length / currentModelRPM;

  // Warn once per 30s when at ≥ 80% of RPM
  if (ratio >= 0.8 && ratio < 1.0 && now - rpmWarnedAt > 30_000) {
    rpmWarnedAt = now;
    toast(
      `${reqTimestamps.length} of ${currentModelRPM} requests used this minute — nearing the rate limit.`,
      'warning',
      6000
    );
  }
}

// ── Token bar ─────────────────────────────────────────────────────────────────

function showTokenBar(streaming) {
  tokenWrap.classList.remove('hidden');
  if (streaming) {
    tokenFill.style.width = '0%';
    tokenFill.classList.add('streaming');
    tokenLabel.textContent = 'generating…';
  }
}

function animateTokenBar(used, limit) {
  tokenFill.classList.remove('streaming');
  const pct = Math.min((used / limit) * 100, 100);
  tokenFill.style.width = pct + '%';

  // Colour hint: green → amber → red as it fills
  if (pct >= 90) tokenFill.classList.add('token-high');
  else if (pct >= 65) tokenFill.classList.add('token-mid');

  tokenLabel.textContent = `${used} / ${limit} tokens`;
}

// ── Clear ─────────────────────────────────────────────────────────────────────

clearBtn.addEventListener('click', reset);

function reset() {
  currentFile = null;
  preview.src = '';
  fileName.textContent = '';
  dropZone.classList.remove('has-image', 'drag-over');
  generateBtn.disabled = true;
  clearOutput();
  hideOutput();
}

// ── Copy ──────────────────────────────────────────────────────────────────────

copyBtn.addEventListener('click', async () => {
  const text = replyBox.textContent.trim();
  if (!text) return;
  try {
    await navigator.clipboard.writeText(text);
    copyBtn.textContent = 'Copied!';
    copyBtn.classList.add('copied');
    setTimeout(() => { copyBtn.textContent = 'Copy'; copyBtn.classList.remove('copied'); }, 1500);
  } catch {
    toast('Could not copy — try selecting the text manually.', 'error');
  }
});

// ── Toast ─────────────────────────────────────────────────────────────────────

function toast(message, type = 'error', duration = 4500) {
  console.error('[GhostOperator]', message);

  const el = document.createElement('div');
  el.className = `toast toast-${type}`;
  el.setAttribute('role', 'alert');

  const text = document.createElement('span');
  text.textContent = message;

  const close = document.createElement('button');
  close.className = 'toast-close';
  close.textContent = '✕';
  close.addEventListener('click', () => dismissToast(el));

  el.appendChild(text);
  el.appendChild(close);
  toastContainer.appendChild(el);

  requestAnimationFrame(() => requestAnimationFrame(() => el.classList.add('toast-show')));
  el.dataset.timer = setTimeout(() => dismissToast(el), duration);
}

function dismissToast(el) {
  clearTimeout(el.dataset.timer);
  el.classList.remove('toast-show');
  el.addEventListener('transitionend', () => el.remove(), { once: true });
}

// ── Friendly error messages ───────────────────────────────────────────────────

function friendlyError(raw, status) {
  console.error('[GhostOperator error]', status ? `HTTP ${status}:` : '', raw);

  if (!navigator.onLine) return "You're offline — check your connection.";
  if (status === 413)    return "Image is too large. Try a smaller screenshot (max 20 MB).";
  if (status === 400)    return "Couldn't read that image. Try saving it as PNG and uploading again.";
  if (status === 405)    return "Unexpected request. Please refresh the page.";
  if (status >= 500)     return "Server ran into a problem. Try again in a moment.";

  const msg = typeof raw === 'string' ? raw.toLowerCase() : '';
  if (msg.includes('api error'))   return "The AI service hit an issue. Try again shortly.";
  if (msg.includes('fetch') || msg.includes('network') || msg.includes('failed to fetch'))
    return "Can't reach the server. Is GhostOperator still running?";

  return "Something went wrong — please try again.";
}

// ── Helpers ───────────────────────────────────────────────────────────────────

function setGenerating(on) {
  generateBtn.disabled = on;
  clearBtn.disabled    = on;
  generateBtn.innerHTML = on
    ? '<span class="spinner"></span>Generating…'
    : 'Generate Reply';
}

function showOutput()  { outputSection.classList.add('visible'); }
function hideOutput()  { outputSection.classList.remove('visible'); }

function clearOutput() {
  replyBox.textContent = '';
  replyBox.classList.remove('streaming', 'error');
  copyBtn.disabled = true;
  copyBtn.textContent = 'Copy';
  copyBtn.classList.remove('copied');
  tokenWrap.classList.add('hidden');
  tokenFill.style.width = '0%';
  tokenFill.className = 'token-fill';
  tokenLabel.textContent = '— tokens';
}
