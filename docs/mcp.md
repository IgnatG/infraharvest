# AI agents (MCP)

`infraharvest mcp` serves infraharvest to AI agents such as Claude Code, Copilot or Cursor over the [Model Context Protocol](https://modelcontextprotocol.io), on stdin and stdout. Its tools run the same binary, so they behave like the command line:

| Tool | What it does |
|---|---|
| `discover` | Lists a provider's resources into a selection file, and summarises what it includes and excludes, with reasons |
| `import` | Generates configuration from a selection file. It asks the user to confirm first, through MCP elicitation. A client that can't ask gets the command to run instead |
| `report` | Returns an import's report |

Their parameters are named after the flags they set: `provider`, `services` and `selection`, and for `import` also `out`, `engine` and `incremental`; `report` takes `out`. Where to list is in the provider's own terms, as its command's flags are (see [AWS](aws.md), [Azure](azure.md) and [Google Cloud](google.md)):

| Provider | Parameters | Flags they set |
|---|---|---|
| `aws` | `regions`, `profile`, `accounts` | `--regions`, `--profile`, `--accounts` |
| `azure` | `subscriptions`, `management_group`, `resource_group` | `--subscriptions`, `--management-group`, `--resource-group` |
| `google` | `projects`, `organization`, `folders`, `regions` | `--projects`, `--organization`, `--folders`, `--regions` |

A parameter the provider's command has no flag for, such as `regions` for Azure, is rejected with an error naming it, and so is a Google scope without `projects`, `organization` or `folders`. So are services and regions not named like one (letters, digits and `_*.-`), and any value or path with a control character such as a newline, which could make the confirmation say something else; the confirmation quotes the paths, and the command a client that can't ask is given quotes each argument for a POSIX shell. The user confirms an import seeing what it reads in these terms: the accounts and profile, the subscriptions and resource group, or the projects, organizations and folders, with the regions and services.

The agent can propose a selection, but only a person can start an import. The confirmation is for the selection file as it was when the user was asked: if the agent changes the file before the import runs, nothing is imported, and the import reads the content the user confirmed from a private copy. Each confirmation runs one import, and only an answer to a request the server made counts. The confirmation relies on the MCP client asking the user and passing on their answer honestly. The files the tools read and write stay under `--root` (default: the current directory, which the server refuses when it is the root of a filesystem or the home directory, as some hosts start servers there; it logs the root it uses to stderr), and a symbolic link under it that leads elsewhere is rejected: in the paths the agent names, in the report a `report` call reads, and anywhere in the output directory an import writes into (Terraform's `.terraform` directories aside). infraharvest reads the cloud and writes files; it never applies anything. Hosts other than Claude Code, such as desktop apps, may start the server outside any project: pass `--root` there. To add the server to Claude Code:

```sh
claude mcp add infraharvest -- infraharvest mcp
```
