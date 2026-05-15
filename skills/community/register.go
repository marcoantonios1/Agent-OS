// Package community is the home for user-contributed skills.
//
// To add a skill:
//  1. Create a sub-package: skills/community/myskill/tool.go
//  2. Implement the tools.Tool interface in that package.
//  3. Uncomment (or add) a reg.Register(...) line below.
//  4. List the skill name in the relevant agent's agent.yaml skills: list.
//  5. Restart the server.
//
// See docs/contributing-skills.md for a full walkthrough.
package community

import "github.com/marcoantonios1/Agent-OS/internal/tools"

// RegisterAll registers every community skill into the global registry.
// Built-in skills are registered separately in internal/skills/registry.go.
func RegisterAll(reg *tools.ToolRegistry) {
	// Uncomment the lines below after implementing your skill:
	//
	// reg.Register(weather.New())
	// reg.Register(stockprice.New(os.Getenv("ALPHA_VANTAGE_KEY")))
	// reg.Register(jira.New(os.Getenv("JIRA_TOKEN")))
}
