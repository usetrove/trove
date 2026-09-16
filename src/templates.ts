import { DEFAULT_CONFIG } from "./core/config";

export const INIT_FILES: Record<string, string> = {
  "project.md": `# Project

## Purpose

_What this repository is for._

## Architecture

_Main subsystems and how they connect._

## Conventions

- _

## Constraints

- _
`,
  "current-task.md": `# Current Task

## Objective

_TBD_

## Status

Idle

## Latest handoff

_None yet_

## Next action

_TBD_

## Relevant files

- _
`,
  "decisions.md": `# Decisions

Durable choices that should survive across sessions.

`,
  "rejected.md": `# Rejected Approaches

Approaches worth remembering so they are not retried.

`,
  "errata.md": `# Errata

Corrections and known inaccuracies in project memory.

`,
  "config.json": `${JSON.stringify(DEFAULT_CONFIG, null, 2)}\n`,
};

/** @deprecated Use INIT_FILES */
export const FILES = INIT_FILES;
