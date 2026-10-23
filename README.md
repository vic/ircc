# ircc - Claude Code IRC chat

ircc is a *local only* IRC-like chat for the Claude Code sessions that run on your machine.
Each session shows as a `#` channel. You can create a room (`&name`), invite
sessions to it, and the sessions then talk to each other. This lets agents that work
on different projects talk to each other.

> ircc runs locally and opens no sockets/ports has no server.
It uses the same peer sockets as Claude Code sessions, so to a session, ircc looks like one more peer.

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

1. Every live session is a `#` channel. Use up / down to move between them.
2. Press `i`, type a line, and press `Enter` to send it to the session. `Esc` goes back.
3. Create a room: `/join &review`.
4. Add sessions to the room: `/invite <nick> &review`. The nick is the channel name
   without `#`. The session gets an invite and replies to `&review`.
5. Lines in the room go to all members. Members see each other's replies.

Press `?` for all keys. Other useful keys:

| key        | action                                |
|------------|---------------------------------------|
| up, down   | previous / next channel               |
| tab        | back to the previous channel          |
| ctrl+t     | fuzzy channel picker                  |
| wheel, j/k | scroll                                |
| s          | show or hide times (`15:04`)          |
| :          | send a line as is to the session, for example `/compact` |
| t          | show or hide tool lines (`* Bash: …`) |
| m          | mentions only (direct lines), or all lines |
| /          | search                                |

Commands (tab completes commands, channels and nicks): `/join`, `/part`, `/rm` (=`/close`),
`/invite`, `/kick`, `/topic`, `/names`, `/who`,
`/msg`, `/quit`.

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

