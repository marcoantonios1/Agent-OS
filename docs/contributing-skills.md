# Contributing a community skill

Community skills live in `skills/community/`. Adding one requires no changes to built-in code — create a sub-package, implement one interface, register it, and restart.

---

## How it works

Built-in skills are wired in `internal/skills/registry.go` and are part of the core codebase. Community skills are wired in `skills/community/register.go`, which you edit directly. Both end up in the same global `ToolRegistry` that every agent draws from.

```
internal/skills/registry.go   ← built-in skills (do not edit)
skills/community/register.go  ← your skills go here
skills/community/myskill/
    tool.go                    ← your implementation
```

The `skills/community/*/` directories are gitignored — your implementations stay local. Only `register.go` is tracked so the hook point is always present.

---

## Step 1 — Create the sub-package

```bash
mkdir skills/community/weather
touch skills/community/weather/tool.go
```

---

## Step 2 — Implement `tools.Tool`

Every skill must satisfy two methods:

```go
type Tool interface {
    Definition() costguard.ToolDefinition
    Execute(ctx context.Context, input json.RawMessage) (string, error)
}
```

**`Definition()`** tells the LLM the tool's name, what it does, and what parameters it accepts (JSON Schema). **`Execute()`** runs the tool and returns a plain string the LLM reads as the result.

### Full example — weather tool

```go
// skills/community/weather/tool.go
package weather

import (
    "context"
    "encoding/json"
    "fmt"
    "net/http"

    "github.com/marcoantonios1/Agent-OS/internal/costguard"
)

type Tool struct{ apiKey string }

func New(apiKey string) *Tool { return &Tool{apiKey: apiKey} }

func (t *Tool) Definition() costguard.ToolDefinition {
    return costguard.ToolDefinition{
        Name:        "weather",
        Description: "Get the current weather for a city.",
        Parameters: map[string]any{
            "type": "object",
            "properties": map[string]any{
                "city": map[string]any{
                    "type":        "string",
                    "description": "City name, e.g. \"London\"",
                },
            },
            "required": []string{"city"},
        },
    }
}

type input struct {
    City string `json:"city"`
}

func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
    var in input
    if err := json.Unmarshal(raw, &in); err != nil {
        return "", fmt.Errorf("weather: invalid input: %w", err)
    }
    url := fmt.Sprintf("https://wttr.in/%s?format=3", in.City)
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
    if err != nil {
        return "", err
    }
    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return "", fmt.Errorf("weather: request failed: %w", err)
    }
    defer resp.Body.Close()
    var buf [256]byte
    n, _ := resp.Body.Read(buf[:])
    return string(buf[:n]), nil
}
```

---

## Step 3 — Register the skill

Open `skills/community/register.go` and add your skill:

```go
import (
    "os"

    "github.com/marcoantonios1/Agent-OS/internal/tools"
    "github.com/marcoantonios1/Agent-OS/skills/community/weather"
)

func RegisterAll(reg *tools.ToolRegistry) {
    reg.Register(weather.New(os.Getenv("WEATHER_API_KEY")))
}
```

If the skill needs no API key, just call `weather.New()` with no argument.

---

## Step 4 — Expose it to an agent

Add the skill name to the agent's `agent.yaml`:

```yaml
id: research
model: claude-sonnet-4-6
tool_call_model: gemma4:26b
skills:
  - web_search
  - web_fetch
  - weather       # ← add your skill name here
```

The name must match exactly what `Definition().Name` returns.

---

## Step 5 — Restart

```bash
# Docker
docker compose up --build -d

# Local
make run
```

On startup the agent will log the tool as available. Send it a message that should trigger the tool call and confirm it appears in the `costguard request` logs.

---

## Tips

**Nil-safe constructors** — if the skill needs an API key, return a disabled no-op when the key is empty rather than panicking:

```go
func New(apiKey string) *Tool {
    if apiKey == "" {
        return nil // RegisterAll should check for nil before registering
    }
    return &Tool{apiKey: apiKey}
}

// In register.go:
if t := weather.New(os.Getenv("WEATHER_API_KEY")); t != nil {
    reg.Register(t)
}
```

**Error messages** — `Execute()` errors are fed back to the LLM as `{"error":"..."}`. Write them in plain language the model can relay to the user.

**Parameter schema** — use `"required": []string{...}` to list mandatory parameters. The LLM will not call the tool without them.

**Testing** — write a `_test.go` alongside `tool.go`. The `Execute` method accepts a `context.Context` so you can inject a `context.WithTimeout` in tests. Mock the HTTP client or external service rather than hitting real APIs in CI.

---

## Example skills

Three ready-to-use example skills live in `skills/community/examples/`. Copy any of them into `skills/community/` to activate it.

### weather — Open-Meteo (no API key)

Geocodes the city name, fetches current temperature, humidity, wind speed, and conditions.

```bash
cp -r skills/community/examples/weather skills/community/weather
```

`register.go` addition:
```go
import "github.com/marcoantonios1/Agent-OS/skills/community/weather"

reg.Register(weather.New())
```

Sample output: `Weather in London, GB: partly cloudy, 18.5°C, humidity 65%, wind 12.3 km/h`

---

### stock_price — Alpha Vantage (free API key required)

Returns the current price and daily change for any ticker symbol.

Get a free key at <https://www.alphavantage.co/support/#api-key> (25 requests/day on the free tier), then:

```bash
cp -r skills/community/examples/stock_price skills/community/stock_price
```

Set the key in your `.env`:
```
ALPHA_VANTAGE_KEY=your_key_here
```

`register.go` addition:
```go
import "github.com/marcoantonios1/Agent-OS/skills/community/stock_price"

if t := stock_price.New(os.Getenv("ALPHA_VANTAGE_KEY")); t != nil {
    reg.Register(t)
}
```

Sample output: `AAPL: $189.42 (+2.13, +1.14% today)`

---

### url_shortener — is.gd (no API key)

Shortens any URL using the is.gd free service.

```bash
cp -r skills/community/examples/url_shortener skills/community/url_shortener
```

`register.go` addition:
```go
import "github.com/marcoantonios1/Agent-OS/skills/community/url_shortener"

reg.Register(url_shortener.New())
```

Sample output: `Shortened URL: https://is.gd/abc123`
