# Architecture Decision Records (ADRs)

> **Audience**: anyone proposing or revising a significant technical decision in diting v2 — human maintainers, Claude, Codex, GPT-5.4, or any other reviewer.
>
> **Purpose**: this file is the writing guide for ADRs in this project. It exists because ADR 0001 shipped its first draft with two reviewable mistakes; the lessons from that revision live here so future ADRs start from a stronger baseline.

---

## What an ADR is (and is not)

An ADR records **one decision** with its **rationale**, **alternatives**, and **evidence**. It is optimised for the reader six months from now who wants to know:

1. What was decided?
2. Why was this decided and not one of the alternatives?
3. What evidence was available at the time?
4. Under what conditions should we re-evaluate?

An ADR is **not**:

- A design document (that lives in `docs/architecture.md`).
- A changelog (that lives in git).
- A taste vote ("I prefer X").
- A greenfield analysis of every possible option — it records the decision actually made and names the alternatives rejected.

---

## When to write an ADR

Write one when **any** of these is true:

- The decision is **hard to reverse** later (dependency choice, data format, wire protocol).
- The decision has **multiple defensible options** and the reasoning for picking one is not self-evident from the code.
- The decision is the result of **empirical testing** whose results you want to preserve (spike outcomes, benchmark wins).
- A **reviewer questioned** an earlier decision and the clarification is worth keeping (this is how ADR 0001 was born).
- A **dependency added** — new external library, new data source, new paid API.
- A **contract changed** — CLI flag renamed, output schema changed, config key renamed.

Do **not** write an ADR for:

- Bug fixes (git log is the record).
- Pure refactors that preserve semantics.
- Cosmetic / style changes.
- Decisions that are obviously right (only one reasonable option existed).

When in doubt, write one. A redundant ADR is cheap; a missing ADR is expensive.

---

## File layout and numbering

```
docs/adr/
├── README.md                           ← this file
├── 0001-utls-fetch-layer.md            ← first ADR, accepted
├── 0002-<next-slug>.md
└── ...
```

- Number sequentially, four digits, zero-padded. No gaps, no re-use.
- Slug is lowercase-kebab-case, descriptive enough that the filename hints at the decision.
- Never rename or renumber an existing ADR. If superseding, create a new one and mark the old one with status `Superseded by ADR NNNN`.

---

## Required sections

Every ADR must include these sections in this order. Use the template at the bottom of this file as the starting point.

### 1. Front matter

```markdown
# ADR NNNN — <one-line decision title>

**Status**: Proposed | Accepted | Deprecated | Superseded by ADR NNNN
**Date**: YYYY-MM-DD (of first commit; update in Revision note when revised)
**Decider**: <where the decision was made — spike output, benchmark run, discussion thread, etc.>
**Supersedes**: ADR NNNN (if applicable, otherwise omit)
```

### 2. Context

One to three paragraphs on the problem that made this decision necessary. Answer: *why are we deciding anything at all?* Link to the relevant architecture.md section if applicable.

### 3. Decision

The actual decision, stated plainly. Include concrete specifics (version numbers, constant names, file paths). Avoid hedging words ("probably", "might").

### 4. Alternatives considered

A table with columns `Alternative | Verdict | Reason`. Include the alternatives you rejected — **especially the ones a reasonable reviewer would propose**. If you only list the winners and the obvious losers, reviewers will assume you did not consider the middle ground.

Verdicts: `Accepted`, `Rejected`, `Deferred`, `Future evaluation`.

### 5. Evidence

The single most important section. Answer: *what data backs the decision?*

Every empirical claim must cite:

- How the data was collected (smoke test, benchmark, spike, production logs).
- Sample size (N URLs, M runs, K queries).
- Variance (stddev, min/max, median vs mean).
- Known limitations of the sample (what wasn't tested).
- Where the raw data lives (code path, git hash, log file).

**Single-run numbers are not evidence.** If you ran a test once and got 85 %, that is noise-prone. You need at least 3–5 runs to claim a success rate. See "Empirical hygiene" below.

### 6. Consequences

Positive and negative consequences of the decision. Include follow-up work required ("Phase 1 must implement X", "benchmark must validate Y"). This is where "trigger conditions for re-evaluation" live — explicit named conditions under which this ADR should be revisited.

### 7. Implementation notes

Short, concrete instructions for the engineer who implements this. Include gotchas you discovered during the spike ("do not use `X.Config.Y` — it is ignored by `X.DoSomething`"). Keep to actionable bullet points; this is not design documentation.

### 8. References

Links to: spike code, related PRs, upstream docs, relevant papers, external reviews. Every claim that is not common knowledge needs a reference.

### 9. Revision notes (only if revised)

When an ADR is revised after acceptance, add a numbered revision note at the bottom. **Do not silently rewrite the original text.** Document:

- What the original claimed.
- What was wrong with it.
- What prompted the revision (external review, new data, changed circumstances).
- What the new version decides and why.

See ADR 0001 §11 for an example.

---

## Key principles (lessons from ADR 0001)

These are the principles extracted from the ADR 0001 revision experience. Every ADR is expected to follow them.

### Principle 1 — "Sounds reasonable" is not a reason

ADR 0001 first draft picked `HelloChrome_120` as the production fingerprint. The reasoning was "Chrome 120 is a recent version I remember". That is not a reason — it is taste masquerading as rationale.

A proper decision comes from one of:

- **Empirical test**: "we ran it on 14 URLs, 8 times, and HelloChrome_Auto beats HelloChrome_120 by 4.4 pp on mean success rate."
- **Authoritative source**: "the upstream README recommends HelloChrome_Auto for new integrations."
- **Constraint satisfaction**: "HelloChrome_120 is the last version with feature X we depend on."

If you cannot name which category your reason falls into, the ADR is not ready to ship.

### Principle 2 — Test the alternatives a reviewer would propose

A reviewer should not discover an alternative you did not consider. Before writing the Alternatives section, spend 10 minutes brainstorming: "if I were a hostile reviewer, what would I push back on?" Then test or explicitly document why you did not test.

In ADR 0001, the missing alternatives were:
- Newer Chrome versions (HelloChrome_131, HelloChrome_133) — present in the same library
- `utls.NewRoller()` multi-fingerprint rotation — recommended by upstream

Both were knowable by skimming the utls source tree for 5 minutes. Neither was in the first draft. Both were surfaced by external review and required a full re-run.

**Lesson**: before writing "Alternatives considered", skim the source tree of your chosen dependency and its README. List everything that looks like a competing option, then decide which to test.

### Principle 3 — Invite external review, and take it seriously

Every non-trivial ADR should be reviewed by at least one independent party before being marked `Accepted`. Options:

- Another human who knows the domain.
- Codex CLI (see `.claude/skills/codex/`) for a second-opinion critique.
- GPT-5.4 or another LLM with a role prompt of "adversarial reviewer".

When the reviewer finds a gap, **do not minimise it**. Re-run the spike, get new data, and revise the ADR with a clear revision note. Silent fixes erode trust in the document.

### Principle 4 — Decisions have expiry conditions

Every ADR must state the conditions under which it should be revisited. Examples from ADR 0001:

- "If `chrome_auto` success rate drops more than 5 pp in a utls version bump, open a new ADR."
- "If we observe per-fingerprint sustained blocks, re-evaluate Roller."
- "On every utls version bump, re-run the spike."

Without trigger conditions, ADRs silently go stale. A stale ADR is worse than no ADR because it gives false confidence.

### Principle 5 — Revise transparently, never rewrite silently

When an ADR is wrong, the ADR's history is as important as its current content. Readers need to understand:

- What we used to believe.
- Why we believed it.
- What made us change our minds.
- What we believe now.

**Do not delete the old decision from the file.** Move it into §11 Revision note. Keep the git history of the ADR file clean — one commit per revision, with a descriptive message.

### Principle 6 — Preserve the mistakes

If your spike uncovered a bug (e.g., "I assumed `Config.X` overrides `Spec.X` but it does not"), **write that into the ADR explicitly**. Future implementers will make the same mistake otherwise.

ADR 0001 preserves the `utls.Config.NextProtos` → ClientHello ALPN override bug exactly so Phase 1 engineers (me or otherwise) cannot silently fall into it again.

---

## Empirical hygiene

When an ADR claims a number, that number must be defensible. This section is the minimum bar.

### For success-rate claims (e.g., "utls reaches 85 % of URLs")

- **Sample size**: at least 10 URLs, preferably covering known-easy, known-medium, and known-hard cases.
- **Runs**: at least 3 runs, preferably 5+. Report median if mean and median diverge.
- **Variance**: report stddev or (min, max) — never just the mean.
- **Outliers**: if one run is clearly an outlier (network-side issue, not method-side), explain why and either keep or drop it consistently across techniques.
- **Comparison**: include a baseline. "utls is 85 %" is meaningless without "net/http is X % on the same set".

### For latency claims (e.g., "chromedp takes 3 s per page")

- **Warm vs cold** must be stated. Cold-start latency is often 5–10× warm latency.
- **p95 / p99**, not just mean. Latency distributions have fat tails.
- **Sample size**: 100+ requests for a real number, 20+ for a smoke-test rough estimate.

### For cost claims (e.g., "one search costs $0.05")

- **Token counts** must be reported per LLM call (input + output + cached).
- **Model identity** at the time of measurement.
- **Date of measurement** (prices change).

### Things that do **not** count as evidence

- "I remember that X was slow."
- "Blog post from 2023 says Y."
- "GitHub issue mentions Z."
- "It feels like W."

These can *inspire* a spike, but the ADR must cite the spike that verified the claim, not the inspiration.

---

## How an ADR moves through states

```
Proposed  → author writes draft, requests review
    │
    ▼
Accepted  → review complete, decision committed to
    │
    ├─→ Deprecated    → decision no longer relevant (e.g., the component is gone)
    │
    └─→ Superseded by ADR NNNN → a newer ADR replaces this one
                                  (the old ADR is preserved, not deleted)
```

### State transition rules

- **Proposed → Accepted**: requires at least one external review and (if applicable) a successful spike or benchmark.
- **Accepted → Accepted (revised)**: when the same decision holds but details change. Add §N Revision note; do NOT create a new ADR.
- **Accepted → Superseded**: when a new ADR replaces the core decision. Mark the old ADR's status. Never delete.
- **Accepted → Deprecated**: when the decision's subject no longer exists (e.g., component removed).

When in doubt between "revise" and "supersede":
- If the core decision is the same and details changed → **revise**.
- If the core decision is replaced → **supersede**.

ADR 0001 was revised, not superseded: the core decision ("use utls") never changed; the details ("which ClientHelloID", "whether to use Roller") did.

---

## Review checklist (for reviewers)

When reviewing an ADR, walk through this list:

- [ ] Is the **decision** stated plainly without hedging?
- [ ] Are **at least 2 alternatives** named, with specific reasons for rejection?
- [ ] Is every **empirical claim** backed by a linked spike / benchmark / log?
- [ ] Does the evidence cite **sample size and variance**?
- [ ] Are **trigger conditions** for re-evaluation explicitly named?
- [ ] Does the ADR preserve any **bugs found during investigation**?
- [ ] Is the **revision policy** followed (no silent rewrites)?
- [ ] Could a new contributor **implement this decision in 30 minutes** from the implementation notes alone?
- [ ] Would **you** be convinced if you were hostile to the author's position?

If any checkbox is empty, flag it in review and push back before accepting.

---

## Template (copy this to start a new ADR)

````markdown
# ADR NNNN — <one-line decision title>

**Status**: Proposed
**Date**: YYYY-MM-DD
**Decider**: <spike / benchmark / review / discussion>
**Supersedes**: N/A

## Context

<2-3 paragraphs: what problem forced this decision? Why now? Link to architecture.md
section if relevant.>

## Decision

<Plain statement of what we are doing. Include specifics: version numbers, constant
names, file paths, CLI flags, config keys, etc. Avoid hedging.>

## Alternatives considered

| Alternative | Verdict | Reason |
|---|---|---|
| <option A> | Rejected | <specific reason, ideally with data> |
| <option B> | Rejected | <specific reason> |
| <option C> | Deferred | <trigger condition for reconsidering> |
| <option D — what a hostile reviewer would propose> | <verdict> | <reason> |

## Evidence

### Sample

<Describe the sample: N URLs / M queries / K users. What's in it, what's not.>

### Method

<How the data was collected. Spike path: `test/spike/<name>/main.go`. Commit hash if
relevant. Run command.>

### Results

| Technique / Option | Mean | Median | StdDev | Sample | Notes |
|---|---|---|---|---|---|
| <row per option> | | | | | |

### Interpretation

<What the data tells us. Acknowledge limitations: "this sample does not cover X, so
the decision may not hold under condition Y". Do not overclaim.>

## Consequences

### Positive

- <specific benefit>
- <specific benefit>

### Negative

- <specific cost / tradeoff>
- <what we are giving up>

### Follow-up required

- <work that must happen because of this decision>
- <Phase X task>

### Trigger conditions for re-evaluation

Re-open this ADR or write a new one if **any** of the following is observed:

- <specific threshold or event, e.g., "success rate drops below 70%">
- <specific dependency event, e.g., "library X releases 2.0">
- <specific schedule, e.g., "annually on 12-month anniversary">

## Implementation notes

<Bullet list of concrete instructions for the implementer. Include gotchas discovered
during the spike. Keep it actionable; this is not design docs.>

1. <instruction>
2. <instruction>
3. <gotcha: "do NOT do X, because Y">

## References

- Spike: `test/spike/<name>/`
- Upstream docs: <url>
- Related issues / PRs: <link>
- External review: <who reviewed, when>

<!-- Add revision notes below this line when the ADR is revised. -->
````

---

## Appendix: why this guide exists

ADR 0001 in its first draft (`21804aa`) claimed to record a decision but was really a post-hoc rationalisation of a casual choice. It picked `HelloChrome_120` because the author had heard of Chrome 120, not because 120 was the best available option. The Alternatives section missed the upstream recommendation (`Roller`) entirely. The Evidence section cited only 3 runs, enough for confidence but not enough to catch the outlier behaviour that the 8-run follow-up revealed.

An external review caught both gaps within one message. The revision required:

1. A 4-technique re-run of the spike (net/http, chrome120, chrome_auto, roller).
2. 8 independent runs to compute meaningful variance.
3. Rewriting the Decision, Alternatives, and Evidence sections.
4. Adding a Version selection rationale (§6) and a Multi-fingerprint strategy (§7).
5. A public Revision note (§11) preserving the original mistake.

The total cost of the revision was ~2 hours. The total cost of the first draft was ~20 minutes. **The first draft was cheap because it skipped the parts that mattered.** This README exists so future ADRs in this repo get the thorough version on the first pass.

If you are writing an ADR and tempted to skip the Evidence section because the decision "seems obvious" — re-read this appendix. Then write the Evidence section anyway.
