"""Claude Agent SDK using GideonDB memory and search through gideondb-mcp.

    pip install claude-agent-sdk
    go install github.com/Bharanipbk/gideondb/cmd/gideondb-mcp@latest
    gideondb -lite &
    python examples/agents/claude_agent_sdk_mcp.py
"""

import asyncio
import os

from claude_agent_sdk import ClaudeAgentOptions, query

options = ClaudeAgentOptions(
    mcp_servers={
        "gideondb": {
            "type": "stdio",
            "command": "gideondb-mcp",
            "args": ["-allow-writes", "-embed-url", "http://localhost:11434/v1", "-embed-model", "nomic-embed-text"],
            "env": {"GIDEONDB_URL": os.getenv("GIDEONDB_URL", "http://127.0.0.1:6333"), "GIDEONDB_API_KEY": os.getenv("GIDEONDB_API_KEY", "")},
        }
    },
    allowed_tools=["mcp__gideondb__remember", "mcp__gideondb__recall", "mcp__gideondb__search", "mcp__gideondb__list_collections"],
    system_prompt="Use the gideondb remember and recall tools for user 'alice'.",
)


async def main() -> None:
    async for message in query(prompt="Remember that I live in Lisbon, then tell me where I live.", options=options):
        print(message)


asyncio.run(main())
