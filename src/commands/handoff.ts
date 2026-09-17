import { unlink } from "node:fs/promises";
import path from "node:path";
import { isTroveInitialized, readTroveConfig } from "../core/config";
import { requireGitRepository } from "../core/git";
import { getTrovePaths } from "../core/paths";
import { confirm, isInteractive } from "../core/prompt";
import { autoDraftToInput, buildAutoDraft } from "../handoff/autoDraft";
import { buildHandoffDocument } from "../handoff/buildDraft";
import { collectHandoffEvidence } from "../handoff/collectEvidence";
import { displayFoundSummary } from "../handoff/collectGitContext";
import { reviewAndApplyDurableRecords } from "../handoff/proposeDurableRecords";
import { openAndReadEditedDraft } from "../handoff/renderPreview";
import {
  confirmSave,
  reviewDraftInteractively,
} from "../handoff/promptUser";
import { saveSessionHandoff, writeDraftHandoff } from "../handoff/saveSession";
import { updateCurrentTask } from "../handoff/updateCurrentTask";
import {
  confirmWarnings,
  validateHandoffInput,
} from "../handoff/validateDraft";
import { renderHandoffMarkdown, renderReviewMarkdown } from "../templates/handoff";
import type { HandoffInput } from "../types/handoff";

export interface HandoffCliOptions {
  yes?: boolean;
  next?: string;
  nextArg?: string;
  objective?: string;
  decisions?: string;
  rejected?: string;
  skipDurable?: boolean;
  transcript?: string;
  /** Level-1 fallback: force the old full questionnaire (not default). */
  manual?: boolean;
}

export async function runHandoff(
  cwd = process.cwd(),
  options: HandoffCliOptions = {},
): Promise<void> {
  const repoRoot = await requireGitRepository(cwd);
  const paths = getTrovePaths(repoRoot);

  if (!isTroveInitialized(paths)) {
    console.error("Trove has not been initialized in this repository.");
    console.error("");
    console.error("Run:");
    console.error("  trove init");
    process.exitCode = 1;
    return;
  }

  await readTroveConfig(paths.config);
  const evidence = await collectHandoffEvidence(paths, {
    transcript: options.transcript,
  });
  const autoDraft = buildAutoDraft(evidence);

  displayFoundSummary(evidence.git, autoDraft.objective.text);
  if (evidence.session) {
    console.log(
      `Session context: ${evidence.session.filesExplored.length} file(s), ${evidence.session.keyFindings.length} finding(s) from transcript`,
    );
    console.log("");
  } else if (options.transcript?.trim() || process.env.TROVE_TRANSCRIPT) {
    console.log("Session context: transcript provided but no messages parsed.");
    console.log("");
  }

  if (evidence.git.branch === "main" || evidence.git.branch === "master") {
    console.log(`Warning: Current branch is \`${evidence.git.branch}\`.`);
    console.log("");
  }

  const nextFromCli = options.next?.trim() || options.nextArg?.trim() || "";
  const nonInteractive = Boolean(options.yes) || !isInteractive();

  let input: HandoffInput;
  let openEditor = false;

  if (nonInteractive) {
    if (!nextFromCli) {
      console.error("Non-interactive handoff requires --next (or a next-action argument).");
      process.exitCode = 1;
      return;
    }
    input = autoDraftToInput(autoDraft, nextFromCli, {
      objective: options.objective,
      decisions: options.decisions,
      rejectedApproaches: options.rejected,
    });
    console.log("");
    console.log(renderReviewMarkdown(autoDraft).trimEnd());
    console.log("");
  } else {
    const review = await reviewDraftInteractively({
      draft: autoDraft,
      nextFromCli,
    });
    if (review.action === "cancel") {
      console.log("No files were changed.");
      return;
    }
    input = review.input;
    if (options.objective?.trim()) input.objective = options.objective.trim();
    if (options.decisions?.trim()) input.decisions = options.decisions.trim();
    if (options.rejected?.trim()) input.rejectedApproaches = options.rejected.trim();
    openEditor = review.action === "edit";
  }

  const validation = validateHandoffInput(input);
  if (validation.errors.length > 0) {
    for (const error of validation.errors) console.error(`✖ ${error}`);
    console.error("No files were changed.");
    process.exitCode = 1;
    return;
  }

  if (!nonInteractive && !(await confirmWarnings(validation.warnings))) {
    console.log("No files were changed.");
    return;
  }
  if (nonInteractive) {
    for (const warning of validation.warnings) console.log(`Warning: ${warning}`);
  }

  if (!nonInteractive && !openEditor) {
    const ok = await confirmSave();
    if (!ok) {
      console.log("No files were changed.");
      return;
    }
  }

  const document = buildHandoffDocument({
    gitContext: evidence.git,
    input,
    relevantFiles: autoDraft.relevantFiles,
    autoDraft,
  });

  let markdown = renderHandoffMarkdown(document);
  const draftPath = await writeDraftHandoff(paths.sessions, document.id, markdown);

  if (openEditor) {
    markdown = await openAndReadEditedDraft(markdown, draftPath);
    if (!markdown.trim()) {
      console.error("Edited draft is empty. No files were changed.");
      process.exitCode = 1;
      return;
    }
    const saveEdited = await confirm("Save edited handoff?", true);
    if (!saveEdited) {
      console.log("No files were changed.");
      return;
    }
  }

  const savedPath = await saveSessionHandoff(paths.sessions, document.id, markdown);
  await updateCurrentTask(paths.currentTask, document, savedPath, repoRoot);
  await unlink(draftPath).catch(() => undefined);

  console.log("");
  console.log("✓ Handoff saved");
  console.log(`  ${path.relative(repoRoot, savedPath) || savedPath}`);
  console.log("✓ Current task updated");

  if (document.durableProposals.length > 0 && !options.skipDurable) {
    if (nonInteractive) {
      console.log(
        `! ${document.durableProposals.length} durable candidate(s) skipped in --yes mode`,
      );
    } else {
      await reviewAndApplyDurableRecords(paths, document.durableProposals);
    }
  } else if (
    !nonInteractive &&
    !options.skipDurable &&
    (autoDraft.relatedDecisions.length > 0 || autoDraft.relatedRejected.length > 0)
  ) {
    // Existing related durable memory is already on disk — no prompt needed unless new candidates.
  }

  if (!evidence.git.hasUncommittedChanges && evidence.git.recentCommits.length > 0) {
    if (!nonInteractive) {
      const archive = await confirm(
        "Working tree is clean. Mark this task complete in current-task.md?",
        false,
      );
      if (archive) {
        // Soft status flip only — do not move files in V1
        const { readText, writeText } = await import("../core/fs");
        let task = await readText(paths.currentTask);
        task = task.replace(/## Status\nActive/, "## Status\nComplete");
        await writeText(paths.currentTask, task);
        console.log("✓ Marked current task Complete (not archived)");
      }
    }
  }

  console.log("");
  console.log("Next time:");
  console.log(`  trove resume "${input.objective.trim()}"`);
}
