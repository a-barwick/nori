"""Flush the fixed local Chromium profile before a normal guest shutdown."""
import json
import os
import sys
import time
from urllib.request import urlopen
import websocket

with urlopen("http://127.0.0.1:9222/json/version", timeout=3) as response:
    address = json.load(response)["webSocketDebuggerUrl"]
connection = websocket.create_connection(address, timeout=3, suppress_origin=True)
try:
    connection.send(json.dumps({"id": 1, "method": "Browser.close"}))
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        response = json.loads(connection.recv())
        if response.get("id") == 1:
            if "error" in response:
                raise RuntimeError("Browser.close rejected")
            break
    else:
        raise TimeoutError("No Browser.close response")
finally:
    connection.close()

if len(sys.argv) > 1:
    pid = int(sys.argv[1])
    for attempt in range(100):
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            break
        time.sleep(0.1)
    else:
        raise TimeoutError("Chromium did not exit after Browser.close")
