// A stand-in for `trove engine serve` that misbehaves on demand, for the bridge
// tests. MODE (an environment variable of the child) picks the behaviour:
//   echo      answers hello (protocol 1) and every other message with ok:true
//   slow      answers hello, then waits 400 ms before each other reply
//   crash     answers hello, then exits on the first other message
//   garbage   answers hello, then prints a line that is not JSON
//   noid      answers hello, then prints an error line without an id (an oversized message)
//   badproto  answers hello with protocol 99
//   silent    never answers anything
import { createInterface } from "node:readline";

const mode = process.env.MODE ?? "echo";
const rl = createInterface({ input: process.stdin });
const send = (o) => process.stdout.write(JSON.stringify(o) + "\n");

rl.on("line", (line) => {
  const m = JSON.parse(line);
  if (mode === "silent") return;
  if (m.method === "hello") {
    send({ id: m.id, ok: true, protocol: mode === "badproto" ? 99 : 1, version: "test", recoverable: true });
    return;
  }
  if (mode === "crash") process.exit(3);
  if (mode === "garbage") {
    process.stdout.write("this is not json\n");
    return;
  }
  if (mode === "noid") {
    send({ ok: false, error: "message too large or unreadable; the input was left unchanged" });
    return;
  }
  const reply = () => send({ id: m.id, ok: true, echo: m.method });
  if (mode === "slow") setTimeout(reply, 400);
  else reply();
});
