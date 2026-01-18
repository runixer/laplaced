# Testbot - CLI Tool for Testing Laplaced Bot

Testbot provides a command-line interface for testing the laplaced Telegram bot without running the full Telegram integration. It supports sending messages, checking database state, and processing sessions for fact extraction.

## IMPORTANT: Use `go run` for Development

**When testing changes to the bot code, always use `go run` instead of compiling.**

During development, you'll frequently modify files in `internal/`. A compiled binary (`go build`) won't reflect those changes - you'll be testing stale code. Always use `go run` to test the actual current state of your codebase.

```bash
# GOOD - tests current code
go run ./cmd/testbot send "test"

# BAD - tests old compiled code (misses your recent changes)
go build -o testbot ./cmd/testbot && ./testbot send "test"
```

**Why use `go run` instead of `go build`:**
- Tests **actual current code**, not stale binaries
- Automatically recompiles when files change
- No need to remember to rebuild after edits
- Safer: can't accidentally test old code

**When is `go build` appropriate:**
- Deploying to production
- Creating standalone binaries for distribution
- CI/CD pipelines (though `go run` is also fine there)

## Quick Start

```bash
# Run testbot (uses data/laplaced.db by default)
go run ./cmd/testbot send "What is 2+2?"

# Use with production database copy
go run ./cmd/testbot --db data/prod/laplaced.db send "test message"

# Check facts
go run ./cmd/testbot check-facts

# Process session
go run ./cmd/testbot process-session
```

## Environment Variables

Testbot uses the same environment variables as the main bot via config file:

- `LAPLACED_OPENROUTER_API_KEY` - OpenRouter API key (required)
- `LAPLACED_ALLOWED_USER_IDS` - Comma-separated user IDs (default user ID is first in list)
- `LAPLACED_*` - Any other `LAPLACED_` prefixed variables from the main bot config

## Commands

### send

Send a test message through the bot pipeline.

```bash
go run ./cmd/testbot send <message> [flags]
```

**Flags:**

- `--check-response <substring>` - Verify response contains substring
- `--check-topics <count>` - Verify topic count after message
- `--process-session` - Force process session after message
- `--max-wait <duration>` - Maximum wait time for LLM response (default: 2m0s)
- `--output <format>` - Output format: text, json (default: text)

**Examples:**

```bash
# Basic message
go run ./cmd/testbot send "What is 2+2?"

# With response verification
go run ./cmd/testbot send "My name is Alice" --check-response "Alice"

# With JSON output
go run ./cmd/testbot send "test" --output json

# With session processing
go run ./cmd/testbot send "My name is Alice" --process-session
```

### check-facts

Check facts in database.

```bash
go run ./cmd/testbot check-facts [flags]
```

**Flags:**

- `-f, --format <format>` - Output format: text, json (default: text)

**Example:**

```bash
go run ./cmd/testbot check-facts
go run ./cmd/testbot check-facts --format json
```

### check-topics

Check topics in database.

```bash
go run ./cmd/testbot check-topics [flags]
```

**Flags:**

- `-f, --format <format>` - Output format: text, json (default: text)

**Example:**

```bash
go run ./cmd/testbot check-topics
go run ./cmd/testbot check-topics --format json
```

### check-people

Check people in database.

```bash
go run ./cmd/testbot check-people [flags]
```

**Flags:**

- `-f, --format <format>` - Output format: text, json (default: text)

**Example:**

```bash
go run ./cmd/testbot check-people
go run ./cmd/testbot check-people --format json
```

### check-messages

Check recent messages in database.

```bash
go run ./cmd/testbot check-messages [flags]
```

**Flags:**

- `-f, --format <format>` - Output format: text, json (default: text)

**Example:**

```bash
go run ./cmd/testbot check-messages
go run ./cmd/testbot check-messages --format json
```

### process-session

Force process active session into topic and extract facts.

**Note:** This can take 10-30 seconds depending on session length — calls LLM to create topic summary and extract facts.

```bash
go run ./cmd/testbot process-session
```

**Example:**

```bash
# Send messages and process
go run ./cmd/testbot send "My name is Alice"
go run ./cmd/testbot send "I love photography"
go run ./cmd/testbot process-session

# Check extracted facts
go run ./cmd/testbot check-facts
```

### clear-facts

Clear all facts for user.

```bash
go run ./cmd/testbot clear-facts
```

### clear-topics

Clear all topics for user.

```bash
go run ./cmd/testbot clear-topics
```

### clear-people

Clear all people for user.

```bash
go run ./cmd/testbot clear-people
```

## Global Options

- `--config <path>` - Path to config file (default: auto-detect)
- `-u, --user <id>` - User ID for testing (default: first from `LAPLACED_ALLOWED_USER_IDS`)
- `--db <path>` - Database path (default: `data/laplaced.db`, use `data/prod/laplaced.db` for production copy, use `--db ""` for temp DB)
- `--logs` - Show log output (default: quiet)
- `-v, --verbose` - Verbose debug output (implies `--logs`)

## Usage Examples

### Testing Fact Extraction

```bash
# 1. Clear existing facts
go run ./cmd/testbot clear-facts

# 2. Send messages
go run ./cmd/testbot send "My name is Alice and I'm a software engineer"
go run ./cmd/testbot send "I live in San Francisco and love hiking"

# 3. Process session
go run ./cmd/testbot process-session

# 4. Check extracted facts
go run ./cmd/testbot check-facts
```

### Testing RAG Retrieval

```bash
# 1. Create topics
go run ./cmd/testbot send "Python is a programming language" --process-session
go run ./cmd/testbot send "JavaScript is used for web development" --process-session

# 2. Check topics
go run ./cmd/testbot check-topics

# 3. Ask question that should retrieve from RAG
go run ./cmd/testbot send "What programming languages did I ask about?" --check-response "Python"
```

### Automated Testing

```bash
# Run test with verification
go run ./cmd/testbot send "What is 2+2?" --check-response "4" --output json

# In CI/CD pipeline
go run ./cmd/testbot send "test" --check-response "expected" || exit 1
```

## Testing with Production Database

The `data/prod/laplaced.db` is a copy of the production database. To test on it:

```bash
# Run testbot on production database copy
go run ./cmd/testbot --db data/prod/laplaced.db send "test message"

# Check the results
go run ./cmd/testbot --db data/prod/laplaced.db check-facts
go run ./cmd/testbot --db data/prod/laplaced.db check-topics
```

**Note:** `data/prod/laplaced.db` is already a copy and safe to use for testing. Changes here don't affect the production database.

### Database Locations

- **`data/laplaced.db`** (default) - Test database for quick testing with RAG/facts
- **`data/prod/laplaced.db`** - Copy of production database for testing on real data
- **`--db ""`** (empty) - Temporary database in `/tmp` for clean testing without data

## JSON Output Format

The `send` command outputs JSON when `--output json` is specified:

```json
{
  "status": "PASS",
  "response_raw": "The response text",
  "response_html": "<p>The response text</p>",
  "duration_ms": 1234,
  "metrics": {
    "tokens": 150,
    "cost_usd": 0.000123,
    "topics_matched": 3,
    "facts_injected": 5
  }
}
```

If assertions fail, the output includes:

```json
{
  "status": "FAIL",
  "failures": [
    "response does not contain \"expected\"",
    "expected 5 topics, got 3"
  ],
  ...
}
```

## Development

### Why `go run` is recommended for development

When developing the laplaced bot, you'll frequently modify files in `internal/`. Using `go run` ensures you test the actual current code:

1. **Automatic recompilation**: Go recompiles all packages on each run
2. **No stale code**: You can't accidentally test an old binary
3. **Faster workflow**: No need to remember to rebuild after edits

**Example workflow:**
```bash
# Edit a file in internal/bot/
vim internal/bot/handler.go

# Test immediately - go run recompiles automatically
go run ./cmd/testbot send "test"

# Edit again
vim internal/bot/handler.go

# Test again - always testing current code
go run ./cmd/testbot send "test"
```

### Building (for deployment)

Only compile binaries when deploying to production or creating standalone distributions:

```bash
# Build locally
go build -o testbot ./cmd/testbot

# Build for Linux AMD64
GOARCH=amd64 go build -o testbot-linux-amd64 ./cmd/testbot

# Build for Linux ARM64
GOARCH=arm64 go build -o testbot-linux-arm64 ./cmd/testbot
```

**Note:** Built binaries are useful for:
- Production deployment
- Creating standalone tools for users
- CI/CD pipelines (though `go run` works there too)

### Testing

```bash
# Run testbot tests
go test ./cmd/testbot/...

# Run all tests
go test ./...
```

### Code Structure

```
cmd/testbot/
├── main.go           - Entry point (Cobra Execute())
├── root.go           - Root command, global flags, testbot initialization
├── send.go           - send command
├── check.go          - check-* commands
├── process.go        - process-session command
├── clear.go          - clear-* commands
└── README.md         - This file

internal/app/
├── env.go            - Shared .env loading
└── builder.go        - Shared service initialization
```

## Troubleshooting

### "LAPLACED_OPENROUTER_API_KEY not set in config/env"

Set the environment variable:

```bash
export LAPLACED_OPENROUTER_API_KEY=your-key-here
```

Or add it to your `.env` file:

```
LAPLACED_OPENROUTER_API_KEY=your-key-here
```

### "invalid user ID"

Either set `LAPLACED_ALLOWED_USER_IDS` or use `--user` flag:

```bash
export LAPLACED_ALLOWED_USER_IDS=123456789
go run ./cmd/testbot send "test"

# Or
go run ./cmd/testbot --user 123456789 send "test"
```

### Database errors

By default, testbot uses `data/laplaced.db`. To use a different database:

```bash
# Use production database copy
go run ./cmd/testbot --db data/prod/laplaced.db send "test"

# Use temporary database (clean state)
go run ./cmd/testbot --db "" send "test"

# Use custom path
go run ./cmd/testbot --db /path/to/database.db send "test"
```

## Related Documentation

- [CLAUDE.md](../../CLAUDE.md) - Main project documentation
- [cmd/bot/main.go](../bot/main.go) - Main bot entry point
- [internal/app/](../../internal/app/) - Shared initialization code
