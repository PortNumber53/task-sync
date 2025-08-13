We want to add support for a simple JSON-RPC protocol over stdin/stdout so we can integrate with IDEs or editors to:
- query test lists
- parse failures
The communication will happen over a local channel (a named pipe), so there this new feature won't rely on networking

When running, the test should support a `--jsonrpc` flag to indicate it needs to communicate events using our JSON-RPC protocol over stdin/stdout

Every message must be framed as:
Content-Length: <N>\r\n
\r\n
<JSON-RPC payload>\n

During initialization, client->server uses this payload
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "initialize",
  "params": {
    "clientName": "MyIDE",
    "capabilities": { /* reserved for future */ }
  }
}

and server to client uses this payload
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "serverName": "GTestRPC",
    "version": "0.1.0"
  }
}

While listing tests:
Client will request:
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "listTests",
  "params": {}
}

and the server response will follow this payload:
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "tests": [
      { "suite": "FooTest", "name": "DoesSomething" },
      { "suite": "BarTest", "name": "HandlesEdgeCase" },
      /* … */
    ]
  }
}

When running tests
client will use a payload like this:
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "runTest",
  "params": {
    "suite": "FooTest",
    "name": "DoesSomething"
  }
}

and the server will use a payload like this:
{
  "jsonrpc": "2.0",
  "id": 3,
  "result": {}
}

As tests run, the server will emit: events like:
{
  "jsonrpc": "2.0",
  "method": "testEvent",
  "params": {
    "suite": "FooTest",
    "name": "DoesSomething",
    "status": "start" | "pass" | "fail",
    "message"?: "failure details or output",
    "durationMs"?: 123            // optional timing info
  }
}


For shutdown:
From client to server:
{
  "jsonrpc": "2.0",
  "id": 4,
  "method": "shutdown",
  "params": {}
}

and from server to cdlient:
{
  "jsonrpc": "2.0",
  "id": 4,
  "result": {}
}

For error handling, e.g. when a method is called with invalid parameters, the payload looks like this
{
  "jsonrpc": "2.0",
  "id": <same id>,
  "error": {
    "code": -32602,           // e.g. Invalid params
    "message": "Test not found"
  }
}


To keep the code organized, the new feature will be implemented within a new 'RunJsonRpcLoop' method, and gate the new feature behind a flag prefixed with `GTEST_DEFINE_bool_`
