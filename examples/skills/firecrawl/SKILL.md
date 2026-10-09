---
name: firecrawl
description: Search the web and read pages with Firecrawl. Use for current information, documentation, or the content of a URL.
mcp_tools:
  - name: server
    transport: mcp
    endpoint: https://mcp.firecrawl.dev/v2/mcp
    auth_type: bearer
    credential_ref: firecrawl-api-key
---

# Firecrawl

Use these tools when the answer depends on information you do not already have:
recent events, current documentation, prices, or the content of a specific page.

- Start with the search tool to find sources. Prefer one precise query over several broad ones.
- Use the scrape tool to read a page you already have a URL for. Ask for the main content only.
- Cite the URLs you used.
- Each call spends Firecrawl credits, so do not scrape a page you have already read.

Every tool the Firecrawl server offers is registered for the agent when the skill is
accepted; the list comes from the server, not from this file.

## Setup

Store your Firecrawl API key as a kitchen credential named `firecrawl-api-key`, then
register this skill. The key is sent as a Bearer token and never appears in this file.
