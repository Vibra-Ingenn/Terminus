// Package tools provides tool calling for local LLM inference.
//
// The model emits a tool_call JSON object in its output; ParseToolCall
// recovers it (with a tolerant fallback parser) and the Go framework
// dispatches to the appropriate tool handler. GBNF grammar constraints were
// removed because too many local models could not follow them reliably.
package tools
