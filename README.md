# Conductor CLI

Lead/worker orchestration for [Claude Code](https://claude.com/claude-code), with independent read-only reviews by Codex or other models, and one settings repository that keeps every machine in sync.

- **The lead never codes.** Your main Claude session plans, delegates, verifies and reports. Workers do the work.
- **Right-sized workers.** `worker-mini` for mechanical work, `worker-standard` for clear-scope work, `worker-expert` for hard problems. You choose each role's model and effort.
- **Clean context.** Workers return short structured reports, never code dumps.
- **Independent review.** At milestones the lead runs a review chain (by default Codex, then a read-only Claude reviewer agent; Antigravity and NVIDIA can join), with per-day limits.
- **Commits only with your OK** by default.
- **Pluggable providers.** Codex, Google Antigravity (`agy`) and NVIDIA are built in and optional; add others as plugins without rebuilding.
- **No hand-typed model ids.** Model lists come from each tool: Claude Code's and Codex's catalogs for your account, `agy models` and the NVIDIA API (`conductor models available`).
- **Skills for every agent.** Search skills.sh, Claude plugin marketplaces and your machine; one copy in `~/.agents/skills` is linked into Claude Code, Cursor, Gemini CLI, Copilot and 70+ other agents. Your list syncs; the files never enter your settings repo.
- **Cross-platform.** One binary for macOS, Linux and Windows. API keys live in the OS credential store.

## Install

The command is `conductor`; the package is `conductor-cli` (not to be confused with the Conductor.app cask).

```bash
# macOS / Linux (Homebrew)
brew tap mewebstudio/tap
brew install mewebstudio/tap/conductor-cli

# Any OS, from a release archive: put the binary on PATH
./conductor self-install   # ~/.local/bin, or %LOCALAPPDATA%\Programs\conductor on Windows (added to PATH)

# From source (Go 1.25+)
make install               # builds ~/.local/bin/conductor
```

Then:

```bash
conductor install --config-repo https://github.com/<you>/conductor-config.git
```

Keep settings in a synced folder instead of a git repo with `--config-path` (e.g. iCloud Drive or Dropbox); both flags together clone the repo into that folder. Move them later with `conductor config move <folder>`.

`conductor install` asks how agents should address you and in which language, which coding agents get your skills, offers to import skills already on the machine, offers optional providers, and in custom mode lets you choose every setting. Run it again any time; it is safe.

On a second machine, run the same command: your settings and skill list come from the repository or folder, missing skills are installed, and API keys are asked for once per machine.

## Everyday commands

| Command | What it does |
|---|---|
| `conductor doctor` | Check tools, sign-ins, keys and generated files |
| `conductor models` · `models set <role> <model> [effort]` · `models edit` · `models disable mini` | Roles: `lead`, `mini`, `standard`, `expert`, `reviewer` |
| `conductor models available [claude\|codex\|antigravity\|nvidia]` | Models each tool offers you, with their effort levels |
| `conductor profile [economy\|balanced\|quality]` | Switch every role at once |
| `conductor review` · `review chain codex nvidia claude` · `review set codex gpt-6.1-sol medium` · `review policy manual` · `review limit codex 3` | Review chain and reviewers |
| `conductor providers` · `providers enable antigravity\|nvidia` · `providers key nvidia` · `providers disable <name>` | Optional providers |
| `conductor ask --prompt <file> --out <file> [--provider nvidia] [--model <id>]` | Text-only jobs on an external model (saves Claude quota) |
| `conductor skills` · `skills search <words>` · `skills add <name\|owner/repo[/path]\|plugin@marketplace\|folder>` · `skills remove` · `skills update` · `skills sync` · `skills import` | Skills for all your agents, and Claude plugins |
| `conductor skills agents` · `skills agents edit` · `skills agents add <key> <folder>` | Which agents receive skills |
| `conductor project init` · `project list` · `project forget <name>` | Prepare a repo: `docs/decisions.md`, `docs/handoff.md`, `.conductor.conf` (test/build commands, protected folders) |
| `conductor vault` · `vault set <folder>` · `vault open` · `vault off` | One Obsidian vault for every project's docs |
| `conductor export <folder>` · `import <folder>` | Copy all settings to a folder and back (never API keys) |
| `conductor comms` · `comms language tr` · `comms address Alex` · `comms address --clear` · `comms edit` | Report language and how agents address you |
| `conductor config` · `config set git.push ask` | Every other setting |
| `conductor sync` | Pull settings, push local edits, re-apply, install missing skills |
| `conductor apply` | Regenerate Claude files from settings |
| `conductor uninstall [--purge]` | Remove everything conductor installed |

Add `--local` to any change to keep it on this machine only.

## Where things live

| Path | Contents |
|---|---|
| `~/.conductor-cli/config/conductor.toml` | Your settings (a clone of your settings repo, or your `--config-path` folder) |
| `~/.conductor-cli/config/skills.toml` | Your chosen agents, skills (source + pinned commit) and plugins |
| `~/.agents/skills/<skill>` | The one installed copy of each skill; agents get links (junctions on Windows) |
| `~/.conductor-cli/local.toml` | Machine-only overrides |
| `~/.conductor-cli/providers/<name>/` | Provider plugins |
| `~/.conductor-cli/backups/` | Copies of files before conductor changed them |
| `~/.claude/skills/conductor/`, `~/.claude/agents/{worker-*,reviewer}.md` | Generated; do not edit |
| `~/.claude/settings.json` | Conductor sets `model`, `effortLevel` and a session-start sync hook; nothing else is touched |
| `~/.claude/CLAUDE.md` | Conductor manages only the block between `conductor:begin/end` markers |

## Skills

- `skills add` understands a bare name (looked up on skills.sh), `owner/repo` (pick from the skills inside), `owner/repo/path` or a GitHub `tree` URL, `plugin@marketplace` (Claude Code plugin) and a local folder (this machine only). `owner/repo#name` picks one skill directly.
- Every skill is pinned to a commit. `skills update` shows the files that changed and asks before moving the pin.
- Conductor never overwrites or removes a skill folder it did not install; `skills adopt` takes one over, `skills import` adopts everything it can identify on this machine (vercel-labs `skills` lock file, Claude marketplace clones, git clones).
- The `conductor` skill itself and Claude plugins are Claude-only; other skills go to every chosen agent.
- At Claude session start, missing skills are installed in the background; the session never waits.
- The agent folder table is generated from [vercel-labs/skills](https://github.com/vercel-labs/skills) (MIT) with `scripts/gen_agents.py`.

## Projects

Run once per repository, from inside it:

```bash
cd ~/code/my-app
conductor project init                # name in the vault = folder name
conductor project init --name MyApp   # or choose the name
```

It creates only what is missing and never overwrites existing files:

| File | Purpose |
|---|---|
| `docs/decisions.md` | Decision log the lead reads before design decisions and appends to (date, decision, why) |
| `docs/handoff.md` | Where work stopped; the lead updates it before a session ends or near a usage limit |
| `.conductor.conf` | `test` and `build` commands workers verify with (guessed for Go, Swift, Xcode, npm, Cargo, Python, Gradle; Xcode needs your scheme name), and `protected` folders workers must not modify |
| `.gitignore` | Adds `/docs/reviews/` (review reports stay on the machine) |

It also registers the project on this machine and, if the vault is on, links its `docs/` into the vault. The new files are not committed; review and commit them yourself.

`init` works in any project folder, git or not. It only refuses the home folder, a filesystem root and folders above home (such as `/Users`), which are almost always a mistake.

```bash
conductor project list                # registered projects and their vault state
conductor project forget MyApp        # unregister and remove the vault link
```

`forget` keeps `docs/` and `.conductor.conf` in the project, since they are your files. If `init` ran in the wrong folder, delete them there yourself.

## Obsidian vault

`conductor vault set ~/Documents/Conductor` turns on one vault for all projects. Every `conductor project init` links that project's `docs/` folder into `Projects/<name>` (a symlink, or a junction on Windows) and refreshes `Home.md`. Nothing is copied: decisions and handoffs stay in each repository and are committed there; review reports (`docs/reviews/`) stay git-ignored but are visible in the vault. The vault path is stored as `~/…` so it works on every machine; which projects are linked is per machine. Obsidian may not notice edits made outside it right away; reopen the note if needed.

## Providers

| Provider | Needs | Read-only how | Reviews get the diff |
|---|---|---|---|
| `codex` | Codex CLI, signed in | `--sandbox read-only`, ephemeral | runs `git diff` itself |
| `antigravity` | `agy`, signed in | `--mode plan --sandbox`, no command permissions granted | inline in the prompt |
| `nvidia` | API key (OS credential store) | text only | inline in the prompt |
| `claude` | — | `reviewer` agent with Read/Grep/Glob only | inline in a prompt file |

Optional providers are offered during install only when their CLI is present (or, for NVIDIA, always), and a provider that is off, signed out or over quota is skipped in the chain.

## Provider plugins

Create `~/.conductor-cli/providers/<name>/provider.toml`:

```toml
name         = "my-reviewer"
description  = "My own review script"
capabilities = ["review", "ask"]
command      = "run.sh"     # in this folder, or on PATH
needs_key    = false
repo_access  = true         # false: review prompts include the diff
```

The command is called as `<command> <review|ask> <prompt_file> <output_file>` with `CONDUCTOR_MODEL`, `CONDUCTOR_EFFORT`, `CONDUCTOR_ROOT` (and `CONDUCTOR_API_KEY` when `needs_key`). Exit `0` ok, `1` failed, `2` not configured, `3` quota or rate limited. Then run `conductor providers enable <name>`.

## Development

```bash
make test
make build
```

Releases: push a `v*` tag; GitHub Actions builds all platforms with GoReleaser and, when the `HOMEBREW_TAP_TOKEN` secret is set, updates the cask in [mewebstudio/homebrew-tap](https://github.com/mewebstudio/homebrew-tap).

## License

MIT. The agent folder table is derived from [vercel-labs/skills](https://github.com/vercel-labs/skills) (MIT).
