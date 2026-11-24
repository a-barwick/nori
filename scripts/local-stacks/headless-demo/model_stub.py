"""Deterministic loopback replies for the outer conversation and task models."""

import itertools
import json
from http.server import BaseHTTPRequestHandler

from mcp_stub import (
    BODY,
    LIST_MODEL_TOOL,
    RECEIPT_ID,
    RECIPIENT,
    REPORT_ID,
    SEND_MODEL_TOOL,
    SUBJECT,
)

ACCOUNT = "demo.user@demo.example"
PROMPT = f"Check the pilot reports, then send the demo brief to {RECIPIENT}."
TASK_ANSWER = "Approved demo brief sent."
DECLINED_ANSWER = "Brief delivery was declined by the user."
FINAL_REPLY = f"The approved demo brief was sent to {RECIPIENT} (receipt {RECEIPT_ID})."


class ModelStub(BaseHTTPRequestHandler):
    call_ids = itertools.count(1)

    def log_message(self, format, *args):
        pass

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if self.path == "/v1/chat/completions":
            self.handle_chat(request)
        elif self.path == "/v1/responses":
            self.handle_responses(request)
        else:
            self.send_error(404)

    def handle_chat(self, request):
        messages = request["messages"]
        finished = any(
            marker in str(message.get("content", ""))
            for message in messages for marker in (TASK_ANSWER, RECEIPT_ID)
        )
        declined = any(
            DECLINED_ANSWER in str(message.get("content", ""))
            for message in messages
        )
        if finished:
            content = FINAL_REPLY
        elif declined:
            content = DECLINED_ANSWER
        else:
            content = "Ready."
        schema = request.get("response_format", {}).get("json_schema", {})
        if schema.get("name") == "nori_response":
            actions = []
            if not finished and not declined and any(
                m["role"] == "user" and PROMPT in str(m.get("content", "")) for m in messages
            ):
                actions = [{
                    "name": "dispatch",
                    "arguments": {
                        "request": PROMPT, "for": None, "question_id": None,
                        "attachments": [], "revises": None,
                    },
                }]
                content = "I’ll check the pilot reports and prepare the brief for approval."
            content = json.dumps({"actions": actions, "text": content})
        usage = {"prompt_tokens": 100, "completion_tokens": 25, "total_tokens": 125}
        metadata = {"model": request["model"], "service_tier": "default", "usage": usage}
        if request.get("stream"):
            chunks = [
                {"choices": [{"delta": {"content": content}, "finish_reason": None}]},
                {"choices": [{"delta": {}, "finish_reason": "stop"}]},
                {"choices": [], **metadata},
            ]
            data = "".join(f"data: {json.dumps(chunk)}\n\n" for chunk in chunks)
            self.reply((data + "data: [DONE]\n\n").encode(), "text/event-stream")
        else:
            self.reply_json({
                **metadata,
                "choices": [{
                    "message": {"role": "assistant", "content": content}, "finish_reason": "stop",
                }],
            })

    def handle_responses(self, request):
        items = request["input"]
        history = "\n".join(
            item["output"] for item in items if item.get("type") == "function_call_output"
        )
        transcript = json.dumps(items)
        tools = {tool.get("name") for tool in request.get("tools", [])}
        if RECEIPT_ID in history:
            name, arguments = "complete", {
                "answer": TASK_ANSWER,
                "evidence": [
                    f"MCP {LIST_MODEL_TOOL} returned report {REPORT_ID}",
                    f"MCP {SEND_MODEL_TOOL} receipt {RECEIPT_ID}",
                ],
                "unmet": [],
            }
        elif "The user declined this action" in transcript:
            name, arguments = "complete", {
                "answer": DECLINED_ANSWER,
                "evidence": [f"MCP {LIST_MODEL_TOOL} returned report {REPORT_ID}"],
                "unmet": ["User declined send_brief approval"],
            }
        elif REPORT_ID in history:
            # First call triggers Nori's approval card; after approval, retrying with the
            # exact same arguments matches the recorded approval digest and executes.
            name, arguments = SEND_MODEL_TOOL, {
                "recipient": RECIPIENT,
                "subject": SUBJECT,
                "body": BODY,
            }
        elif LIST_MODEL_TOOL in tools:
            name, arguments = LIST_MODEL_TOOL, {}
        else:
            # A summary request has no task tools.
            name, arguments = None, {}
        if name is None:
            output = {"type": "message", "content": [{"type": "output_text", "text": "Fixture summary."}]}
        elif name not in tools:
            self.send_error(422, f"Expected {name} in the task tool catalog")
            return
        else:
            output = {
                "type": "function_call", "name": name, "arguments": json.dumps(arguments),
                "call_id": f"call_headless_demo_{next(self.call_ids)}",
            }
        self.reply_json({
            "model": request["model"], "service_tier": "default", "status": "completed",
            "output": [output], "usage": {"input_tokens": 100, "output_tokens": 25, "total_tokens": 125},
        })

    def reply_json(self, data):
        self.reply(json.dumps(data).encode(), "application/json")

    def reply(self, data, content_type):
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)
