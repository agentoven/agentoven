# ADR-0013: Agent-to-UI Protocol (A2UI)

- **Date:** 2026-03-03
- **Status:** Accepted
- **Deciders:** siddartha

## Context

AgentOven standardizes agent-to-agent communication (A2A) and agent-to-tool
communication (MCP), but there is no standard for **agent-to-user-interface**
interaction. Today, agents return plain text or markdown, and the UI renders
it generically. There's no way for an agent to:

- Declare what UI it needs (chat, form, dashboard, map, chart)
- Send structured UI components (buttons, cards, tables, sliders)
- Receive structured user input (form submissions, selections, approvals)
- Adapt its interface based on platform (web, mobile, CLI, Slack)

### The Problem

1. **Generic rendering** — all agents look the same in the dashboard (text in/out)
2. **No interactivity** — agents can't ask structured questions (dropdowns, date pickers)
3. **Platform-agnostic** — the same agent should render differently on web vs CLI vs Slack
4. **No progressive enhancement** — simple agents don't need complex UIs; complex agents
   (multi-step workflows, data visualization) are crippled by text-only interfaces

### Industry Context

- **Anthropic's computer use** — agents generate screenshots, not UI components
- **OpenAI's function calling** — structured output but no UI semantics
- **Vercel's AI SDK** — has `useChat` hooks but no agent-side protocol
- **Streamlit** — great for Python data apps but not agent-native

**Nobody has standardized how agents declare and deliver UI.** This is our opportunity.

## Decision

### 1. UICapability on AgentCard

Extend the A2A `AgentCard` with UI capabilities that declare what interfaces
the agent supports:

```go
// pkg/models/agent.go — extend AgentCard

type UICapability struct {
    // Modes the agent supports
    Modes []UIMode `json:"modes"` // chat, form, dashboard, canvas

    // Components the agent can emit
    Components []string `json:"components,omitempty"` // button, card, table, chart, map, slider, etc.

    // Whether the agent supports streaming UI updates
    StreamingUI bool `json:"streaming_ui,omitempty"`

    // Whether the agent supports structured input (form submissions)
    StructuredInput bool `json:"structured_input,omitempty"`

    // Platform hints
    Platforms []string `json:"platforms,omitempty"` // web, mobile, cli, slack, teams
}

type UIMode string

const (
    UIModeChat      UIMode = "chat"      // conversational (default)
    UIModeForm      UIMode = "form"      // structured input collection
    UIModeDashboard UIMode = "dashboard" // data visualization
    UIModeCanvas    UIMode = "canvas"    // freeform layout
)
```

### 2. UI Message Parts

Extend the A2A `MessagePart` to include UI components alongside text and data:

```go
type UIComponent struct {
    Type       string                 `json:"type"`       // button, card, table, chart, form, etc.
    ID         string                 `json:"id"`         // unique within message
    Props      map[string]interface{} `json:"props"`      // component-specific properties
    Children   []UIComponent          `json:"children,omitempty"`
    Actions    []UIAction             `json:"actions,omitempty"`
    Responsive *ResponsiveHints       `json:"responsive,omitempty"`
}

type UIAction struct {
    ID     string                 `json:"id"`
    Label  string                 `json:"label"`
    Type   string                 `json:"type"`  // submit, navigate, invoke_agent, dismiss
    Params map[string]interface{} `json:"params,omitempty"`
}

type ResponsiveHints struct {
    MinWidth    string `json:"min_width,omitempty"`    // "mobile", "tablet", "desktop"
    FallbackTo  string `json:"fallback_to,omitempty"`  // degrade to this component type
    CLIRendering string `json:"cli_rendering,omitempty"` // how to render in terminal
}
```

### 3. Built-in Component Types

| Component | Props | Use Case |
|-----------|-------|----------|
| `text` | `content`, `format` (md/plain) | Default text output |
| `button` | `label`, `variant`, `action` | Call-to-action |
| `card` | `title`, `subtitle`, `image`, `body` | Entity display |
| `table` | `columns`, `rows`, `sortable` | Data display |
| `form` | `fields[]`, `submit_action` | Structured input |
| `chart` | `type` (bar/line/pie), `data`, `labels` | Data visualization |
| `progress` | `value`, `max`, `label` | Task progress |
| `alert` | `severity`, `message`, `dismissible` | Notifications |
| `code` | `language`, `content`, `copyable` | Code display |
| `tabs` | `items[]`, `active` | Multi-panel navigation |
| `accordion` | `items[]`, `expandable` | Collapsible sections |
| `image` | `src`, `alt`, `caption` | Media display |

### 4. Form Fields

When an agent needs structured input, it emits a `form` component:

```json
{
  "type": "form",
  "id": "booking-form",
  "props": {
    "title": "Book a Meeting",
    "fields": [
      {"name": "date", "type": "date", "label": "Meeting Date", "required": true},
      {"name": "duration", "type": "select", "label": "Duration", "options": ["30min", "1h", "2h"]},
      {"name": "attendees", "type": "text", "label": "Attendees (comma-separated)"},
      {"name": "notes", "type": "textarea", "label": "Notes", "required": false}
    ]
  },
  "actions": [
    {"id": "submit", "label": "Book Meeting", "type": "submit"},
    {"id": "cancel", "label": "Cancel", "type": "dismiss"}
  ]
}
```

### 5. Platform Rendering

The same UI component renders differently per platform:

| Component | Web | CLI | Slack |
|-----------|-----|-----|-------|
| `button` | `<button>` | `[1] Label` numbered choice | Block Kit button |
| `table` | HTML `<table>` | ASCII table (tabled crate) | Markdown table |
| `form` | HTML `<form>` | Interactive prompts (dialoguer) | Modal |
| `chart` | Canvas/SVG | ASCII chart | Image attachment |
| `card` | Material card | Bordered text box | Block Kit section |

### 6. Agent-Side API

Agents emit UI through the existing A2A task response, with a new `ui` part type:

```json
{
  "jsonrpc": "2.0",
  "method": "tasks/send",
  "params": {
    "message": {
      "role": "agent",
      "parts": [
        {"type": "text", "text": "Here's your booking summary:"},
        {
          "type": "ui",
          "component": {
            "type": "card",
            "props": {
              "title": "Meeting Booked",
              "subtitle": "March 5, 2026 at 2:00 PM",
              "body": "Duration: 1 hour\nAttendees: Alice, Bob"
            },
            "actions": [
              {"id": "reschedule", "label": "Reschedule", "type": "invoke_agent"},
              {"id": "cancel", "label": "Cancel Meeting", "type": "invoke_agent"}
            ]
          }
        }
      ]
    }
  }
}
```

### 7. User Input Flow

When a user interacts with a UI component (clicks button, submits form),
the client sends a structured message back to the agent:

```json
{
  "role": "user",
  "parts": [
    {
      "type": "ui_action",
      "component_id": "booking-form",
      "action_id": "submit",
      "data": {
        "date": "2026-03-05",
        "duration": "1h",
        "attendees": "alice@co.com, bob@co.com",
        "notes": ""
      }
    }
  ]
}
```

### 8. Implementation Phases

| Phase | Scope | Repo |
|-------|-------|------|
| Phase 1 | `UICapability` on AgentCard, `UIComponent` types, text/button/card/table | OSS |
| Phase 2 | Form components, structured input, platform-specific rendering | OSS |
| Phase 3 | Dashboard mode, chart components, streaming UI updates | OSS + Pro |
| Phase 4 | Canvas mode, drag-and-drop, collaborative editing | Pro |
| Phase 5 | Agent Viewer auto-generates UI from capabilities | Pro |

## Consequences

### Positive

- First open standard for agent-to-UI communication
- Agents become interactive, not just text-in/text-out
- Platform-agnostic — same agent works on web, CLI, Slack, Teams
- Progressive enhancement — simple agents use text, complex agents use rich UI
- Foundation for Agent Viewer (ADR-0008 Layer 3)

### Negative

- UI component library needs maintenance and documentation
- Cross-platform rendering consistency is challenging
- Increases message payload size
- CLI rendering of rich components is inherently limited

## Alternatives Considered

### A. HTML-in-responses

Rejected — HTML is web-specific. Can't render HTML in CLI or Slack.
UI components are a higher abstraction that maps to any platform.

### B. Markdown extensions

Rejected — Markdown is text-focused. No semantic components (forms,
buttons, charts). Extended markdown syntaxes are non-standard and brittle.

### C. React Server Components

Rejected — React-specific. AgentOven is framework-agnostic. The component
model is inspired by React props but platform-independent.

### D. Defer to client-side rendering

Rejected — the agent has domain knowledge about what UI is needed.
Client-side rendering means the client must understand agent semantics.
A2UI lets agents declare their UI intent explicitly.
