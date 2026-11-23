// Package proctree runs a child in its own process group and stops the whole
// tree it spawned: agent CLIs start helpers (node, MCP servers) that killing
// only the direct child would leave running.
//
//   - Prepare: before Start, puts the child in a new process group.
//   - Kill: kills the group (Unix) or the tree (Windows, taskkill /T).
//   - Interrupt: asks the child to stop (os.Interrupt; on Windows a
//     CTRL_BREAK to its group, which Go programs see as os.Interrupt).
package proctree
