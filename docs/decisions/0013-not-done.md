# 0013 Things that Magi does not do

Status: accepted

## Decision

- No tools that are JavaScript programs which the model writes. Tools
  are functions of the service. JavaScript is only in the web app.
- No credential broker for internal systems. A service proves who it is
  with its own token. A persona that acts as a person uses a LinkKeys
  grant. No service trusts a header that names a person.
- No summary of a conversation that replaces the conversation in the
  model context, for now. The conversation graph and the knowledge graph
  select what a turn reads. If one line of a conversation graph is too
  large for a model, that line can be made shorter then.
- No models from an API provider. All roles use local models.

## Reason

Each of these adds a part that can fail or leak, and the present design
has a simpler answer.
