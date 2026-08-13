# Issue tracker: Local Markdown

Issues and specs for this repository live as Markdown files in `.scratch/`.

## Conventions

- One feature per directory: `.scratch/<feature-slug>/`.
- The spec is `.scratch/<feature-slug>/spec.md`.
- Implementation issues are numbered files under `.scratch/<feature-slug>/issues/`, starting with `01`.
- Triage state is recorded as a `Status:` line near the top of each issue.
- Comments and conversation history are appended under a `## Comments` heading.

## Publishing and fetching

When a skill publishes to the issue tracker, create the corresponding file under `.scratch/<feature-slug>/`. When a skill fetches a ticket, read the referenced local Markdown file.

## Wayfinding

- Map: `.scratch/<effort>/map.md`.
- Child ticket: `.scratch/<effort>/issues/NN-<slug>.md`.
- Ticket metadata uses `Type:`, `Status:`, and optional `Blocked by:` lines.
- A ticket is unblocked when every referenced blocker is resolved.
- Claim a ticket by setting `Status: claimed`; resolve it by adding an `## Answer`, setting `Status: resolved`, and updating the map.
