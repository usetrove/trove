import { appendText } from "../core/fs";
import { confirm } from "../core/prompt";
import {
  renderDurableDecision,
  renderDurableRejected,
} from "../templates/handoff";
import type { DurableRecordProposal, TrovePaths } from "../types/handoff";

export async function reviewAndApplyDurableRecords(
  paths: TrovePaths,
  proposals: DurableRecordProposal[],
): Promise<void> {
  if (proposals.length === 0) return;

  console.log("");
  console.log(`! ${proposals.length} possible durable record(s) found`);
  console.log("");
  console.log("Potential durable records found:");
  proposals.forEach((p, i) => {
    const label = p.type === "decision" ? "Decision" : "Rejected approach";
    console.log(`${i + 1}. ${label}: ${p.title}`);
  });
  console.log("");

  const wantsReview = await confirm("Review durable records now?", false);
  if (!wantsReview) {
    console.log("Skipped durable memory updates.");
    return;
  }

  console.log("");
  console.log("── Preview ──");
  for (const proposal of proposals) {
    const date = new Date().toISOString().slice(0, 10);
    const rendered =
      proposal.type === "decision"
        ? renderDurableDecision(proposal, date)
        : renderDurableRejected(proposal, date);
    console.log(rendered.trimEnd());
    console.log("");
  }

  const approve = await confirm(
    "Approve changes to decisions.md and/or rejected.md?",
    false,
  );
  if (!approve) {
    console.log("No durable files were changed.");
    return;
  }

  const date = new Date().toISOString().slice(0, 10);
  for (const proposal of proposals) {
    if (proposal.type === "decision") {
      await appendText(paths.decisions, renderDurableDecision(proposal, date));
      console.log("✓ Appended to decisions.md");
    } else {
      await appendText(paths.rejected, renderDurableRejected(proposal, date));
      console.log("✓ Appended to rejected.md");
    }
  }

  console.log("✓ Durable project memory updated");
}
