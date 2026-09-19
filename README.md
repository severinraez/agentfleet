# agentfleet

A server/client intermediary inbetween a host and many sandboxed agents.

Start it on the host:

`agentfleet orchestrator`

Configure it in a sandbox and use it

`AF_ORCHESTRATOR_URL=ws://host:port AF_SANDBOX_ID=mybox agentfleet rpc myscript`

## Orchestrator

The server process agents talk to. Will interactions by agents to stdout.

## RPC

RPC allows calling binaries on the host from the sandbox: `agentfleet rpc BINARY-NAME [ARGUMENTS]`. stdin/stdout/stderr are streamed, the host binaries' exit code is mirrored.

BINARY-NAME must be one of the binaries in the directory given by `rpc.directory`.

## Configuration

`agentfleet` takes it's configuration from it's configuration and enviromnent variables, where the latter take precedence.

The config file is at the path in `AF_CONFIG_PATH`, falling back to `./agentfleet.yaml` and then `~/.agentfleet.yaml`.

Paths in config files are relative to the config file, paths in environment variables are relative to CWD.

By example of the config option `rpc.directory`, the config file representation is

```yaml
rpc:
  directory: my-rpc-dir
```

and the corresponding environment variable is `AF_RPC_DIRECTORY`.
