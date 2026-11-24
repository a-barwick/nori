"""Self-contained loopback Streamable HTTP MCP stub server for the headless demo."""

from http.server import BaseHTTPRequestHandler
import json

MCP_SERVER_NAME = "Demo Reports"
LIST_TOOL_NAME = "list_reports"
SEND_TOOL_NAME = "send_brief"
LIST_MODEL_TOOL = "mcp_demo_reports__list_reports"
SEND_MODEL_TOOL = "mcp_demo_reports__send_brief"

REPORT_ID = "rpt_q4_pilot"
REPORT_SUMMARY = "Q4 pilot: 99.4% task completion across 12 active MCP integrations."
RECEIPT_ID = "brief_demo_1"
RECIPIENT = "ops@demo.example"
SUBJECT = "Demo brief"
BODY = "Q4 pilot summary: 99.4% task completion across 12 active MCP integrations."

TOOLS = [
    {
        "name": LIST_TOOL_NAME,
        "title": "List demo reports",
        "description": "List recent pilot reports available to summarize.",
        "inputSchema": {
            "type": "object",
            "properties": {},
            "additionalProperties": False,
        },
        "annotations": {
            "readOnlyHint": True,
            "destructiveHint": False,
            "idempotentHint": True,
            "openWorldHint": False,
        },
    },
    {
        "name": SEND_TOOL_NAME,
        "title": "Send demo brief",
        "description": "Deliver an executive brief to a target recipient.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "recipient": {"type": "string", "minLength": 1},
                "subject": {"type": "string", "minLength": 1},
                "body": {"type": "string", "minLength": 1},
            },
            "required": ["recipient", "subject", "body"],
            "additionalProperties": False,
        },
        "annotations": {
            "readOnlyHint": False,
            "destructiveHint": False,
            "idempotentHint": False,
            "openWorldHint": True,
        },
    },
]


class MCPStub(BaseHTTPRequestHandler):
    """Minimal Streamable HTTP JSON-RPC 2.0 MCP server with one read and one write tool."""

    def log_message(self, format, *args):
        pass

    def do_GET(self):
        if self.path != "/mcp":
            self.send_error(404)
            return
        self.send_response(405)
        self.send_header("Allow", "POST, DELETE")
        self.send_header("Content-Length", "0")
        self.end_headers()

    def do_DELETE(self):
        if self.path != "/mcp":
            self.send_error(404)
            return
        self.send_response(204)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def do_POST(self):
        if self.path != "/mcp":
            self.send_error(404)
            return
        length = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(length)
        try:
            message = json.loads(raw)
        except json.JSONDecodeError:
            self.reply_json({"jsonrpc": "2.0", "id": None, "error": {"code": -32700, "message": "Parse error"}}, 400)
            return

        # JSON-RPC notifications have no "id" and expect 202 Accepted with an empty body.
        if "id" not in message:
            self.send_response(202)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return

        req_id = message["id"]
        method = message.get("method", "")
        params = message.get("params") or {}

        if method == "initialize":
            version = params.get("protocolVersion") or "2025-06-18"
            self.reply_result(req_id, {
                "protocolVersion": version,
                "capabilities": {"tools": {}},
                "serverInfo": {"name": "demo-reports", "version": "1.0.0"},
            })
        elif method == "ping":
            self.reply_result(req_id, {})
        elif method == "tools/list":
            self.reply_result(req_id, {"tools": TOOLS})
        elif method == "tools/call":
            self.call_tool(req_id, params)
        else:
            self.reply_error(req_id, -32601, f"Method not found: {method}")

    def call_tool(self, req_id, params):
        name = params.get("name")
        args = params.get("arguments") or {}
        if name == LIST_TOOL_NAME:
            payload = {
                "reports": [
                    {
                        "id": REPORT_ID,
                        "title": "Q4 pilot metrics",
                        "summary": REPORT_SUMMARY,
                    }
                ]
            }
            self.reply_tool_ok(req_id, payload)
        elif name == SEND_TOOL_NAME:
            payload = {
                "receipt_id": RECEIPT_ID,
                "status": "sent",
                "recipient": args.get("recipient", RECIPIENT),
                "subject": args.get("subject", SUBJECT),
            }
            self.reply_tool_ok(req_id, payload)
        else:
            self.reply_error(req_id, -32602, f"Unknown tool: {name}")

    def reply_tool_ok(self, req_id, payload):
        self.reply_result(req_id, {
            "content": [{"type": "text", "text": json.dumps(payload)}],
            "isError": False,
        })

    def reply_result(self, req_id, result):
        self.reply_json({"jsonrpc": "2.0", "id": req_id, "result": result})

    def reply_error(self, req_id, code, message):
        self.reply_json({"jsonrpc": "2.0", "id": req_id, "error": {"code": code, "message": message}})

    def reply_json(self, data, status=200):
        encoded = json.dumps(data).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)
