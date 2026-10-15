# AI agents (MCP)

`infraharvest mcp` serves infraharvest to AI agents such as Claude Code, Copilot or Cursor over the [Model Context Protocol](https://modelcontextprotocol.io), on stdin and stdout. Its tools run the same binary, so they behave like the command line:

| Tool | What it does |
|---|---|
| `discover` | Lists a provider's resources into a selection file, and summarises what it includes and excludes, with reasons |
| `import` | Generates configuration from a selection file. It asks the user to confirm first, through MCP elicitation. A client that can't ask gets the command to run instead |
| `report` | Returns an import's report |

Their parameters are named after the flags they set: `provider`, `services`, `regions`, `profile` and `selection`, and for `import` also `out`, `engine` and `incremental`; `report` takes `out`.

The agent can propose a selection, but only a person can start an import. infraharvest reads the cloud and writes files; it never applies anything. To add the server to Claude Code:

```sh
claude mcp add infraharvest -- infraharvest mcp
```
