# agentfleet

Your agent runs in a sandbox so it can't hurt you. Now it can't do anything useful either — no cloud credentials, no ssh agent, no logged-in `gh`, no production kubeconfig.

`agentfleet` gives it a narrow, audited door back to the host. The sandbox asks the host to run one of a handful of scripts you wrote; the credentials stay on the host, the agent never holds them, and every call goes past your eyes on the way through. One hub serves as many sandboxes as you point at it, so a whole fleet's reach into your host is one log you can watch.

Today agentfleet does exactly one thing: it lets a sandboxed process run allow-listed binaries on its host, and prints a line for each one.

## Install

On the host, download the release for your platform from
[releases](https://github.com/severinraez/agentfleet/releases) and put it on your `PATH`.

The same static binary runs in the sandbox — for a container image that is one line:

```dockerfile
COPY --from=ghcr.io/severinraez/agentfleet /agentfleet /usr/local/bin/agentfleet
```

## Quickstart

Write a capability. It is an ordinary executable in a directory you choose —
`rpc/deploy`:

```sh
#!/bin/sh
# af: deploy the current branch to staging
set -eu
exec /opt/deploy.sh --env=staging --tag="$AF_SANDBOX_ID"
```

Point the hub at it, in `agentfleet.yaml` on the host:

```yaml
hub:
  listen: 192.168.100.1:7777
rpc:
  directory: rpc
```

Start the hub:

```
agentfleet hub
```

Call it from the sandbox:

```
AF_HUB_URL=ws://192.168.100.1:7777 AF_SANDBOX_ID=mybox agentfleet rpc deploy
```

The hub prints:

```
2026-09-19T14:03:11Z mybox deploy exit=0 1.4s in=0B out=2.1kB
```

## Hub

The host-side process sandboxes talk to. It accepts connections, runs capabilities on request, and logs one line per call when the call finishes:

```
2026-09-19T14:03:11Z mybox deploy --force exit=1 12.7s in=0B out=340B
```

Timestamp, sandbox id, binary, arguments, exit code, duration, and bytes moved each way. That is the whole logging story — there is no format switch and no way to log stream contents. **Arguments are logged**, so capabilities take secrets on stdin, never as arguments.

`hub.listen` has no default and the hub will not start without it. See [Security](#security).

## RPC

```
agentfleet rpc BINARY-NAME [ARGUMENTS]
```

Runs `BINARY-NAME` from the hub's `rpc.directory`. stdin, stdout and stderr stream in both directions, and the host binary's exit code is mirrored.

Each call opens its own connection and closes it when the call ends — nothing runs resident in the sandbox. There is no timeout: a call lasts as long as the sandbox holds the connection. If the sandbox disappears mid-call, the hub kills the host process group rather than leaking it.

Run it with no arguments to see what this sandbox can call:

```
$ agentfleet rpc
deploy   deploy the current branch to staging
notify   send a message to the #agents channel
```

## Capabilities

Every file in `rpc.directory` is a capability you are handing to every sandbox that can reach the hub. Arguments pass through unchanged, so the file itself is the entire boundary — write one wrapper per thing you want to allow, as narrow as you can stand, and read [Security](#security) before you add the first one.

A wrapper describes itself with an `af:` comment in its first few lines, which is what the listing above shows:

```sh
# af: send a message to the #agents channel
```

Wrappers run with the hub's environment, credentials and all, plus `AF_SANDBOX_ID` naming the caller — so a wrapper can scope itself per sandbox without agentfleet needing a policy language:

```sh
exec kubectl --namespace="agent-$AF_SANDBOX_ID" "$@"
```

Their working directory is the hub's, or `rpc.working_directory` if you set it.

## Configuration

`agentfleet` takes its configuration from a configuration file and environment variables, where the latter take precedence. There are no command line flags.

The config file is at the path in `AF_CONFIG_PATH`, falling back to `./agentfleet.yaml` and then `~/.agentfleet.yaml`.

Paths in config files are relative to the config file, paths in environment variables are relative to CWD.

By example of the config option `rpc.directory`, the config file representation is

```yaml
rpc:
  directory: my-rpc-dir
```

and the corresponding environment variable is `AF_RPC_DIRECTORY`.

On the host:

| Option | Environment variable | |
|---|---|---|
| `hub.listen` | `AF_HUB_LISTEN` | Address the hub binds. Required, no default. |
| `rpc.directory` | `AF_RPC_DIRECTORY` | Directory holding the capabilities. Required. |
| `rpc.working_directory` | `AF_RPC_WORKING_DIRECTORY` | Working directory for capabilities. Defaults to the hub's. |

In the sandbox:

| Option | Environment variable | |
|---|---|---|
| `hub.url` | `AF_HUB_URL` | Websocket URL of the hub. Required. |
| `sandbox.id` | `AF_SANDBOX_ID` | Name this sandbox goes by. Required. |

## Security

**There is no authentication.** Anyone who can open a connection to `hub.listen` can run anything in `rpc.directory`. Reachability is the authorization — bind an interface only your sandboxes can reach, a container bridge or a VM network. That is why `hub.listen` has no default: it is your access control list, and defaulting it would make that easy to miss.

**`AF_SANDBOX_ID` is a label, not an identity.** The sandbox chooses it. Log lines carry it and wrappers can scope on it, but never grant anything on the strength of a name another sandbox could also claim.

**`rpc.directory` is a capability set, not a list of programs you trust.** Arguments are not filtered, so a general-purpose binary in there is a full host escape — `git` gets you `git config core.pager='sh -c …'`, and anything with a `--exec` or `-c` flag tells the same story. Never symlink system binaries into it. The narrowness of your wrappers is the only thing standing between a sandboxed agent and your host.

**Secrets go on stdin.** Arguments appear in the hub's log.

**The sandbox cannot set environment variables for a wrapper.** Wrappers inherit the hub's environment and nothing else, which is what keeps `LD_PRELOAD`, `BASH_ENV` and `GIT_SSH_COMMAND` from turning every capability into arbitrary host execution.

## Exit codes

| Code | Meaning |
|---|---|
| `125` | agentfleet itself failed — hub unreachable, unknown capability, protocol mismatch |
| `128+N` | the host binary was killed by signal N |
| anything else | the host binary's own exit code |

## Stability

Pre-1.0. The wire protocol carries a version and the hub rejects mismatches by name, so skew between an updated hub and a stale sandbox image fails loudly rather than strangely — but it will change, and interactive capabilities are the likely first break. Update both sides together.
