# GhostOperator — Next.js drop-in

Copy this folder into your Next.js (App Router) project. The route lives at
`/ghost-operator`.

```
your-next-app/
└── app/
    └── ghost-operator/
        ├── page.jsx
        └── ghost-operator.css
```

That's it — visit `/ghost-operator`. Everything else below is optional polish.

---

## 1. Fonts (recommended)

The design uses **Geist**, **Geist Mono**, and **Instrument Serif**. The CSS
falls back to system fonts if these aren't loaded, but it looks much better
with the real ones. In your **root layout** (`app/layout.jsx`):

```jsx
import { Geist, Geist_Mono, Instrument_Serif } from "next/font/google";

const geist = Geist({ subsets: ["latin"], variable: "--font-geist" });
const geistMono = Geist_Mono({ subsets: ["latin"], variable: "--font-geist-mono" });
const instrumentSerif = Instrument_Serif({
  subsets: ["latin"], weight: "400", style: ["normal", "italic"],
  variable: "--font-instrument-serif",
});

export default function RootLayout({ children }) {
  return (
    <html lang="en" className={`${geist.variable} ${geistMono.variable} ${instrumentSerif.variable}`}>
      <body>{children}</body>
    </html>
  );
}
```

Then in `ghost-operator.css`, swap the three font-family declarations at the
top to use the variables:

```css
--mono:  var(--font-geist-mono), ui-monospace, SFMono-Regular, Menlo, monospace;
--sans:  var(--font-geist),      ui-sans-serif, system-ui, -apple-system, sans-serif;
--serif: var(--font-instrument-serif), ui-serif, Georgia, serif;
```

Or skip `next/font` entirely and add a `<link rel="stylesheet">` in your root
layout pointing at Google Fonts.

---

## 2. Wire up the backend

The component has two stubbed integration points, both marked with
`TODO(api):` in `page.jsx`.

### `handleFile(file)` — OCR an uploaded screenshot

Replace the stubbed body with a call to your OCR endpoint:

```jsx
const handleFile = async (file) => {
  if (!file?.type.startsWith("image/")) return;
  setSourceImage(file);
  setSourceImageUrl(URL.createObjectURL(file));

  const fd = new FormData();
  fd.append("image", file);
  const res = await fetch("/api/ocr", { method: "POST", body: fd });
  const { thread } = await res.json(); // [{who, text, t}]
  setParsedThread(thread);
};
```

### `generate()` — draft candidate replies

Swap the staggered-timeout stub for a real call. A streaming pattern:

```jsx
const res = await fetch("/api/draft-replies", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({
    thread: parsedThread,
    tones: requested,
    length,
    context,
    model: model.id,
  }),
});
const reader = res.body.getReader();
// ... decode SSE / NDJSON and push tokens into the matching candidate slot
```

The candidate object shape the UI expects:
```ts
{
  id: string,
  tone: string,
  text: string,
  confidence: number,   // 0..1
  tokens: number,
  latency: string,      // "0.62"
  streaming?: boolean,  // true while loading; UI shows skeleton
}
```

---

## 3. TypeScript

If your project is TS, rename `page.jsx` → `page.tsx`. The component is
plain JSX with inferred types — it'll compile as-is, or you can tighten up
the `useState` calls with explicit types (`useState<Candidate[]>([])` etc).

---

## 4. Styling

`ghost-operator.css` is scoped to a `.go-root` wrapper, so it won't leak
into the rest of your app. Theme variants are toggled via `data-theme` on
that wrapper:

```jsx
<div className="go-root" data-theme="phosphor" data-density="regular">
```

Available themes: `phosphor` (default), `amber`, `cyan`, `violet`.
Density: `regular`, `compact`.

If you want users to switch themes at runtime, lift `data-theme` into state.

---

## 5. What was stripped vs. the prototype

- ✂ Tweaks panel (was preview-environment scaffolding)
- ✂ Demo "scenario" presets and the mock chat thread
- ✂ Recent intercepts history list (re-add when you have a real store)
- ✂ `CANDIDATE_BANK` fake reply data

Everything else — layout, states, interactions, animations — is intact.
