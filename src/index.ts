#!/usr/bin/env node
import { Command } from "commander";
import { runHandoff } from "./commands/handoff";
import { runInit } from "./commands/init";
import { runResume } from "./commands/resume";

const program = new Command();

program.name("trove").description("Trove CLI").version("1.0.0");

program
  .command("hello")
  .description("print hello")
  .action(() => {
    console.log("hello");
  });

program
  .command("init")
  .description("initialize trove in this directory")
  .action(async () => {
    await runInit();
  });

program
  .command("handoff")
  .description("auto-draft a reviewable session checkpoint")
  .argument("[next]", "one-line next action (quick handoff)")
  .option("-y, --yes", "non-interactive save (requires --next or [next])", false)
  .option("--next <text>", "exact next action")
  .option("--objective <text>", "override detected objective")
  .option("--decisions <text>", "optional decision to propose for durable memory")
  .option("--rejected <text>", "optional rejected approach to propose")
  .option("--skip-durable", "do not prompt for durable memory updates", false)
  .action(async (nextArg: string | undefined, options) => {
    try {
      await runHandoff(process.cwd(), {
        ...options,
        nextArg,
      });
    } catch (error) {
      console.error(error instanceof Error ? error.message : String(error));
      process.exitCode = 1;
    }
  });

program
  .command("resume")
  .description("print a budgeted context packet for the next coding session")
  .argument("[query]", "optional focus phrase (defaults to current task)")
  .option("-b, --budget <tokens>", "token budget for the packet")
  .option("-o, --output <file>", "write packet to a file instead of stdout")
  .option("-p, --path", "print latest handoff path only", false)
  .option("--verbose", "show retrieval order and pack details on stderr", false)
  .action(async (query: string | undefined, options) => {
    try {
      await runResume(query, {
        budget: options.budget,
        output: options.output,
        verbose: options.verbose,
        pathOnly: options.path,
      });
    } catch (error) {
      console.error(error instanceof Error ? error.message : String(error));
      process.exitCode = 1;
    }
  });

program.parse(process.argv);
