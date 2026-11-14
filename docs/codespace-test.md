# Testing the full flow on Linux in a GitHub Codespace

This walks a new user's path on Linux: install Trove, install the Claude Code plugin, run `/trove:setup`, and do one
real Claude Code session with your own key. It spends money, so it is capped and you run it by hand.

Before the beta tag exists, the install step builds from source instead (step 3, option B).

## 0. Costs and limits

- One short session on a pay-per-use Console key. Set the cap yourself (step 5). Keep it at or under $0.50.
- The Codespace stays on your GitHub account. Stop it when you finish, and delete it if you do not need it.
- Never paste the key into chat, a commit, an issue, a Dockerfile or `devcontainer.json`. Use step 1.

## 1. Put your API key in a Codespaces secret (not in the repo or your shell history)

1. On GitHub, open **Settings → Codespaces → Secrets → New secret**.
2. Name: `ANTHROPIC_API_KEY`. Value: your Console key (`sk-ant-api03-...`, or `sk-ant-usr-...`).
3. Repository access: select `usetrove/trove` only.
4. Create the Codespace after saving the secret. A secret is only loaded into a Codespace created (or rebuilt) after it was set.

Check it is loaded, without printing it:

```bash
test -n "$ANTHROPIC_API_KEY" && echo "key is set: ${#ANTHROPIC_API_KEY} characters"
```

If you prefer not to use a secret, enter the key for this terminal only. `read -s` keeps it out of the shell history:

```bash
read -r -s ANTHROPIC_API_KEY && export ANTHROPIC_API_KEY
```

Make sure no base URL override is set. Trove's test should go straight to Anthropic's default endpoint:

```bash
env | grep ANTHROPIC_BASE_URL || echo "ANTHROPIC_BASE_URL is not set"
```

## 2. Open the Codespace

On `usetrove/trove`, choose the branch to test (`hooks-rewrite` before the beta, `main` after it), then **Code → Codespaces → Create codespace on <branch>**.

## 3. Install Trove

**Option A, after the beta release exists (the real user path):**

```bash
curl -fsSL https://raw.githubusercontent.com/usetrove/trove/main/install.sh | TROVE_VERSION=0.1.0-beta.1 sh
trove --version          # should print 0.1.0-beta.1 and a commit
```

**Option B, before the release (build from source, the same flags as the release build):**

```bash
go build -trimpath -ldflags "-s -w" -o "$HOME/bin/trove" ./cmd/trove
export PATH="$HOME/bin:$PATH"
trove --version
```

Option B's plugin uses the `trove` on your `PATH`, because the plugin's `VERSION` is `dev` until the release sets it.

## 4. Install Claude Code and the plugin

Install Claude Code by its official instructions (code.claude.com/docs). Then, in the terminal:

```bash
claude --version
claude plugin marketplace add usetrove/trove
claude plugin install trove@trove
```

`sh` must be on your `PATH` for the hooks (it is, on Linux). `trove doctor` checks it.

## 5. Run `/trove:setup`, then one real session

Start Claude Code in a small project folder. Keep the session cheap: give it a one-line task.

```bash
mkdir -p ~/trove-demo && cd ~/trove-demo && printf 'alpha beta gamma\n' > notes.txt
claude
```

Inside Claude Code:

1. Type `/trove:setup` and follow the prompts. It shows each change first and asks before applying. Rewriting stays
   off until you choose a mode. For this test, choose **shadow** for the repeat-read check (it only estimates) and
   decline the status line and the gateway.
2. Type `/trove:status`. It lists which features are on, in shadow, or held back.
3. Ask one thing: `Read notes.txt and reply with only its first word.` Then exit (`/exit`).

For a capped one-shot run instead of the interactive one, use Claude Code's own budget flag (replace the key only if
you did not use step 1):

```bash
cd ~/trove-demo && claude -p "Read notes.txt and reply with only its first word." --max-budget-usd 0.10 --output-format json
```

## 6. Check the result

```bash
trove plugin status    # "First-run report: true" once a session has started
trove                  # your report; one request, with its cost labelled "estimated"
ls ~/.trove/hooks/sessions/    # a *.feat file means the Read hook ran in your session
```

Compare the spend with your Console usage page. Trove's figure is Claude Code's own report, not an invoice.

## 7. Clean up

```bash
trove uninstall --purge-data --yes     # restores agent files and deletes Trove's data
claude plugin uninstall trove          # removes the plugin
```

Then stop the Codespace (**Codespaces → ... → Stop**), and delete it if you are done. Delete the secret on GitHub once
you no longer need it.

## What to report back

- Each step that failed, with the exact message.
- Anything that looked confusing, including wording.
- The `trove doctor` output after step 5.
- The spend Trove reported, and the spend shown on your Console usage page for the same run.
