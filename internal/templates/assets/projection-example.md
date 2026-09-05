## Markdown and HTML skeleton

Generate `projection.md` in this shape:

````markdown
# Implement execution review

> Human-review projection. The phase issue bodies and typed artifacts remain authoritative.

## Review summary

**Recommendation:** ...

**Decision requested:** ...

## Decisions and evidence

- **Decision needed:** ... ([source](...))
- **Confirmed:** ... ([source](...))
- **Planning estimate:** ...; confidence: medium; basis: ...

```html-preview id=implement-execution-review version=1 title="Implement execution review" height=720
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Implement execution review</title>
  <style>
    :root {
      color-scheme: light;
      --ink: #172033; --muted: #607086; --line: #d9e0ea;
      --surface: #fff; --soft: #f5f7fb; --decision: #4056b4;
      --settled: #187252; --evidence: #9a6500; --blocked: #a93636;
    }
    * { box-sizing: border-box; }
    body { margin: 0; padding: 20px; color: var(--ink); background: var(--surface);
      font: 15px/1.5 system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
    main { width: min(100%, 1080px); margin: auto; }
    .summary, .card { border: 1px solid var(--line); border-radius: 14px; padding: 16px; }
    .grid { display: grid; grid-template-columns: repeat(12, 1fr); gap: 12px; }
    .card { grid-column: span 6; background: var(--soft); }
    .decision { border-left: 5px solid var(--decision); }
    .settled { border-left: 5px solid var(--settled); }
    .estimate { border-style: dashed; color: var(--muted); }
    button, input, textarea { font: inherit; }
    button:focus-visible, input:focus-visible, textarea:focus-visible,
    summary:focus-visible, a:focus-visible { outline: 3px solid #8da2ff; outline-offset: 2px; }
    @media (max-width: 700px) { body { padding: 12px; } .card { grid-column: 1 / -1; } }
    @media (prefers-reduced-motion: reduce) { *, *::before, *::after {
      animation-duration: .01ms !important; transition-duration: .01ms !important; } }
  </style>
</head>
<body>
  <main>
    <section class="summary" aria-labelledby="summary-title">
      <h1 id="summary-title">Implement execution review</h1>
      <p>Who is affected, the situation they are in, and the observable outcome this plan must deliver.</p>
    </section>
    <section aria-labelledby="case-title">
      <h2 id="case-title">Concrete case walkthrough</h2>
      <div class="grid">
        <article class="card"><h3>What the person sees</h3><p>...</p></article>
        <article class="card"><h3>What the system does</h3><p>...</p></article>
      </div>
      <p><strong>Reviewer verifies:</strong> ...</p>
    </section>
    <section aria-labelledby="attention-title">
      <h2 id="attention-title">Needs your attention</h2>
      <div class="grid">
        <article class="card decision"><h3>Decision needed</h3><p>...</p></article>
        <article class="card settled"><h3>Confirmed constraint</h3><p>...</p></article>
        <article class="card estimate"><h3>Planning estimate</h3><p>...</p></article>
      </div>
    </section>
    <!-- Add the phase model, drill-down, and source links. -->
  </main>
  <script>
    // Add only local presentation state (filters, tabs, accordions, DAG focus).
  </script>
</body>
</html>
```
````
