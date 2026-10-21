"""OpenAI Agents SDK using GideonDB memory and search through gideondb-mcp.

    pip install openai-agents
    go install github.com/Bharanipbk/gideondb/cmd/gideondb-mcp@latest
    gideondb -lite &
    OPENAI_API_KEY=... python examples/agents/openai_agents_mcp.py
"""

import asyncio
import os

from agents import Agent, Runner
from agents.mcp import MCPServerStdio


async def main() -> None:
    async with MCPServerStdio(
        name="gideondb",
        params={
            "command": "gideondb-mcp",
            "args": ["-allow-writes", "-embed-url", "https://api.openai.com/v1", "-embed-model", "text-embedding-3-small"],
            "env": {
                "GIDEONDB_URL": os.getenv("GIDEONDB_URL", "http://127.0.0.1:6333"),
                "GIDEONDB_API_KEY": os.getenv("GIDEONDB_API_KEY", ""),
                "EMBEDDING_API_KEY": os.environ["OPENAI_API_KEY"],
            },
        },
    ) as gideondb:
        agent = Agent(
            name="Assistant",
            instructions="You remember facts about user 'alice' with the remember tool and look them up with recall.",
            mcp_servers=[gideondb],
        )
        await Runner.run(agent, "Remember that I take my coffee black.")
        result = await Runner.run(agent, "How do I take my coffee?")
        print(result.final_output)


asyncio.run(main())
