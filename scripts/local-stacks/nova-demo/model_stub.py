"""Deterministic loopback replies for the outer conversation and task models."""

import itertools
import json
import time
from http.server import BaseHTTPRequestHandler

ACCOUNT = "recorded.user@example.com"
SUBJECT = "Nova demo brief"
BODY = "Calendar summary for the Nova pilot."
PROMPT = "List my calendars, then send the Nova demo brief to my own mailbox."
TASK_ANSWER = "Draft approved and sent."
FINAL_REPLY = "The approved Nova demo brief was sent."
DRAFT_ID = "recdraft1"
SENT_ID = "recsent1"


class ModelStub(BaseHTTPRequestHandler):
    call_ids = itertools.count(1)

    def log_message(self, format, *args):
        pass

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if self.path == "/v1/chat/completions":
            self.chat(request)
        elif self.path == "/v1/responses":
            self.task_responses(request)
        else:
            self.send_error(404)

    def chat(self, request):
        messages = request["messages"]
        finished = any(
            marker in str(message.get("content", ""))
            for message in messages for marker in (TASK_ANSWER, SENT_ID)
        )
        content = FINAL_REPLY if finished else "Ready."
        schema = request.get("response_format", {}).get("json_schema", {})
        if schema.get("name") == "nori_response":
            actions = []
            if not finished and any(
                m["role"] == "user" and PROMPT in str(m.get("content", "")) for m in messages
            ):
                actions = [{
                    "name": "dispatch",
                    "arguments": {
                        "request": PROMPT, "for": None, "question_id": None,
                        "attachments": [], "revises": None,
                    },
                }]
                content = "I’ll check your calendars and prepare the email."
            content = json.dumps({"actions": actions, "text": content})
        if schema.get("name") == "chat_response":
            content = json.dumps({"actions": [], "text": content})
        usage = {"prompt_tokens": 100, "completion_tokens": 25, "total_tokens": 125}
        metadata = {"model": request["model"], "service_tier": "default", "usage": usage}
        if request.get("stream"):
            middle = max(1, len(content) - 4)
            chunks = [
                {"choices": [{"delta": {"content": content[:middle]}, "finish_reason": None}]},
                {"choices": [{"delta": {"content": content[middle:]}, "finish_reason": None}]},
                {"choices": [{"delta": {}, "finish_reason": "stop"}]},
                {"choices": [], **metadata},
            ]
            frames = [f"data: {json.dumps(chunk)}\n\n".encode() for chunk in chunks]
            frames.append(b"data: [DONE]\n\n")
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Content-Length", str(sum(map(len, frames))))
            self.end_headers()
            try:
                self.wfile.write(frames[0])
                self.wfile.flush()
                time.sleep(0.6)
                self.wfile.writelines(frames[1:])
            except (BrokenPipeError, ConnectionResetError):
                pass
        else:
            self.reply_json({
                **metadata,
                "choices": [{
                    "message": {"role": "assistant", "content": content}, "finish_reason": "stop",
                }],
            })

    def task_responses(self, request):
        history = "\n".join(
            item["output"] for item in request["input"] if item.get("type") == "function_call_output"
        )
        tools = {tool.get("name") for tool in request.get("tools", [])}
        if SENT_ID in history:
            name, arguments = "complete", {
                "answer": TASK_ANSWER,
                "evidence": ["Calendar list returned", f"Gmail send receipt {SENT_ID}"],
                "unmet": [],
            }
        elif DRAFT_ID in history:
            name, arguments = "mail_send_draft", {"draft_id": DRAFT_ID}
        elif "calendar" in history:
            name, arguments = "mail_draft_create", {"to": [ACCOUNT], "subject": SUBJECT, "body": BODY}
        elif "calendar_list_calendars" in tools:
            name, arguments = "calendar_list_calendars", {}
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
                "call_id": f"call_nova_demo_{next(self.call_ids)}",
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
