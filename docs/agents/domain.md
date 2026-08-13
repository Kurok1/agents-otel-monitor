# Domain Docs

This repository uses a single-context domain-document layout.

## Before exploring

Read the following when they exist:

- `CONTEXT.md` at the repository root.
- ADRs under `docs/adr/` that apply to the area being changed.

If these files do not exist, proceed silently. Domain-modeling workflows create them only when terminology or a durable decision needs to be recorded.

## Layout

```text
/
├── CONTEXT.md
└── docs/adr/
```

## Vocabulary and decisions

- Use the glossary's established names for domain concepts.
- Reconsider terminology that is not present in the glossary before inventing a synonym.
- Explicitly surface any change that contradicts an existing ADR instead of silently overriding it.
