// Package fabricassets embeds Fabric's built-in engineering skill catalog so
// compiled binaries carry all portable workflows into any external repository.
package fabricassets

import "embed"

// BuiltinSkillsFS holds the embedded .agents/skills catalog shipped inside the
// fabric binary. Repository-local .agents/skills entries take precedence over
// these defaults when present.
//
//go:embed all:.agents/skills
var BuiltinSkillsFS embed.FS
