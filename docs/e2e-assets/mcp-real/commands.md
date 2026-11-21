Generated new encryption key. To persist credentials across restarts, set HIVE_CREDENTIAL_KEY=fvkpVsf1ZzBNJK1ag48w-kTTypJDJBb884_3cPk-38I=
Generated new encryption key. To persist credentials across restarts, set HIVE_CREDENTIAL_KEY=xAQUcOHMDIk-8PTRQH3BDhp5RS_FSFKsX1NHV9oJ0oY=
MCP tool 'get-sum' execution failed: [Server: everything-stdio] [Transport: stdio] Tool 'get-sum' failed: Input validation error: Invalid arguments for tool get-sum: b: Invalid input: expected number, received undefined
Inputs: {"a": 1}
Traceback (most recent call last):
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/tool_registry.py", line 1024, in executor
    result = client_ref.call_tool(tool_name, merged_inputs)
             ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 711, in call_tool
    return self._call_tool_with_retry(_stdio_call)
           ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 772, in _call_tool_with_retry
    return call()
           ^^^^^^
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 709, in _stdio_call
    return self._run_async(self._call_tool_stdio_async(tool_name, arguments, conn), conn)
           ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 213, in _run_async
    return self._wait_for_future(future, loop)
           ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 269, in _wait_for_future
    return future.result(timeout=1.0)
           ^^^^^^^^^^^^^^^^^^^^^^^^^^
  File "/Users/mac/.local/share/uv/python/cpython-3.11.16-macos-aarch64-none/lib/python3.11/concurrent/futures/_base.py", line 456, in result
    return self.__get_result()
           ^^^^^^^^^^^^^^^^^^^
  File "/Users/mac/.local/share/uv/python/cpython-3.11.16-macos-aarch64-none/lib/python3.11/concurrent/futures/_base.py", line 401, in __get_result
    raise self._exception
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 829, in _call_tool_stdio_async
    raise RuntimeError(f"[Server: {self.config.name}] [Transport: {self.config.transport}] Tool '{tool_name}' failed: {error_text}")
RuntimeError: [Server: everything-stdio] [Transport: stdio] Tool 'get-sum' failed: Input validation error: Invalid arguments for tool get-sum: b: Invalid input: expected number, received undefined
Generated new encryption key. To persist credentials across restarts, set HIVE_CREDENTIAL_KEY=_e-wVgjCkCvYk0ReWhATxZgBDQ-2xzCF5QzO_PrQe3o=
Retrying MCP STDIO tool call after dead-session signal from 'everything-stdio': 
[python] everything-stdio: tools=16 ok
Generated new encryption key. To persist credentials across restarts, set HIVE_CREDENTIAL_KEY=837m8htO_HdaeS9W1FlGRzZ08x4gA82w4EmmnBM4vXA=
Generated new encryption key. To persist credentials across restarts, set HIVE_CREDENTIAL_KEY=8ShnSDK1iPItgbDFMoi1Nuv_Bv9O3nFj5vdd_ZGueP0=
Retrying MCP STDIO tool call after dead-session signal from 'time-stdio': 
[python] time-stdio: tools=3 ok
Generated new encryption key. To persist credentials across restarts, set HIVE_CREDENTIAL_KEY=WJaF0iHIi--OJkEJg423h1gTfBXqJFJPKYdz9fB2UL0=
MCP tool 'get-sum' execution failed: [Server: everything-sse] [Transport: sse] Tool 'get-sum' failed: Input validation error: Invalid arguments for tool get-sum: b: Invalid input: expected number, received undefined
Inputs: {"a": 1}
Traceback (most recent call last):
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/tool_registry.py", line 1024, in executor
    result = client_ref.call_tool(tool_name, merged_inputs)
             ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 719, in call_tool
    return self._call_tool_with_retry(_sse_call)
           ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 799, in _call_tool_with_retry
    return call()
           ^^^^^^
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 717, in _sse_call
    return self._run_async(self._call_tool_stdio_async(tool_name, arguments, conn), conn)
           ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 213, in _run_async
    return self._wait_for_future(future, loop)
           ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 269, in _wait_for_future
    return future.result(timeout=1.0)
           ^^^^^^^^^^^^^^^^^^^^^^^^^^
  File "/Users/mac/.local/share/uv/python/cpython-3.11.16-macos-aarch64-none/lib/python3.11/concurrent/futures/_base.py", line 456, in result
    return self.__get_result()
           ^^^^^^^^^^^^^^^^^^^
  File "/Users/mac/.local/share/uv/python/cpython-3.11.16-macos-aarch64-none/lib/python3.11/concurrent/futures/_base.py", line 401, in __get_result
    raise self._exception
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 829, in _call_tool_stdio_async
    raise RuntimeError(f"[Server: {self.config.name}] [Transport: {self.config.transport}] Tool '{tool_name}' failed: {error_text}")
RuntimeError: [Server: everything-sse] [Transport: sse] Tool 'get-sum' failed: Input validation error: Invalid arguments for tool get-sum: b: Invalid input: expected number, received undefined
Generated new encryption key. To persist credentials across restarts, set HIVE_CREDENTIAL_KEY=GC8t2Di9ZrhRLdIinfliCXy41wSa8A6lNeOHtgtkyB4=
[python] everything-sse: tools=16 ok
Generated new encryption key. To persist credentials across restarts, set HIVE_CREDENTIAL_KEY=Og12xDColwDsis1CVTPH2jciBPkgNmZRJyZl3DFxcEI=
Health check failed for MCP server 'time-http': Client error '404 Not Found' for url 'http://127.0.0.1:64789/mcp/health'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/404
Failed to discover tools from 'time-http': Failed to list tools via HTTP: Client error '404 Not Found' for url 'http://127.0.0.1:64789/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/404
Failed to connect to MCP server 'time-http'
Traceback (most recent call last):
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 655, in _list_tools_http
    response.raise_for_status()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/.venv/lib/python3.11/site-packages/httpx/_models.py", line 829, in raise_for_status
    raise HTTPStatusError(message, request=request, response=self)
httpx.HTTPStatusError: Client error '404 Not Found' for url 'http://127.0.0.1:64789/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/404

The above exception was the direct cause of the following exception:

Traceback (most recent call last):
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_connection_manager.py", line 90, in acquire
    client.connect()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 330, in connect
    self._discover_tools()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 597, in _discover_tools
    tools_list = self._list_tools_http()
                 ^^^^^^^^^^^^^^^^^^^^^^^
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 663, in _list_tools_http
    raise RuntimeError(f"Failed to list tools via HTTP: {e}") from e
RuntimeError: Failed to list tools via HTTP: Client error '404 Not Found' for url 'http://127.0.0.1:64789/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/404
MCP Registry Load
MCP server 'time-http' failed to register, retrying in 2s: registered 0 tools
Health check failed for MCP server 'time-http': Client error '404 Not Found' for url 'http://127.0.0.1:64789/mcp/health'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/404
Failed to discover tools from 'time-http': Failed to list tools via HTTP: Client error '404 Not Found' for url 'http://127.0.0.1:64789/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/404
Failed to connect to MCP server 'time-http'
Traceback (most recent call last):
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 655, in _list_tools_http
    response.raise_for_status()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/.venv/lib/python3.11/site-packages/httpx/_models.py", line 829, in raise_for_status
    raise HTTPStatusError(message, request=request, response=self)
httpx.HTTPStatusError: Client error '404 Not Found' for url 'http://127.0.0.1:64789/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/404

The above exception was the direct cause of the following exception:

Traceback (most recent call last):
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_connection_manager.py", line 90, in acquire
    client.connect()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 330, in connect
    self._discover_tools()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 597, in _discover_tools
    tools_list = self._list_tools_http()
                 ^^^^^^^^^^^^^^^^^^^^^^^
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 663, in _list_tools_http
    raise RuntimeError(f"Failed to list tools via HTTP: {e}") from e
RuntimeError: Failed to list tools via HTTP: Client error '404 Not Found' for url 'http://127.0.0.1:64789/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/404
MCP Registry Load
MCP server 'time-http' failed after retry: registered 0 tools
Health check failed for MCP server 'time-http': Client error '404 Not Found' for url 'http://127.0.0.1:64789/mcp/health'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/404
Failed to discover tools from 'time-http': Failed to list tools via HTTP: Client error '404 Not Found' for url 'http://127.0.0.1:64789/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/404
Generated new encryption key. To persist credentials across restarts, set HIVE_CREDENTIAL_KEY=pap40RnqcvqkHvbOUobMQUZtWlE8z2pmnDmFTH4vKAc=
Health check failed for MCP server 'time-http': Client error '404 Not Found' for url 'http://127.0.0.1:64789/mcp/health'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/404
Failed to discover tools from 'time-http': Failed to list tools via HTTP: Client error '404 Not Found' for url 'http://127.0.0.1:64789/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/404
Failed to connect to MCP server 'time-http'
Traceback (most recent call last):
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 655, in _list_tools_http
    response.raise_for_status()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/.venv/lib/python3.11/site-packages/httpx/_models.py", line 829, in raise_for_status
    raise HTTPStatusError(message, request=request, response=self)
httpx.HTTPStatusError: Client error '404 Not Found' for url 'http://127.0.0.1:64789/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/404

The above exception was the direct cause of the following exception:

Traceback (most recent call last):
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_connection_manager.py", line 90, in acquire
    client.connect()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 330, in connect
    self._discover_tools()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 597, in _discover_tools
    tools_list = self._list_tools_http()
                 ^^^^^^^^^^^^^^^^^^^^^^^
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 663, in _list_tools_http
    raise RuntimeError(f"Failed to list tools via HTTP: {e}") from e
RuntimeError: Failed to list tools via HTTP: Client error '404 Not Found' for url 'http://127.0.0.1:64789/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/404
MCP Registry Load
MCP server 'time-http' failed to register, retrying in 2s: registered 0 tools
Health check failed for MCP server 'time-http': Client error '404 Not Found' for url 'http://127.0.0.1:64789/mcp/health'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/404
Failed to discover tools from 'time-http': Failed to list tools via HTTP: Client error '404 Not Found' for url 'http://127.0.0.1:64789/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/404
Failed to connect to MCP server 'time-http'
Traceback (most recent call last):
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 655, in _list_tools_http
    response.raise_for_status()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/.venv/lib/python3.11/site-packages/httpx/_models.py", line 829, in raise_for_status
    raise HTTPStatusError(message, request=request, response=self)
httpx.HTTPStatusError: Client error '404 Not Found' for url 'http://127.0.0.1:64789/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/404

The above exception was the direct cause of the following exception:

Traceback (most recent call last):
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_connection_manager.py", line 90, in acquire
    client.connect()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 330, in connect
    self._discover_tools()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 597, in _discover_tools
    tools_list = self._list_tools_http()
                 ^^^^^^^^^^^^^^^^^^^^^^^
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 663, in _list_tools_http
    raise RuntimeError(f"Failed to list tools via HTTP: {e}") from e
RuntimeError: Failed to list tools via HTTP: Client error '404 Not Found' for url 'http://127.0.0.1:64789/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/404
MCP Registry Load
MCP server 'time-http' failed after retry: registered 0 tools
[python] time-http: tools=0 ok
Generated new encryption key. To persist credentials across restarts, set HIVE_CREDENTIAL_KEY=EzPInrY0f-jpElKW8DvRpzERK9nyvJjQh8UyY09uxa0=
Health check failed for MCP server 'everything-http': Client error '406 Not Acceptable' for url 'http://127.0.0.1:64788/mcp/health'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/406
Failed to discover tools from 'everything-http': Failed to list tools via HTTP: Client error '406 Not Acceptable' for url 'http://127.0.0.1:64788/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/406
Failed to connect to MCP server 'everything-http'
Traceback (most recent call last):
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 655, in _list_tools_http
    response.raise_for_status()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/.venv/lib/python3.11/site-packages/httpx/_models.py", line 829, in raise_for_status
    raise HTTPStatusError(message, request=request, response=self)
httpx.HTTPStatusError: Client error '406 Not Acceptable' for url 'http://127.0.0.1:64788/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/406

The above exception was the direct cause of the following exception:

Traceback (most recent call last):
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_connection_manager.py", line 90, in acquire
    client.connect()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 330, in connect
    self._discover_tools()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 597, in _discover_tools
    tools_list = self._list_tools_http()
                 ^^^^^^^^^^^^^^^^^^^^^^^
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 663, in _list_tools_http
    raise RuntimeError(f"Failed to list tools via HTTP: {e}") from e
RuntimeError: Failed to list tools via HTTP: Client error '406 Not Acceptable' for url 'http://127.0.0.1:64788/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/406
MCP Registry Load
MCP server 'everything-http' failed to register, retrying in 2s: registered 0 tools
Health check failed for MCP server 'everything-http': Client error '406 Not Acceptable' for url 'http://127.0.0.1:64788/mcp/health'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/406
Failed to discover tools from 'everything-http': Failed to list tools via HTTP: Client error '406 Not Acceptable' for url 'http://127.0.0.1:64788/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/406
Failed to connect to MCP server 'everything-http'
Traceback (most recent call last):
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 655, in _list_tools_http
    response.raise_for_status()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/.venv/lib/python3.11/site-packages/httpx/_models.py", line 829, in raise_for_status
    raise HTTPStatusError(message, request=request, response=self)
httpx.HTTPStatusError: Client error '406 Not Acceptable' for url 'http://127.0.0.1:64788/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/406

The above exception was the direct cause of the following exception:

Traceback (most recent call last):
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_connection_manager.py", line 90, in acquire
    client.connect()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 330, in connect
    self._discover_tools()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 597, in _discover_tools
    tools_list = self._list_tools_http()
                 ^^^^^^^^^^^^^^^^^^^^^^^
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 663, in _list_tools_http
    raise RuntimeError(f"Failed to list tools via HTTP: {e}") from e
RuntimeError: Failed to list tools via HTTP: Client error '406 Not Acceptable' for url 'http://127.0.0.1:64788/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/406
MCP Registry Load
MCP server 'everything-http' failed after retry: registered 0 tools
Health check failed for MCP server 'everything-http': Client error '406 Not Acceptable' for url 'http://127.0.0.1:64788/mcp/health'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/406
Failed to discover tools from 'everything-http': Failed to list tools via HTTP: Client error '406 Not Acceptable' for url 'http://127.0.0.1:64788/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/406
Generated new encryption key. To persist credentials across restarts, set HIVE_CREDENTIAL_KEY=DDdWSntpTutyufxncHEKg05bn27QmseqCNEVYL8PBjQ=
Health check failed for MCP server 'everything-http': Client error '406 Not Acceptable' for url 'http://127.0.0.1:64788/mcp/health'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/406
Failed to discover tools from 'everything-http': Failed to list tools via HTTP: Client error '406 Not Acceptable' for url 'http://127.0.0.1:64788/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/406
Failed to connect to MCP server 'everything-http'
Traceback (most recent call last):
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 655, in _list_tools_http
    response.raise_for_status()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/.venv/lib/python3.11/site-packages/httpx/_models.py", line 829, in raise_for_status
    raise HTTPStatusError(message, request=request, response=self)
httpx.HTTPStatusError: Client error '406 Not Acceptable' for url 'http://127.0.0.1:64788/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/406

The above exception was the direct cause of the following exception:

Traceback (most recent call last):
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_connection_manager.py", line 90, in acquire
    client.connect()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 330, in connect
    self._discover_tools()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 597, in _discover_tools
    tools_list = self._list_tools_http()
                 ^^^^^^^^^^^^^^^^^^^^^^^
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 663, in _list_tools_http
    raise RuntimeError(f"Failed to list tools via HTTP: {e}") from e
RuntimeError: Failed to list tools via HTTP: Client error '406 Not Acceptable' for url 'http://127.0.0.1:64788/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/406
MCP Registry Load
MCP server 'everything-http' failed to register, retrying in 2s: registered 0 tools
Health check failed for MCP server 'everything-http': Client error '406 Not Acceptable' for url 'http://127.0.0.1:64788/mcp/health'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/406
Failed to discover tools from 'everything-http': Failed to list tools via HTTP: Client error '406 Not Acceptable' for url 'http://127.0.0.1:64788/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/406
Failed to connect to MCP server 'everything-http'
Traceback (most recent call last):
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 655, in _list_tools_http
    response.raise_for_status()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/.venv/lib/python3.11/site-packages/httpx/_models.py", line 829, in raise_for_status
    raise HTTPStatusError(message, request=request, response=self)
httpx.HTTPStatusError: Client error '406 Not Acceptable' for url 'http://127.0.0.1:64788/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/406

The above exception was the direct cause of the following exception:

Traceback (most recent call last):
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_connection_manager.py", line 90, in acquire
    client.connect()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 330, in connect
    self._discover_tools()
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 597, in _discover_tools
    tools_list = self._list_tools_http()
                 ^^^^^^^^^^^^^^^^^^^^^^^
  File "/Users/mac/hive/.wt/mcp-real-crosscheck/core/framework/loader/mcp_client.py", line 663, in _list_tools_http
    raise RuntimeError(f"Failed to list tools via HTTP: {e}") from e
RuntimeError: Failed to list tools via HTTP: Client error '406 Not Acceptable' for url 'http://127.0.0.1:64788/mcp/mcp/v1'
For more information check: https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/406
MCP Registry Load
MCP server 'everything-http' failed after retry: registered 0 tools
[python] everything-http: tools=0 ok
{"wrote": "/Users/mac/.hermes/profiles/omh-tui/cache/scratch/mcp-real-work/python-side.json", "servers": 5}
MCP STDIO owner teardown did not finish within 10s
MCP STDIO owner teardown did not finish within 10s
MCP STDIO owner teardown did not finish within 10s
=== RUN   TestMCPRealServerDump
2026/11/21 16:13:18 ERROR: MCP tool 'mcp__everything_stdio__get_sum' execution failed: [Server: everything-stdio] [Transport: stdio] Tool 'get-sum' failed: Input validation error: Invalid arguments for tool get-sum: b: Invalid input: expected number, received undefined
Inputs: {"a": 1}: [Server: everything-stdio] [Transport: stdio] Tool 'get-sum' failed: Input validation error: Invalid arguments for tool get-sum: b: Invalid input: expected number, received undefined
2026/11/21 16:13:29 ERROR: MCP tool 'mcp__everything_sse__get_sum' execution failed: [Server: everything-sse] [Transport: sse] Tool 'get-sum' failed: Input validation error: Invalid arguments for tool get-sum: b: Invalid input: expected number, received undefined
Inputs: {"a": 1}: [Server: everything-sse] [Transport: sse] Tool 'get-sum' failed: Input validation error: Invalid arguments for tool get-sum: b: Invalid input: expected number, received undefined
real-server MCP crosscheck dump written: /Users/mac/.hermes/profiles/omh-tui/cache/scratch/mcp-real-work/go-side.json (5 servers)
--- PASS: TestMCPRealServerDump (16.73s)
PASS
ok  	github.com/aden-hive/hive-go/internal/cli	17.250s
[raw] everything-stdio: 16 tools
[raw] time-stdio: 3 tools
[raw] everything-sse: 16 tools
[raw] time-http: 3 tools
[raw] everything-http: 16 tools
{"unexpected": 0, "exceptions": 5}

### CLI mode python: hive mcp add everything-http --transport http --url http://127.0.0.1:64788/mcp

```bash
HIVE_HOME=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/mcp-real-work/python-cli-home hive mcp add --name everything-http --transport http --url http://127.0.0.1:64788/mcp
```

✓ Registered everything-http (http)

```

### CLI mode python: hive mcp run everything-http --list

```bash
HIVE_HOME=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/mcp-real-work/python-cli-home hive mcp run everything-http --list
```


Available tools:
  echo                                      Echoes back the input string
  get-annotated-message                     Demonstrates how annotations can be used to provide metadata about...
  get-env                                   Returns all environment variables, helpful for debugging MCP server...
  get-header-params                         Returns its region argument and the Mcp-Param-Region HTTP header it...
  get-resource-links                        Returns up to ten resource links that reference different types of...
  get-resource-reference                    Returns a resource reference that can be used by MCP clients
  get-structured-content                    Returns structured content along with an output schema for client...
  get-sum                                   Returns the sum of two numbers
  get-tiny-image                            Returns a tiny MCP logo image.
  gzip-file-as-resource                     Compresses a single file using gzip compression. Depending upon the...
  trigger-list-changed                      Sends a notifications/<list>/list_changed for the tool, prompt or...
  trigger-log-messages                      Sends one log message at each logging level during the call. On...
  trigger-long-running-operation            Demonstrates a long running operation with progress updates.
  trigger-resource-update                   Sends a notifications/resources/updated for a resource URI. On...
  toggle-simulated-logging                  Toggles simulated, random-leveled logging on or off. Deprecated:...
  toggle-subscriber-updates                 Toggles simulated resource subscription updates on or off....

```

### CLI mode go: hive mcp add everything-http --transport http --url http://127.0.0.1:64788/mcp

```bash
HIVE_HOME=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/mcp-real-work/go-cli-home hive mcp add --name everything-http --transport http --url http://127.0.0.1:64788/mcp
```

✓ Registered everything-http (http)

```

### CLI mode go: hive mcp run everything-http --list

```bash
HIVE_HOME=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/mcp-real-work/go-cli-home hive mcp run everything-http --list
```


Available tools:
  echo                                      Echoes back the input string
  get-annotated-message                     Demonstrates how annotations can be used to provide metadata about...
  get-env                                   Returns all environment variables, helpful for debugging MCP server...
  get-header-params                         Returns its region argument and the Mcp-Param-Region HTTP header it...
  get-resource-links                        Returns up to ten resource links that reference different types of...
  get-resource-reference                    Returns a resource reference that can be used by MCP clients
  get-structured-content                    Returns structured content along with an output schema for client...
  get-sum                                   Returns the sum of two numbers
  get-tiny-image                            Returns a tiny MCP logo image.
  gzip-file-as-resource                     Compresses a single file using gzip compression. Depending upon the...
  trigger-list-changed                      Sends a notifications/<list>/list_changed for the tool, prompt or...
  trigger-log-messages                      Sends one log message at each logging level during the call. On...
  trigger-long-running-operation            Demonstrates a long running operation with progress updates.
  trigger-resource-update                   Sends a notifications/resources/updated for a resource URI. On...
  toggle-simulated-logging                  Toggles simulated, random-leveled logging on or off. Deprecated:...
  toggle-subscriber-updates                 Toggles simulated resource subscription updates on or off....

```
