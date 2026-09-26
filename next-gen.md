# Agentfleet

Orchestrates multiple agents

## Hub

The central server.

`agentfleet hub`

## Execution Context

Where one or many agents run in.

For example a sandbox where your dev agents run in.

```json
{
  "name": "development-sandbox",
  "kind": "persistent",
  "setup": {
    "kind": "exec",
    "command": "path/to/setup",
    "args": [
      "literal",
      { "source": "config", "path": "repo" }
      { "source": "secret", "path": "nested.secret_a" },
    ],
    "env": {
      "SECRET_B": { "source": "secret", "path": "nested.secret_b" },
      "CONFIG_A": { "source": "config", "path": "myconfig" }
    }
  },
  "teardown": {
    "kind": "exec",
    "command": "path/to/teardown"
  },
  "session": {
    "kind": "exec",
    "command": "agent_cli",
    "cwd": { "source": "config", "path": "workdir" }
  }
}
```

These can be instantiated and worked in:

```bash
agentfleet context create mybox --type development-sandbox --config '{"repo": "/path/to/repo"}'
agentfleet session mybox
```

You can also define tasks to be triggered via messaging (explained below). E.g. an agent (or script) with limited git access:

```json
{
  "name": "git-agent",
  "kind": "ephemeral",
  "run": {
    "kind": "exec",
    "command": "trigger_agent_cli",
    "args": [
      { "source": "input", "path": "message" },
      { "source": "input", "path": "attachments_path" }
    ],
    "cwd": { "source": "caller", "path": "config.repo" }
  }
}
```

## Config and Secrets

Store configuration and secrets to be used by agents

`echo "secret" | agentfleet secret set mysecret`
`echo "secret" | agentfleet secret set nested.secret_a`
`echo "secret" | agentfleet secret set nested.secret_b`

`agentfleet config set myconfig value`
`echo "config" | agentfleet config set myconfig`
`echo "config" | agentfleet config set nested.config_a`
`echo "config" | agentfleet config set nested.config_b`

## Messaging

Agents can call eachother:

```bash
# Only a message
agentfleet call git-agent "commit the changes"
echo "commit the changes" | agentfleet call git-agent

touch mydir/file.txt
agentfleet call git-agent --attachments mydir/file.txt "commit the changes"
echo "commit the changes" | agentfleet call git-agent --attachments mydir/file.txt
```

The called agent may respond directly to stdout

```json
{
  "name": "git-agent",
  "kind": "ephemeral",
  "run": {
    "kind": "exec",
    "command": "echo",
    "args": ["a"]
  }
}
```

Or via stdout and attachments

```json
{
  "name": "git-agent",
  "kind": "ephemeral",
  "run": {
    "kind": "exec",
    "command": "touch",
    "args": ["output-dir/file"],
    "attachments": "output-dir"
  }
}
```

```bash
echo "commit the changes" | agentfleet call git-agent --response-attachments output-dir
# Response on stdout, attachments in output-dir (created if missing)
echo "commit the changes" | agentfleet call git-agent
# Error: git-agent sends attachments, specify --response-attachments
```
