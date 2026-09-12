# auth.md

dbterm's public website is documentation for a local desktop and terminal
application. The public discovery files are anonymous and do not require an
account, OAuth token, API key, or agent registration.

The dbterm MCP server runs locally over STDIO (`dbterm mcp serve`). It does not
expose a hosted database API, remote MCP endpoint, or OAuth authorization
server. Database credentials stay in the user's local dbterm profile and are
never placed in an agent configuration.

Use the public [agent guide](https://dbterm.shreyam1008.com.np/agents/) and
[Agent Skill index](https://dbterm.shreyam1008.com.np/.well-known/agent-skills/index.json)
to configure a client. Any connection-profile write requires explicit opt-in in
dbterm Settings; query execution remains read-only and bounded.

```yaml
agent_auth:
  registration_required: false
  identity_types_supported: [anonymous]
  credential_types_supported: [none]
  protected_resources: []
  authorization_servers: []
```
