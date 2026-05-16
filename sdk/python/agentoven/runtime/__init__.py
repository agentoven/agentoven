"""
agentoven.runtime — server-side SDK for framework-native managed agents.

Framework agents (LangChain, LangGraph, CrewAI, custom) use this module to:
  1. Read their configuration from AgentOven environment variables.
  2. Serve an HTTP endpoint that the AgentOven control plane proxies to.
  3. Print "AGENT_READY" on startup so the process manager knows they are live.

Quick start::

    # my_agent.py
    from agentoven.runtime import serve, AgentOvenRuntime, LangChainAdapter
    from langchain.agents import create_tool_calling_agent

    runtime = AgentOvenRuntime()
    llm     = runtime.build_langchain_llm()
    tools   = runtime.build_mcp_tools()
    agent   = create_tool_calling_agent(llm, tools, runtime.prompt_template)

    serve(LangChainAdapter(agent))
"""

from agentoven.runtime.runtime import AgentOvenRuntime
from agentoven.runtime.server import AgentOvenServer, serve
from agentoven.runtime.adapters.langchain import LangChainAdapter
from agentoven.runtime.adapters.langgraph import LangGraphAdapter
from agentoven.runtime.adapters.crewai import CrewAIAdapter

__all__ = [
    "AgentOvenRuntime",
    "AgentOvenServer",
    "serve",
    "LangChainAdapter",
    "LangGraphAdapter",
    "CrewAIAdapter",
]
