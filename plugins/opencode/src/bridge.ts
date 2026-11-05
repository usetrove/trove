import { spawn } from "node:child_process";
import type { ChildProcessWithoutNullStreams } from "node:child_process";
import { PROTOCOL } from "./types.ts";
import type { Message, Reply } from "./types.ts";

// The bridge to `trove engine serve`: one long-lived child process, one JSON
// object per line each way. It is the only place the plugin talks to the engine.
//
// Fail open (CLAUDE.md rule 4): call() never throws and never waits longer than
// its timeout. Any problem (the program is missing, it crashed, it answered too
// slowly, it spoke a different protocol, the reply was not JSON) yields null, and
// the caller then uses its original value. A dead engine costs a missed
// optimization, never a stopped agent.
//
// The bridge never reads or logs message contents: log lines are fixed strings
// and error codes.

export interface BridgeOptions {
  /** Path of the trove program. Default: "trove", found on PATH by the OS. */
  binary?: string;
  args?: string[];
  /** Longest a call may take, in milliseconds. Default 1000. */
  timeoutMs?: number;
  /** Longest the start-up handshake may take. Default 5000. */
  startTimeoutMs?: number;
  /** Receives short fixed diagnostics. Default: discard. */
  log?: (line: string) => void;
}

interface Pending {
  resolve: (r: Reply | null) => void;
  timer: ReturnType<typeof setTimeout>;
}

const MAX_RESTARTS = 3;
const RESTART_WINDOW_MS = 60_000;
const TIMEOUTS_BEFORE_RESTART = 3;

export class Bridge {
  readonly #binary: string;
  readonly #args: string[];
  readonly #timeoutMs: number;
  readonly #startTimeoutMs: number;
  readonly #log: (line: string) => void;

  #child: ChildProcessWithoutNullStreams | null = null;
  #buf = "";
  #pending = new Map<number, Pending>();
  #nextId = 1;
  #starting: Promise<boolean> | null = null;
  #ready = false;
  #disabled = false; // permanent for this process: no program, or a protocol we do not speak
  #closing = false;
  #restarts: number[] = [];
  #timeouts = 0;
  #recoverable = false;

  constructor(opts: BridgeOptions = {}) {
    this.#binary = opts.binary ?? "trove";
    this.#args = opts.args ?? ["engine", "serve"];
    this.#timeoutMs = opts.timeoutMs ?? 1000;
    this.#startTimeoutMs = opts.startTimeoutMs ?? 5000;
    this.#log = opts.log ?? (() => {});
  }

  /** True once the engine answered the handshake with a protocol we speak. */
  get available(): boolean {
    return this.#ready && !this.#disabled;
  }

  /** True when the engine can fetch trimmed output back, so the trimmer may be on. */
  get recoverable(): boolean {
    return this.#recoverable;
  }

  /** Starts the engine if it is not running. Resolves false when it cannot be used. */
  start(): Promise<boolean> {
    if (this.#disabled || this.#closing) return Promise.resolve(false);
    if (this.#ready && this.#child) return Promise.resolve(true);
    this.#starting ??= this.#spawn().finally(() => {
      this.#starting = null;
    });
    return this.#starting;
  }

  /**
   * Sends one message and resolves with the reply, or null on any failure. The id
   * is assigned here; callers do not set it.
   */
  async call(msg: Omit<Message, "id">, timeoutMs?: number): Promise<Reply | null> {
    try {
      if (!(await this.start())) return null;
      return await this.#send(msg, timeoutMs ?? this.#timeoutMs);
    } catch {
      return null;
    }
  }

  /**
   * Stops the engine and waits (briefly) for it to finish. Closing its input first
   * lets it write out what it has recorded; it is killed if it does not exit in
   * time. Pending calls resolve null.
   */
  async close(): Promise<void> {
    this.#closing = true;
    const child = this.#child;
    if (!child) return;
    await new Promise<void>((resolve) => {
      const t = setTimeout(() => {
        this.#kill();
        resolve();
      }, 1500);
      child.once("exit", () => {
        clearTimeout(t);
        resolve();
      });
      try {
        child.stdin.end();
      } catch {
        clearTimeout(t);
        this.#kill();
        resolve();
      }
    });
    this.#kill();
  }

  #spawn(): Promise<boolean> {
    const now = Date.now();
    this.#restarts = this.#restarts.filter((t) => now - t < RESTART_WINDOW_MS);
    if (this.#restarts.length >= MAX_RESTARTS) {
      this.#log("trove: the engine keeps stopping, so Trove is off for this session");
      this.#disabled = true;
      return Promise.resolve(false);
    }
    this.#restarts.push(now);

    return new Promise<boolean>((resolve) => {
      let child: ChildProcessWithoutNullStreams;
      try {
        child = spawn(this.#binary, this.#args, { stdio: ["pipe", "pipe", "pipe"], windowsHide: true });
      } catch {
        this.#disabled = true;
        this.#log("trove: could not start the trove program, so Trove is off. Install it (see docs/opencode.md).");
        resolve(false);
        return;
      }
      this.#child = child;
      this.#buf = "";
      let settled = false;
      const done = (ok: boolean) => {
        if (!settled) {
          settled = true;
          resolve(ok);
        }
      };

      child.on("error", (e: NodeJS.ErrnoException) => {
        if (e.code === "ENOENT" || e.code === "EACCES") {
          this.#disabled = true;
          this.#log("trove: the trove program was not found or cannot run, so Trove is off. Install it (see docs/opencode.md).");
        } else {
          this.#log("trove: the engine failed to start (" + (e.code ?? "error") + ")");
        }
        this.#onExit(child);
        done(false);
      });
      child.on("exit", () => {
        this.#onExit(child);
        done(false);
      });
      child.stdin.on("error", () => {}); // EPIPE after the child died; the exit handler deals with it
      child.stderr.on("data", () => {}); // drain; the engine logs nothing the plugin needs
      child.stdout.setEncoding("utf8");
      child.stdout.on("data", (chunk: string) => this.#onData(chunk));

      // Handshake. A failure here means the engine is unusable, not that a call failed.
      this.#send({ method: "hello" }, this.#startTimeoutMs, true).then((rep) => {
        if (rep?.ok && rep.protocol === PROTOCOL) {
          this.#ready = true;
          this.#recoverable = rep.recoverable === true;
          done(true);
          return;
        }
        if (rep) {
          this.#disabled = true;
          this.#log("trove: the trove program speaks a different protocol (" + String(rep.protocol) + "), so Trove is off. Update the plugin or the program.");
        }
        this.#kill();
        done(false);
      });
    });
  }

  #send(msg: Omit<Message, "id">, timeoutMs: number, handshake = false): Promise<Reply | null> {
    const child = this.#child;
    if (!child || (!handshake && !this.#ready)) return Promise.resolve(null);
    const id = this.#nextId++;
    return new Promise<Reply | null>((resolve) => {
      const timer = setTimeout(() => {
        // A late reply is dropped (its id is gone). Several in a row mean the engine is stuck.
        if (this.#pending.delete(id)) {
          resolve(null);
          if (++this.#timeouts >= TIMEOUTS_BEFORE_RESTART) {
            this.#timeouts = 0;
            this.#log("trove: the engine is not answering; restarting it");
            this.#kill();
          }
        }
      }, timeoutMs);
      this.#pending.set(id, { resolve, timer });
      try {
        child.stdin.write(JSON.stringify({ ...msg, id }) + "\n", (err) => {
          if (err) this.#settle(id, null);
        });
      } catch {
        this.#settle(id, null);
      }
    });
  }

  #settle(id: number, rep: Reply | null): void {
    const p = this.#pending.get(id);
    if (!p) return;
    clearTimeout(p.timer);
    this.#pending.delete(id);
    p.resolve(rep);
  }

  #onData(chunk: string): void {
    this.#buf += chunk;
    let nl: number;
    while ((nl = this.#buf.indexOf("\n")) >= 0) {
      const line = this.#buf.slice(0, nl);
      this.#buf = this.#buf.slice(nl + 1);
      if (line === "") continue;
      let rep: Reply;
      try {
        rep = JSON.parse(line) as Reply;
      } catch {
        this.#log("trove: the engine sent something that is not JSON; restarting it");
        this.#kill();
        return;
      }
      if (typeof rep.id !== "number") {
        // An error line with no id (a message that was too large): the engine stops after it.
        this.#kill();
        return;
      }
      this.#timeouts = 0;
      this.#settle(rep.id, rep);
    }
  }

  #onExit(child: ChildProcessWithoutNullStreams): void {
    if (this.#child !== child) return;
    this.#child = null;
    this.#ready = false;
    this.#buf = "";
    for (const id of [...this.#pending.keys()]) this.#settle(id, null);
  }

  #kill(): void {
    const child = this.#child;
    this.#ready = false;
    if (!child) return;
    this.#onExit(child);
    try {
      child.stdin.end();
      child.kill();
    } catch {
      // already gone
    }
  }
}
