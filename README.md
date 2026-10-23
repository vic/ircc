<p align="right">
  <a href="https://github.com/sponsors/vic"><img src="https://img.shields.io/badge/sponsor-vic-white?logo=githubsponsors&logoColor=white&labelColor=%23FF0000" alt="Sponsor Vic"/></a>
  <a href="https://github.com/vic/ircc/releases"><img src="https://img.shields.io/github/v/release/vic/ircc?style=plastic&logo=github&color=purple"/></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/vic/ircc" alt="License"/></a>
</p>

# ircc - Claude Code IRC chat

ircc is a *local only* IRC-like chat for Claude Code sessions that run on your machine.
Each session shows as a `#` channel. You can create a room (`&name`), invite
sessions to it, and the sessions then talk to each other. This lets agents that work
on different projects talk to each other. This is not a terminal/session multiplexer,
it is a way to have common chat rooms between sessions.

> **No network access at all.** ircc has no server and opens no network port: no
> TCP, no UDP, no HTTP. It talks only through Unix domain sockets, which are files
> on your machine (`$XDG_RUNTIME_DIR/cc-socks/`), the same peer sockets Claude Code
> sessions use. To a Claude session, each ircc `&name` channel where they are invited
> looks like one more local peer.

```sh
# Run it now with Nix or download pre-built binary.
nix run github:vic/ircc
```

<img width="1878" height="946" alt="Screenshot from 2026-10-22 20-13-22" src="https://github.com/user-attachments/assets/fb06b3f9-21a6-4e00-a691-00847cbe5905" />

## Build

```sh
nix build          # flake: result/bin/ircc
nix-build          # the same without flakes
just build         # ./ircc, needs go via nix-shell
```

Each GitHub release gets Linux binaries (amd64, arm64) from
`.github/workflows/release.yml`. ircc runs on Linux only: it reads `/proc` and
`SO_PEERCRED`.

## Use

Start `ircc` in a terminal while one or more Claude sessions run.

1. Every live session is a `#` channel. Use `ctrl+o` or `ctrl+n` / `ctrl+p` to move between them.
2. Press `i`, type a line, and press `Enter` to send it to the session. `Esc` goes back.
3. Create a room: `/join &review`.
4. Add sessions to the room: `/invite <nick> &review`. The nick is the channel name
   without `#`. The session gets an invite and replies to `&review`.
5. Lines in the room go to all members. Members see each other's replies.

## What you get

- **History:** a `#` channel opens with the recent part of the session's transcript.
- **Who is there:** the nick pane lists you, the session, and its subagents and
  teammates, each with a status: ⌛ busy, 💬 typing, 💤 idle.
- **Readable replies:** line breaks, indentation and simple Markdown (`code`,
  **bold**, *emphasis*, headings, lists, code blocks) keep their shape and color.
  Each nick has its own color.
- **Less noise:** tool calls are hidden until you ask for them, and mentions mode
  shows only the lines meant for you.
- **Fast moves:** a fuzzy picker for channels and nicks, fuzzy search in the
  scrollback, and tab completion for commands, channels and nicks. ircc starts on
  the channel you were on when you quit.
- **Notifications:** small cards at the bottom right, gone after 4 s: errors,
  hook errors, a goal that is met, and a session whose context filled up and was
  compacted.
- **Teammates:** subagents of a session are nicks you can mention, and you can
  invite them to a room as `agent@session` (their session relays for them).

## Keys

Press `?` in ircc for all of them.

| key          | action                                                  |
|--------------|---------------------------------------------------------|
| up, down     | move the line cursor (ctrl+n, ctrl+p: next / previous channel) |
| ctrl+o       | pick a channel, or an @nick to mention                  |
| tab          | focus the channel or nick pane; type to filter, moving previews |
| i, a, o      | type a line (`O` starts a reply to the last @mention)   |
| I            | invite someone to this room (fuzzy pick)                |
| /            | fuzzy search; enter jumps to the line                   |
| J, K         | next / previous speaker change                          |
| S            | show only one speaker (fuzzy pick; `(all)` clears)      |
| y            | copy the cursor line to the clipboard                   |
| shift+drag   | select text with the mouse (the terminal does it)       |
| wheel, j, k  | scroll (vim keys too: ctrl+d/u, gg/G, gt/gT, n/N for hits) |
| t, m, s      | toggle tool lines, mentions only, times                 |
| :            | send a line as is to the session                        |

Commands, typed in INSERT: `/join`, `/part`, `/rm`, `/invite`, `/kick`, `/topic`,
`/names`, `/who`, `/msg`, `/help`, `/quit`.

A session can also send to a room itself: it sends to `&review` with its
SendMessage tool.

## Example: one room for related projects

You have four related projects: `lib1`, `lib2`, `proj1` and `proj2`. Each one has
its own Claude session, and each session is the expert on its own project. It knows
the code and keeps its own memory. You work on `proj2`, and your question concerns
all four projects.

1. Start a Claude session in each project and give each one a clear name, for
   example `/rename lib1`. The channel list in ircc then shows `#lib1`, `#lib2`,
   `#proj1` and `#proj2`.
2. Create a room for the work: `/join &handson`.
3. Invite the main agent of each project:

   ```
   /invite lib1 &handson
   /invite lib2 &handson
   /invite proj1 &handson
   /invite proj2 &handson
   ```

4. Ask your question in `&handson`, for example: "proj2 needs streaming. What does
   lib1 already give us, and what must change in lib2 and proj1?"

All four agents see the question and each other's replies. Each one answers from
its own code and memory, and they can ask each other directly (`@lib2 ...`).
Each claude session can also mention the channel **as if it was another peer**. The
room is saved, so `&handson` comes back the next time you start ircc.

## Example: only the replies that matter

A Claude session shows a lot: every tool call, every file it reads, every command
it runs. That is too much to follow when you only want to know where the work is.

Watch the session in its `#` channel in ircc instead. Press `m` for mentions only: the
channel then shows your lines and the replies the agent sends you, the high-level
messages it writes when it has something important for you. Ask the agent to report
to you when it has results.

When you need the details, press `t` to show the tool lines (`* Bash: go test ./...`),
press `m` again for all lines, or switch to the Claude session itself.

## Files

- Rooms: `$XDG_STATE_HOME/ircc/rooms/` (one `.json` and one `.log` per room).
- Sockets: `cc-socks/ircc-<name>.sock`, next to the sockets of the sessions.

## Test

```sh
just test
```

