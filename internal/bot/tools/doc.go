// Package tools provides tool execution for the laplaced Telegram bot.
//
// It handles all tool execution for the Laplace agent:
// - Memory tools: add, update, delete facts
// - People tools: create, update, delete, merge people
// - Search tools: search history and people
// - Model tools: custom LLM calls
//
// The ToolExecutor dispatches tool calls to appropriate handlers
// and manages access to repositories and services.
package tools
